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

// SupervisorAPI is the subset of supervisor.Supervisor used by the daemon.
type SupervisorAPI interface {
	Spawn(name string, spec supervisor.ServiceSpec)
	Stop(name string)
	Status(name string) (supervisor.Status, bool)
}

// Daemon wires together the git poller, deployment lifecycle, and process supervisor.
type Daemon struct {
	DB    *db.DB
	Sup   SupervisorAPI
	Paths home.Paths

	mu       sync.RWMutex
	projects map[string]*projectState
}

// projectState holds live runtime state for one root project.
type projectState struct {
	project  db.Project
	queue    *poller.Queue
	cancel   context.CancelFunc

	mu       sync.RWMutex
	sha      string                          // current deployed SHA
	cfg      *config.ProjectFile             // current deployed config (nil = not deployed)
	worktree string                          // current deployed worktree path
	svcSpecs map[string]supervisor.ServiceSpec // keyed by service name
}

// New creates a Daemon ready to be started with Run.
func New(database *db.DB, sup SupervisorAPI, paths home.Paths) *Daemon {
	return &Daemon{
		DB:       database,
		Sup:      sup,
		Paths:    paths,
		projects: make(map[string]*projectState),
	}
}

// Run loads all projects from the DB, recovers any previously running services,
// starts the git polling loops, and serves the Unix socket API.
// It blocks until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	projects, err := d.DB.ListProjects()
	if err != nil {
		return err
	}
	for _, p := range projects {
		if err := d.startProject(ctx, p); err != nil {
			slog.Error("daemon: failed to start project", "project", p.Name, "err", err)
		}
	}
	return d.serve(ctx)
}

// startProject initialises a projectState, optionally recovers running services,
// and launches the poller and deploy-loop goroutines.
func (d *Daemon) startProject(ctx context.Context, p db.Project) error {
	repoDir := d.Paths.RepoDir(p.SpecPath)
	if err := git.EnsureBareClone(repoDir, p.SpecPath); err != nil {
		return err
	}

	pctx, cancel := context.WithCancel(ctx)
	ps := &projectState{
		project:  p,
		queue:    &poller.Queue{},
		cancel:   cancel,
		svcSpecs: make(map[string]supervisor.ServiceSpec),
	}

	if p.CurrentSHA != "" {
		d.recoverProject(ps)
	}

	d.mu.Lock()
	d.projects[p.Name] = ps
	d.mu.Unlock()

	go d.runPoller(pctx, ps)
	go d.deployLoop(pctx, ps)
	return nil
}

// recoverProject attempts to restart services from the last known-good worktree.
func (d *Daemon) recoverProject(ps *projectState) {
	sha := ps.project.CurrentSHA
	worktree := d.Paths.WorktreeDir(ps.project.SpecPath, nil, sha)

	cfg, err := config.Parse(filepath.Join(worktree, "nexus.yaml"))
	if err != nil {
		slog.Warn("daemon: recover skipped (no worktree config)",
			"project", ps.project.Name, "worktree", worktree, "err", err)
		return
	}

	env := d.buildEnv(ps.project, cfg, sha, worktree)
	specs := make(map[string]supervisor.ServiceSpec)
	for name, svc := range cfg.Services {
		key := serviceKey(ps.project.Name, name)
		spec := supervisor.ServiceSpec{
			Command: svc.Run,
			WorkDir: worktree,
			Env:     env,
			LogFile: d.Paths.ServiceLog(key),
		}
		specs[name] = spec
		d.Sup.Spawn(key, spec)
	}

	ps.sha = sha
	ps.cfg = cfg
	ps.worktree = worktree
	ps.svcSpecs = specs
	slog.Info("daemon: recovered project", "project", ps.project.Name, "sha", sha)
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
		RepoDir:  d.Paths.RepoDir(ps.project.SpecPath),
		Ref:      ps.project.Ref,
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
			Name:         ps.project.Name,
			Address:      ps.project.Name,
			Ref:          ps.project.Ref,
			SpecPath:     ps.project.SpecPath,
			RootSpecPath: ps.project.SpecPath,
			NewSHA:       sha,
			PrevSHA:      prevSHA,
			PrevConfig:   prevCfg,
		}

		if err := dep.Deploy(ctx, req); err != nil {
			slog.Error("daemon: deploy failed", "project", ps.project.Name, "sha", sha, "err", err)
			continue
		}

		// Capture service specs so manual restarts can re-spawn with the same config.
		newWorktree := d.Paths.WorktreeDir(ps.project.SpecPath, nil, sha)
		cfg, err := config.Parse(filepath.Join(newWorktree, "nexus.yaml"))
		if err != nil {
			slog.Error("daemon: post-deploy config reload failed", "project", ps.project.Name, "err", err)
			cfg = &config.ProjectFile{}
		}

		env := d.buildEnv(ps.project, cfg, sha, newWorktree)
		specs := make(map[string]supervisor.ServiceSpec)
		for name, svc := range cfg.Services {
			key := serviceKey(ps.project.Name, name)
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
	}
}

// buildEnv constructs the service environment slice with NEXUS_* variables.
func (d *Daemon) buildEnv(p db.Project, cfg *config.ProjectFile, sha, worktree string) []string {
	env := append(os.Environ(),
		"NEXUS_PROJECT="+p.Name,
		"NEXUS_SHA="+sha,
		"NEXUS_REF="+p.Ref,
		"NEXUS_WORKTREE="+worktree,
	)
	for name := range cfg.Volumes {
		upper := strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		volPath := d.Paths.VolumeDir(p.Name, name)
		_ = os.MkdirAll(volPath, 0o755)
		env = append(env, "NEXUS_VOLUME_"+upper+"="+volPath)
	}
	return env
}

// InjectProject seeds in-memory project state without starting the poller or deploy loop.
// Intended for use in tests that exercise socket handlers directly.
func (d *Daemon) InjectProject(name string, cfg *config.ProjectFile, sha string) {
	_, cancel := context.WithCancel(context.Background())
	ps := &projectState{
		project:  db.Project{Name: name},
		queue:    &poller.Queue{},
		cancel:   cancel,
		cfg:      cfg,
		sha:      sha,
		svcSpecs: make(map[string]supervisor.ServiceSpec),
	}
	d.mu.Lock()
	d.projects[name] = ps
	d.mu.Unlock()
}

func serviceKey(project, service string) string { return project + "/" + service }
