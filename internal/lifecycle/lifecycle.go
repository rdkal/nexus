package lifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rdkal/nexus/internal/config"
	"github.com/rdkal/nexus/internal/git"
	"github.com/rdkal/nexus/internal/home"
	"github.com/rdkal/nexus/internal/supervisor"
)

// DBWriter is the persistence interface used by the deployer.
type DBWriter interface {
	SetCurrentSHA(name, sha string) error
	AddDeployment(address, sha string, startedAt time.Time) (int64, error)
	FinishDeployment(id int64, status string, finishedAt time.Time) error
}

// Supervisor is the process supervision interface used by the deployer.
// The real *supervisor.Supervisor satisfies this interface.
type Supervisor interface {
	Spawn(name string, spec supervisor.ServiceSpec)
	Stop(name string)
	Status(name string) (supervisor.Status, bool)
}

// Deployer orchestrates the build-and-swap deployment lifecycle.
// All external I/O operations are injected — swap any field to change behaviour
// without touching the lifecycle logic itself.
type Deployer struct {
	DB    DBWriter
	Sup   Supervisor
	Paths home.Paths

	VerifyWindow    time.Duration // time to observe new services before promoting (default 5s)
	VerifyTickEvery time.Duration // how often to poll during verify (default 100ms)

	// Injectable git operations. Nil uses the real git package implementations.
	Fetch          func(repoDir string) error
	WorktreeAdd    func(repoDir, worktreePath, sha string) error
	WorktreeRemove func(repoDir, worktreePath string) error

	// Injectable config loader. Nil reads nexus.yaml from the worktree root.
	LoadConfig func(worktreePath string) (*config.ProjectFile, error)

	// Injectable build runner. Nil runs sh -c in the worktree and captures output to logFile.
	RunBuild func(command, workDir string, env []string, logFile string) error
}

// Request describes a single deployment.
type Request struct {
	// Project identity
	Name    string // project name used as the DB key (e.g. "my-system")
	Address string // resource address used for supervisor keys and log paths (e.g. "my-system")
	Ref     string // ref being tracked, injected as NEXUS_REF (e.g. "@main")

	// Git coordinates
	SpecPath     string   // spec path of this project's own git repo (repo root)
	RootSpecPath string   // spec path of the root deployment (for worktree placement)
	Aliases      []string // alias chain from root to this project; nil for root projects
	Subdir       string   // in-repo path to this app's nexus.yaml ("" = repo root)

	// Deployment SHAs
	NewSHA  string
	PrevSHA string // empty on first deployment

	// PrevConfig is the parsed nexus.yaml of the currently-running deployment.
	// Needed to know which services to stop during SHUTDOWN and to re-spawn on ROLLBACK.
	// Nil if this is the first deployment.
	PrevConfig *config.ProjectFile
}

// Deploy runs the full deployment lifecycle:
//
//	FETCH → CHECKOUT → BUILD → SHUTDOWN → STARTUP → VERIFY → PROMOTE → CLEANUP
//
// The project and its inline sub-projects (projects: entries without a src:) are
// flattened into units and deployed atomically: all builds run, then all old
// services stop, all new services start, and all are verified together. Inline
// units share this deployment's worktree; their services are addressed
// <address>/<alias>/<service>. External sub-projects are handled separately by
// the daemon and are not part of this deployment.
//
// On build failure the new worktree is removed and previous services are left running.
// On verify failure ROLLBACK stops new services and restores previous services.
func (d *Deployer) Deploy(ctx context.Context, req Request) error {
	// Resolve injectable operations once, falling back to real implementations.
	fetch := d.Fetch
	if fetch == nil {
		fetch = git.Fetch
	}
	worktreeAdd := d.WorktreeAdd
	if worktreeAdd == nil {
		worktreeAdd = git.WorktreeAdd
	}
	worktreeRemove := d.WorktreeRemove
	if worktreeRemove == nil {
		worktreeRemove = git.WorktreeRemove
	}
	loadConfig := d.LoadConfig
	if loadConfig == nil {
		loadConfig = func(wt string) (*config.ProjectFile, error) {
			return config.Parse(filepath.Join(wt, "nexus.yaml"))
		}
	}
	runBuild := d.RunBuild
	if runBuild == nil {
		runBuild = defaultRunBuild
	}

	repoDir := d.Paths.RepoDir(req.SpecPath)
	newWorktree := d.Paths.WorktreeDir(req.RootSpecPath, req.Aliases, req.NewSHA)
	var prevWorktree string
	if req.PrevSHA != "" {
		prevWorktree = d.Paths.WorktreeDir(req.RootSpecPath, req.Aliases, req.PrevSHA)
	}
	// The worktree is the whole checkout; the app's nexus.yaml, build, and services
	// live in its subdirectory (Subdir is "" for a repo-root project, so newDir ==
	// newWorktree in that case). git worktree add/remove still operate on the
	// checkout root above.
	newDir := filepath.Join(newWorktree, req.Subdir)

	// A redeploy of the currently-active SHA resolves the new and previous worktree
	// to the same path. In that case we reuse the existing worktree instead of
	// re-checking it out, and skip cleanup so we never remove the live worktree.
	sameWorktree := prevWorktree != "" && newWorktree == prevWorktree

	// removeNewWorktree discards the new worktree on an abort path — but never when
	// it is the same worktree the current services are running from (same-SHA redeploy).
	removeNewWorktree := func() {
		if !sameWorktree {
			_ = worktreeRemove(repoDir, newWorktree)
		}
	}

	// FETCH: download objects from origin.
	if err := fetch(repoDir); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}

	// CHECKOUT: create an isolated worktree at the new SHA.
	// Skipped on a same-SHA redeploy — the worktree is already checked out.
	if !sameWorktree {
		if err := worktreeAdd(repoDir, newWorktree, req.NewSHA); err != nil {
			return fmt.Errorf("checkout: %w", err)
		}
	}

	// LOAD CONFIG from the app's directory and flatten it (plus the previous config)
	// into inline units to deploy atomically.
	cfg, err := loadConfig(newDir)
	if err != nil {
		removeNewWorktree()
		return fmt.Errorf("load config: %w", err)
	}
	newUnits, _ := cfg.Flatten()
	var prevUnits []config.InlineUnit
	if req.PrevConfig != nil {
		prevUnits, _ = req.PrevConfig.Flatten()
	}

	// RECORD: open a deployment record in the DB.
	depID, err := d.DB.AddDeployment(req.Address, req.NewSHA, time.Now())
	if err != nil {
		removeNewWorktree()
		return fmt.Errorf("record deployment: %w", err)
	}

	// BUILD each unit that declares one (skipped when no build: field). All builds
	// run in the shared worktree, in deterministic (sorted) unit order.
	for _, u := range newUnits {
		if u.Build == "" {
			continue
		}
		uAddr := unitAddress(req.Address, u.RelPath)
		env := d.unitEnv(uAddr, req.Ref, req.NewSHA, newDir, u.Volumes)
		buildLog := d.Paths.BuildLog(uAddr, req.NewSHA)
		if err := runBuild(u.Build, newDir, env, buildLog); err != nil {
			removeNewWorktree()
			_ = d.DB.FinishDeployment(depID, "failed", time.Now())
			return fmt.Errorf("build %s: %w", uAddr, err)
		}
	}

	// SHUTDOWN: stop all services of the current deployment (across all units) in parallel.
	d.stopUnits(req.Address, prevUnits)

	// STARTUP: spawn services for every unit from the new worktree.
	for _, u := range newUnits {
		uAddr := unitAddress(req.Address, u.RelPath)
		env := d.unitEnv(uAddr, req.Ref, req.NewSHA, newDir, u.Volumes)
		for name, svc := range u.Services {
			key := serviceKey(uAddr, name)
			d.Sup.Spawn(key, supervisor.ServiceSpec{
				Command: svc.Run,
				WorkDir: newDir,
				Env:     env,
				LogFile: d.Paths.ServiceLog(key),
			})
		}
	}

	// VERIFY: observe services for VerifyWindow; any crash triggers rollback.
	if err := d.verify(newUnits, req.Address); err != nil {
		slog.Warn("deploy: verify failed, rolling back",
			"address", req.Address, "sha", req.NewSHA, "err", err)
		d.rollback(req, newUnits, prevUnits, prevWorktree, removeNewWorktree)
		_ = d.DB.FinishDeployment(depID, "rolled_back", time.Now())
		return fmt.Errorf("verify: %w", err)
	}

	// PROMOTE: record the new SHA as active.
	if err := d.DB.SetCurrentSHA(req.Name, req.NewSHA); err != nil {
		slog.Error("deploy: SetCurrentSHA failed", "name", req.Name, "sha", req.NewSHA, "err", err)
	}
	_ = d.DB.FinishDeployment(depID, "active", time.Now())

	// CLEANUP: discard the old worktree now that services are running from the new one.
	// Skipped on a same-SHA redeploy — the "old" worktree is the live one.
	if prevWorktree != "" && !sameWorktree {
		if err := worktreeRemove(repoDir, prevWorktree); err != nil {
			slog.Warn("deploy: cleanup old worktree failed", "path", prevWorktree, "err", err)
		}
	}

	slog.Info("deploy: success", "address", req.Address, "sha", req.NewSHA)
	return nil
}

// rollback stops new services and restores the previous deployment.
// removeNewWorktree discards the failed worktree; it is a no-op on a same-SHA
// redeploy, where the "new" and "previous" worktree are the same live checkout.
func (d *Deployer) rollback(
	req Request,
	newUnits, prevUnits []config.InlineUnit,
	prevWorktree string,
	removeNewWorktree func(),
) {
	// Stop new (failed) services across all units.
	d.stopUnits(req.Address, newUnits)

	// Re-spawn old services (across all units) from the previous worktree's app dir.
	if prevWorktree != "" {
		prevDir := filepath.Join(prevWorktree, req.Subdir)
		for _, u := range prevUnits {
			uAddr := unitAddress(req.Address, u.RelPath)
			env := d.unitEnv(uAddr, req.Ref, req.PrevSHA, prevDir, u.Volumes)
			for name, svc := range u.Services {
				key := serviceKey(uAddr, name)
				d.Sup.Spawn(key, supervisor.ServiceSpec{
					Command: svc.Run,
					WorkDir: prevDir,
					Env:     env,
					LogFile: d.Paths.ServiceLog(key),
				})
			}
		}
	}

	// Remove the failed worktree (skipped on a same-SHA redeploy).
	removeNewWorktree()
}

// stopUnits stops every service across the given units, in parallel.
func (d *Deployer) stopUnits(base string, units []config.InlineUnit) {
	var wg sync.WaitGroup
	for _, u := range units {
		uAddr := unitAddress(base, u.RelPath)
		for name := range u.Services {
			wg.Add(1)
			key := serviceKey(uAddr, name)
			go func(k string) {
				defer wg.Done()
				d.Sup.Stop(k)
			}(key)
		}
	}
	wg.Wait()
}

// verify polls all service statuses (across all units) for VerifyWindow. Returns an
// error if any service crashes (Restarts > 0) or becomes degraded within the window.
func (d *Deployer) verify(units []config.InlineUnit, base string) error {
	total := 0
	for _, u := range units {
		total += len(u.Services)
	}
	if total == 0 {
		return nil
	}

	window := d.VerifyWindow
	if window == 0 {
		window = 5 * time.Second
	}
	tickEvery := d.VerifyTickEvery
	if tickEvery == 0 {
		tickEvery = 100 * time.Millisecond
	}

	deadline := time.NewTimer(window)
	defer deadline.Stop()
	tick := time.NewTicker(tickEvery)
	defer tick.Stop()

	for {
		select {
		case <-deadline.C:
			return nil
		case <-tick.C:
			for _, u := range units {
				uAddr := unitAddress(base, u.RelPath)
				for name := range u.Services {
					key := serviceKey(uAddr, name)
					st, ok := d.Sup.Status(key)
					if !ok {
						return fmt.Errorf("service %q disappeared", key)
					}
					if st.Restarts > 0 {
						return fmt.Errorf("service %q crashed (restarts=%d)", key, st.Restarts)
					}
					if st.Degraded {
						return fmt.Errorf("service %q degraded", key)
					}
				}
			}
		}
	}
}

// unitEnv builds the environment for a unit's services: the host environment plus
// NEXUS_* variables and the unit's volume paths (namespaced by the unit's address).
func (d *Deployer) unitEnv(address, ref, sha, worktree string, volumes map[string]struct{}) []string {
	env := append(os.Environ(),
		"NEXUS_PROJECT="+address,
		"NEXUS_SHA="+sha,
		"NEXUS_REF="+ref,
		"NEXUS_WORKTREE="+worktree,
	)
	for name := range volumes {
		upper := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		volPath := d.Paths.VolumeDir(address, name)
		_ = os.MkdirAll(volPath, 0o755)
		env = append(env, "NEXUS_VOLUME_"+upper+"="+volPath)
	}
	return env
}

// unitAddress joins a base address with a unit's relative alias chain.
func unitAddress(base string, rel []string) string {
	if len(rel) == 0 {
		return base
	}
	return base + "/" + strings.Join(rel, "/")
}

// serviceKey combines a resource address and service name into a supervisor key.
func serviceKey(address, service string) string {
	return address + "/" + service
}

func defaultRunBuild(command, workDir string, env []string, logFile string) error {
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open build log: %w", err)
	}
	defer f.Close()

	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = workDir
	cmd.Env = env
	cmd.Stdout = f
	cmd.Stderr = f
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build command: %w", err)
	}
	return nil
}
