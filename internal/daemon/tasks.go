package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/rdkal/nexus/internal/config"
	"github.com/rdkal/nexus/internal/cron"
	"github.com/rdkal/nexus/internal/penv"
)

// taskRunFn runs a task's shell command in workDir with env, appending output to
// logFile, and returns its exit code. Injectable so tests don't shell out.
type taskRunFn func(ctx context.Context, command, workDir string, env []string, logFile string) (int, error)

// taskScheduler runs one project's tasks for its current deployment. It is created
// fresh on each deploy/recovery (capturing that deployment's SHA, worktree and
// env) and cancelled when the project stops or redeploys.
type taskScheduler struct {
	d          *Daemon
	address    string
	ref        string
	sha        string
	appDir     string
	ownVolumes map[string]struct{}
	projectEnv map[string]string
	parentEnv  map[string]string
	tasks      map[string]config.Task
	afterOf    map[string][]string // task name → tasks triggered by its success

	ctx     context.Context // set in start; used by manual triggers
	mu      sync.Mutex
	running map[string]bool
}

func (d *Daemon) newTaskScheduler(ps *projectState, cfg *config.ProjectFile) *taskScheduler {
	afterOf := make(map[string][]string)
	for name, t := range cfg.Tasks {
		if t.After != "" {
			afterOf[t.After] = append(afterOf[t.After], name)
		}
	}
	return &taskScheduler{
		d:          d,
		address:    ps.address,
		ref:        ps.ref,
		sha:        ps.sha,
		appDir:     filepath.Join(ps.worktree, ps.subdir),
		ownVolumes: cfg.Volumes,
		projectEnv: cfg.Environment,
		parentEnv:  ps.parentEnv,
		tasks:      cfg.Tasks,
		afterOf:    afterOf,
		running:    make(map[string]bool),
	}
}

// start launches a scheduling goroutine for every task with a schedule. Tasks
// triggered only by after: or manually simply wait to be fired.
func (s *taskScheduler) start(ctx context.Context) {
	s.ctx = ctx
	for name, t := range s.tasks {
		if t.Schedule == "" {
			continue
		}
		sched, err := cron.Parse(t.Schedule)
		if err != nil {
			slog.Error("task: invalid schedule; not scheduling", "task", s.key(name), "schedule", t.Schedule, "err", err)
			continue
		}
		go s.scheduleLoop(ctx, name, sched)
	}
}

func (s *taskScheduler) scheduleLoop(ctx context.Context, name string, sched cron.Schedule) {
	for {
		next := sched.Next(time.Now())
		if next.IsZero() {
			slog.Warn("task: schedule never fires again", "task", s.key(name))
			return
		}
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			s.fire(ctx, name, "schedule")
		}
	}
}

// fire starts a task run unless one is already active for that task (no self-overlap).
func (s *taskScheduler) fire(ctx context.Context, name, reason string) {
	if ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	if s.running[name] {
		s.mu.Unlock()
		slog.Warn("task: previous run still active; skipping", "task", s.key(name), "reason", reason)
		return
	}
	s.running[name] = true
	s.mu.Unlock()
	go s.run(ctx, name, reason)
}

func (s *taskScheduler) run(ctx context.Context, name, reason string) {
	defer func() {
		s.mu.Lock()
		delete(s.running, name)
		s.mu.Unlock()
	}()

	id, err := s.d.DB.AddTaskRun(s.address, name, reason, time.Now())
	if err != nil {
		slog.Error("task: could not record run", "task", s.key(name), "err", err)
	}
	exit, runErr := s.execute(ctx, name)
	status := "success"
	if runErr != nil || exit != 0 {
		status = "failed"
	}
	if id != 0 {
		_ = s.d.DB.FinishTaskRun(id, status, exit, time.Now())
	}
	slog.Info("task: run finished", "task", s.key(name), "reason", reason, "status", status, "exit", exit)

	// On success, fire the tasks that depend on this one. A failure stops the chain.
	if status == "success" {
		for _, dep := range s.afterOf[name] {
			s.fire(ctx, dep, "after:"+name)
		}
	}
}

// execute resolves the task's environment and runs its command.
func (s *taskScheduler) execute(ctx context.Context, name string) (int, error) {
	task := s.tasks[name]
	logFile := s.d.Paths.TaskLog(s.address, name)
	env, err := penv.Build(penv.Input{
		Paths:         s.d.Paths,
		Address:       s.address,
		Ref:           s.ref,
		SHA:           s.sha,
		WorkDir:       s.appDir,
		OwnVolumes:    s.ownVolumes,
		GlobalVolumes: s.d.globalVolumeEnv(), // resolved fresh: providers may deploy over time
		ProjectEnv:    s.projectEnv,
		ServiceEnv:    task.Environment,
		ParentEnv:     s.parentEnv,
	})
	if err != nil {
		_ = appendLine(logFile, "task environment error: "+err.Error())
		return -1, err
	}
	env = append(env, "NEXUS_TASK="+name)
	return s.d.runTask(ctx, task.Run, s.appDir, env, logFile)
}

func (s *taskScheduler) key(name string) string { return s.address + "/" + name }

// execTask is the default taskRunFn: sh -c in workDir, output appended to logFile.
func execTask(ctx context.Context, command, workDir string, env []string, logFile string) (int, error) {
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		return -1, err
	}
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return -1, err
	}
	defer f.Close()
	fmt.Fprintf(f, "\n=== run %s ===\n", time.Now().UTC().Format(time.RFC3339))

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = workDir
	cmd.Env = env
	cmd.Stdout = f
	cmd.Stderr = f
	err = cmd.Run()
	if err == nil {
		return 0, nil
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), err
	}
	return -1, err
}

func appendLine(logFile, line string) error {
	if err := os.MkdirAll(filepath.Dir(logFile), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintln(f, line)
	return err
}

// startTasks (re)starts the task scheduler for a project from its current config,
// cancelling any previous scheduler. Called after a deploy and on recovery.
func (d *Daemon) startTasks(ctx context.Context, ps *projectState) {
	ps.mu.Lock()
	if ps.taskCancel != nil {
		ps.taskCancel()
		ps.taskCancel = nil
		ps.scheduler = nil
	}
	cfg := ps.cfg
	ps.mu.Unlock()

	if cfg == nil || len(cfg.Tasks) == 0 {
		return
	}
	s := d.newTaskScheduler(ps, cfg)
	sctx, cancel := context.WithCancel(ctx)

	ps.mu.Lock()
	ps.taskCancel = cancel
	ps.scheduler = s
	ps.mu.Unlock()

	s.start(sctx)
	slog.Info("daemon: task scheduler started", "address", ps.address, "tasks", len(cfg.Tasks))
}

// triggerTask fires a task manually (and cascades to its after: dependents).
func (d *Daemon) triggerTask(address, task string) error {
	d.mu.RLock()
	ps := d.projects[address]
	d.mu.RUnlock()
	if ps == nil {
		return fmt.Errorf("project %q not found", address)
	}
	ps.mu.RLock()
	s := ps.scheduler
	ps.mu.RUnlock()
	if s == nil {
		return fmt.Errorf("project %q has no tasks", address)
	}
	if _, ok := s.tasks[task]; !ok {
		return fmt.Errorf("task %q not found in %q", task, address)
	}
	s.fire(s.ctx, task, "manual")
	return nil
}
