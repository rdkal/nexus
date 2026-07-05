package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/rdkal/nexus/internal/config"
	"github.com/rdkal/nexus/internal/db"
	"github.com/rdkal/nexus/internal/git"
	"github.com/rdkal/nexus/internal/home"
	"github.com/rdkal/nexus/internal/lifecycle"
	"github.com/rdkal/nexus/internal/poller"
	"github.com/rdkal/nexus/internal/supervisor"
)

// defaultSelfSpecPath is the spec path of nexus's own repository. A project with
// this spec path is nexus itself: after it deploys, the runtime is restarted so
// nexus-pm loads the freshly built binary. Overridable via NEXUS_SELF_SPEC (for
// forks or tests). Empty disables self-update restarts entirely.
const defaultSelfSpecPath = "github.com/rdkal/nexus"

// SupervisorAPI is the subset of supervisor.Supervisor used by the daemon.
type SupervisorAPI interface {
	Spawn(name string, spec supervisor.ServiceSpec)
	Stop(name string)
	Status(name string) (supervisor.Status, bool)
}

// runtimeRestarter is implemented by the remote supervisor (RemoteSupervisor):
// it asks nexus-pm to restart the nexus runtime binary. The in-process supervisor
// does not implement it — self-update restarts only apply to the split deployment.
type runtimeRestarter interface {
	RestartRuntime() error
}

// Daemon wires together the git poller, deployment lifecycle, and process supervisor.
type Daemon struct {
	DB    *db.DB
	Sup   SupervisorAPI
	Paths home.Paths

	// SelfSpecPath identifies nexus's own project. When a project with this spec
	// path finishes deploying, the runtime is restarted onto the new binary.
	// Defaults to defaultSelfSpecPath (or $NEXUS_SELF_SPEC); empty disables it.
	SelfSpecPath string

	mu sync.RWMutex
	// projects holds every live project keyed by resource address. This includes
	// both root projects and discovered external sub-projects (e.g. "my-system/db").
	projects map[string]*projectState
}

// projectState holds live runtime state for one project — a root project or an
// external sub-project discovered from a parent's nexus.yaml. The two are handled
// identically; a sub-project simply carries a non-nil alias chain and a distinct
// root spec path for worktree placement.
type projectState struct {
	address      string // resource address; root name, or "<root>/<alias>/..." for sub-projects
	specPath     string // this project's own git repo spec path
	rootSpecPath string // root deployment's spec path (for worktree placement)
	ref          string // ref being tracked (e.g. "@main")
	aliases      []string // alias chain from root; nil for root projects
	recoverSHA   string   // SHA to recover on startup ("" = never deployed)

	queue  *poller.Queue
	cancel context.CancelFunc

	mu       sync.RWMutex
	sha      string                            // current deployed SHA
	cfg      *config.ProjectFile               // current deployed config (nil = not deployed)
	worktree string                            // current deployed worktree path
	svcSpecs map[string]supervisor.ServiceSpec // keyed by service name
	children map[string]*projectState          // external sub-projects, keyed by alias
}

// New creates a Daemon ready to be started with Run.
func New(database *db.DB, sup SupervisorAPI, paths home.Paths) *Daemon {
	selfSpec := defaultSelfSpecPath
	if v, ok := os.LookupEnv("NEXUS_SELF_SPEC"); ok {
		selfSpec = v // may be empty, which disables self-update restarts
	}
	return &Daemon{
		DB:           database,
		Sup:          sup,
		Paths:        paths,
		SelfSpecPath: selfSpec,
		projects:     make(map[string]*projectState),
	}
}

// Run loads all root projects from the DB, recovers any previously running services
// (and their sub-projects), starts the git polling loops, and serves the Unix socket
// API. It blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	projects, err := d.DB.ListProjects()
	if err != nil {
		return err
	}
	for _, p := range projects {
		ps := &projectState{
			address:      p.Name,
			specPath:     p.SpecPath,
			rootSpecPath: p.SpecPath,
			ref:          p.Ref,
			recoverSHA:   p.CurrentSHA,
			queue:        &poller.Queue{},
			svcSpecs:     make(map[string]supervisor.ServiceSpec),
		}
		if err := d.startProjectState(ctx, ps); err != nil {
			slog.Error("daemon: failed to start project", "address", ps.address, "err", err)
		}
	}
	return d.serve(ctx)
}

// startProjectState ensures a bare clone, recovers previously running services if
// this project has a deployed SHA, registers the project, and launches its poller
// and deploy-loop goroutines. The project's context is derived from ctx, so
// cancelling ctx (or a parent sub-project's context) stops this project too.
func (d *Daemon) startProjectState(ctx context.Context, ps *projectState) error {
	repoDir := d.Paths.RepoDir(ps.specPath)
	if err := git.EnsureBareClone(repoDir, ps.specPath); err != nil {
		return err
	}

	pctx, cancel := context.WithCancel(ctx)
	ps.cancel = cancel

	if ps.recoverSHA != "" {
		d.recoverProject(pctx, ps)
	}

	d.mu.Lock()
	d.projects[ps.address] = ps
	d.mu.Unlock()

	go d.runPoller(pctx, ps)
	go d.deployLoop(pctx, ps)
	return nil
}

// recoverProject restarts services from the last known-good worktree and
// reconciles any external sub-projects declared in that worktree's config.
func (d *Daemon) recoverProject(ctx context.Context, ps *projectState) {
	sha := ps.recoverSHA
	worktree := d.Paths.WorktreeDir(ps.rootSpecPath, ps.aliases, sha)

	cfg, err := config.Parse(filepath.Join(worktree, "nexus.yaml"))
	if err != nil {
		slog.Warn("daemon: recover skipped (no worktree config)",
			"address", ps.address, "worktree", worktree, "err", err)
		return
	}

	env := d.buildEnv(ps.address, ps.ref, cfg, sha, worktree)
	specs := make(map[string]supervisor.ServiceSpec)
	for name, svc := range cfg.Services {
		key := serviceKey(ps.address, name)
		spec := supervisor.ServiceSpec{
			Command: svc.Run,
			WorkDir: worktree,
			Env:     env,
			LogFile: d.Paths.ServiceLog(key),
		}
		specs[name] = spec
		d.Sup.Spawn(key, spec)
	}

	ps.mu.Lock()
	ps.sha = sha
	ps.cfg = cfg
	ps.worktree = worktree
	ps.svcSpecs = specs
	ps.mu.Unlock()
	slog.Info("daemon: recovered project", "address", ps.address, "sha", sha)

	d.reconcileChildren(ctx, ps, cfg)
}

// runPoller runs the git polling loop for a project.
func (d *Daemon) runPoller(ctx context.Context, ps *projectState) {
	ps.mu.RLock()
	lastSHA := ps.sha
	ps.mu.RUnlock()

	interval := 30 * time.Second
	if v := os.Getenv("NEXUS_POLL_INTERVAL"); v != "" {
		if parsed, err := time.ParseDuration(v); err == nil {
			interval = parsed
		}
	}

	pol := &poller.Poller{
		RepoDir:  d.Paths.RepoDir(ps.specPath),
		Ref:      ps.ref,
		Interval: interval,
	}
	pol.Run(ctx, ps.queue, lastSHA)
}

// deployLoop consumes SHAs from the queue and runs the deployment lifecycle.
func (d *Daemon) deployLoop(ctx context.Context, ps *projectState) {
	dep := &lifecycle.Deployer{
		DB:    d.DB,
		Sup:   d.Sup,
		Paths: d.Paths,
	}

	for {
		sha, ok := ps.queue.WaitPop(ctx)
		if !ok {
			return
		}

		ps.mu.RLock()
		prevSHA := ps.sha
		prevCfg := ps.cfg
		ps.mu.RUnlock()

		req := lifecycle.Request{
			Name:         ps.address,
			Address:      ps.address,
			Ref:          ps.ref,
			SpecPath:     ps.specPath,
			RootSpecPath: ps.rootSpecPath,
			Aliases:      ps.aliases,
			NewSHA:       sha,
			PrevSHA:      prevSHA,
			PrevConfig:   prevCfg,
		}

		if err := dep.Deploy(ctx, req); err != nil {
			slog.Error("daemon: deploy failed", "address", ps.address, "sha", sha, "err", err)
			continue
		}

		// Capture service specs so manual restarts can re-spawn with the same config.
		newWorktree := d.Paths.WorktreeDir(ps.rootSpecPath, ps.aliases, sha)
		cfg, reloadErr := config.Parse(filepath.Join(newWorktree, "nexus.yaml"))
		if reloadErr != nil {
			slog.Error("daemon: post-deploy config reload failed", "address", ps.address, "err", reloadErr)
			cfg = &config.ProjectFile{}
		}

		env := d.buildEnv(ps.address, ps.ref, cfg, sha, newWorktree)
		specs := make(map[string]supervisor.ServiceSpec)
		for name, svc := range cfg.Services {
			key := serviceKey(ps.address, name)
			specs[name] = supervisor.ServiceSpec{
				Command: svc.Run,
				WorkDir: newWorktree,
				Env:     env,
				LogFile: d.Paths.ServiceLog(key),
			}
		}

		ps.mu.Lock()
		ps.sha = sha
		ps.cfg = cfg
		ps.worktree = newWorktree
		ps.svcSpecs = specs
		ps.mu.Unlock()

		// Start/stop external sub-projects declared in the newly deployed config.
		// Skip on a reload error: an empty config would spuriously tear down every
		// sub-project even though the deployment itself succeeded.
		if reloadErr == nil {
			d.reconcileChildren(ctx, ps, cfg)
		}

		// Self-update: if this project is nexus itself, the build step has already
		// swapped $NEXUS_HOME/bin/nexus and the SHA is promoted. Ask nexus-pm to
		// restart the runtime so the new binary is loaded. This SIGTERMs the current
		// process, so it does not return; user services keep running throughout.
		if d.isSelf(ps.specPath) {
			d.restartRuntime(ps.address)
		}
	}
}

// reconcileChildren starts external sub-projects newly declared in cfg and stops
// those that have been removed. External sub-projects nested inside inline
// projects are discovered via Flatten and keyed by their relative address (the
// alias chain from this project), so the diff is stable across nesting levels.
func (d *Daemon) reconcileChildren(ctx context.Context, ps *projectState, cfg *config.ProjectFile) {
	_, externals := cfg.Flatten()
	desired := make(map[string]config.ExternalRef, len(externals))
	for _, ext := range externals {
		desired[strings.Join(ext.RelPath, "/")] = ext
	}

	var toStart []config.ExternalRef
	var toStop []*projectState

	ps.mu.Lock()
	if ps.children == nil {
		ps.children = make(map[string]*projectState)
	}
	for key, ext := range desired {
		if _, ok := ps.children[key]; !ok {
			toStart = append(toStart, ext)
		}
	}
	for key, child := range ps.children {
		if _, ok := desired[key]; !ok {
			toStop = append(toStop, child)
			delete(ps.children, key)
		}
	}
	ps.mu.Unlock()

	for _, child := range toStop {
		slog.Info("daemon: removing sub-project", "address", child.address)
		d.stopProjectState(child)
	}
	for _, ext := range toStart {
		d.startChild(ctx, ps, ext)
	}
}

// startChild builds and starts an external sub-project under parent.
func (d *Daemon) startChild(ctx context.Context, parent *projectState, ext config.ExternalRef) {
	ref := ext.Ref
	if ref == "" {
		ref = "@main"
	}
	relKey := strings.Join(ext.RelPath, "/")
	childAddr := parent.address + "/" + relKey
	aliases := append(append([]string{}, parent.aliases...), ext.RelPath...)

	sha, err := d.DB.CurrentSHA(childAddr)
	if err != nil {
		slog.Error("daemon: sub-project current SHA lookup failed", "address", childAddr, "err", err)
	}

	child := &projectState{
		address:      childAddr,
		specPath:     ext.Src,
		rootSpecPath: parent.rootSpecPath,
		ref:          ref,
		aliases:      aliases,
		recoverSHA:   sha,
		queue:        &poller.Queue{},
		svcSpecs:     make(map[string]supervisor.ServiceSpec),
	}

	parent.mu.Lock()
	if parent.children == nil {
		parent.children = make(map[string]*projectState)
	}
	parent.children[relKey] = child
	parent.mu.Unlock()

	slog.Info("daemon: starting sub-project", "address", childAddr, "src", ext.Src, "ref", ref)
	if err := d.startProjectState(ctx, child); err != nil {
		slog.Error("daemon: failed to start sub-project", "address", childAddr, "err", err)
	}
}

// stopProjectState cancels a project's loops, stops all of its services, and
// recursively stops its sub-projects. Used when a sub-project is removed from its
// parent's config. It is not called on daemon shutdown — nexus-pm keeps services
// running across a runtime restart.
func (d *Daemon) stopProjectState(ps *projectState) {
	if ps.cancel != nil {
		ps.cancel()
	}

	ps.mu.RLock()
	specs := ps.svcSpecs
	children := make([]*projectState, 0, len(ps.children))
	for _, c := range ps.children {
		children = append(children, c)
	}
	ps.mu.RUnlock()

	for name := range specs {
		d.Sup.Stop(serviceKey(ps.address, name))
	}
	for _, child := range children {
		d.stopProjectState(child)
	}

	d.mu.Lock()
	delete(d.projects, ps.address)
	d.mu.Unlock()
}

// isSelf reports whether specPath identifies nexus's own repository.
func (d *Daemon) isSelf(specPath string) bool {
	return d.SelfSpecPath != "" && specPath == d.SelfSpecPath
}

// restartRuntime asks nexus-pm to restart the nexus runtime onto the newly built
// binary. It is a no-op (with a warning) when the supervisor cannot restart the
// runtime — e.g. an in-process supervisor outside the split deployment.
func (d *Daemon) restartRuntime(project string) {
	rr, ok := d.Sup.(runtimeRestarter)
	if !ok {
		slog.Warn("daemon: self-update deployed but supervisor cannot restart runtime",
			"project", project)
		return
	}
	slog.Info("daemon: self-update — requesting runtime restart", "project", project)
	if err := rr.RestartRuntime(); err != nil {
		slog.Error("daemon: runtime restart failed", "project", project, "err", err)
	}
}

// buildEnv constructs the service environment slice with NEXUS_* variables.
// address is the project's resource address (used for NEXUS_PROJECT and volume paths).
func (d *Daemon) buildEnv(address, ref string, cfg *config.ProjectFile, sha, worktree string) []string {
	env := append(os.Environ(),
		"NEXUS_PROJECT="+address,
		"NEXUS_SHA="+sha,
		"NEXUS_REF="+ref,
		"NEXUS_WORKTREE="+worktree,
	)
	for name := range cfg.Volumes {
		upper := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		volPath := d.Paths.VolumeDir(address, name)
		_ = os.MkdirAll(volPath, 0o755)
		env = append(env, "NEXUS_VOLUME_"+upper+"="+volPath)
	}
	return env
}

// InjectProject seeds in-memory project state without starting the poller or deploy loop.
// Intended for use in tests that exercise socket handlers directly.
func (d *Daemon) InjectProject(name string, cfg *config.ProjectFile, sha string) {
	ps := &projectState{
		address:      name,
		specPath:     name,
		rootSpecPath: name,
		queue:        &poller.Queue{},
		cfg:          cfg,
		sha:          sha,
		svcSpecs:     make(map[string]supervisor.ServiceSpec),
	}
	d.mu.Lock()
	d.projects[name] = ps
	d.mu.Unlock()
}

func serviceKey(address, service string) string { return address + "/" + service }
