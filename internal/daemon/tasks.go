package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/rdkal/nexus/internal/config"
	"github.com/rdkal/nexus/internal/cron"
	"github.com/rdkal/nexus/internal/penv"
	"github.com/rdkal/nexus/internal/supervisor"
)

// taskPollInterval is how often the runtime polls nexus-pm for a run's outcome.
// A var so tests can shorten it.
var taskPollInterval = time.Second

// taskExecutor runs one-shot task commands out-of-process (in nexus-pm) so they
// survive a runtime restart, and lets the runtime poll for their outcome by id.
// Both *supervisor.Supervisor and *supervisor.RemoteSupervisor satisfy it.
type taskExecutor interface {
	StartRun(id string, spec supervisor.ServiceSpec) error
	PollRun(id string) (supervisor.RunState, bool, error)
	AckRun(id string)
}

// taskScheduler runs one project's tasks for its current deployment. It is created
// fresh on each deploy/recovery (capturing that deployment's SHA, worktree and
// env) and cancelled when the project stops or redeploys. No self-overlap and run
// history live in the DB, so they are correct across restarts.
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

	ctx context.Context // set in start; used by manual triggers
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

// fire starts a task run unless one is already in progress for that task (no
// self-overlap, checked against the DB so it holds across restarts).
func (s *taskScheduler) fire(ctx context.Context, name, reason string) {
	if ctx.Err() != nil {
		return
	}
	running, err := s.d.DB.HasRunningTaskRun(s.address, name)
	if err != nil {
		slog.Error("task: overlap check failed", "task", s.key(name), "err", err)
		return
	}
	if running {
		slog.Warn("task: previous run still active; skipping", "task", s.key(name), "reason", reason)
		return
	}
	go s.d.startTaskRun(s, name, reason)
}

func (s *taskScheduler) key(name string) string { return s.address + "/" + name }

// startTaskRun resolves the task's environment, records a run, and hands the
// command to nexus-pm; it then awaits the outcome by polling.
func (d *Daemon) startTaskRun(s *taskScheduler, name, reason string) {
	task := s.tasks[name]
	logFile := d.Paths.TaskLog(s.address, name)

	env, err := penv.Build(penv.Input{
		Paths:         d.Paths,
		Address:       s.address,
		Ref:           s.ref,
		SHA:           s.sha,
		WorkDir:       s.appDir,
		OwnVolumes:    s.ownVolumes,
		GlobalVolumes: d.globalVolumeEnv(),
		ProjectEnv:    s.projectEnv,
		ServiceEnv:    task.Environment,
		ParentEnv:     s.parentEnv,
	})
	if err != nil {
		_ = appendLine(logFile, "task environment error: "+err.Error())
		if id, aerr := d.DB.AddTaskRun(s.address, name, reason, time.Now()); aerr == nil {
			_ = d.DB.FinishTaskRun(id, "failed", -1, time.Now())
		}
		slog.Error("task: environment could not be resolved", "task", s.key(name), "err", err)
		return
	}
	env = append(env, "NEXUS_TASK="+name)

	id, err := d.DB.AddTaskRun(s.address, name, reason, time.Now())
	if err != nil {
		slog.Error("task: could not record run", "task", s.key(name), "err", err)
		return
	}
	if d.taskExec == nil {
		slog.Error("task: no executor configured", "task", s.key(name))
		_ = d.DB.FinishTaskRun(id, "failed", -1, time.Now())
		return
	}
	spec := supervisor.ServiceSpec{Command: task.Run, WorkDir: s.appDir, Env: env, LogFile: logFile}
	if err := d.taskExec.StartRun(runKey(id), spec); err != nil {
		slog.Error("task: could not start run", "task", s.key(name), "err", err)
		_ = d.DB.FinishTaskRun(id, "failed", -1, time.Now())
		return
	}
	slog.Info("task: run started", "task", s.key(name), "reason", reason, "id", id)
	d.awaitTaskRun(s.address, name, id)
}

// awaitTaskRun polls nexus-pm until the run finishes (or is lost), finalises the
// task_runs row, and cascades to after: dependents on success. It runs under the
// daemon's root context so a redeploy doesn't abandon it; on daemon shutdown it
// leaves the row 'running' for the next startup's recoverTaskRuns to finish.
func (d *Daemon) awaitTaskRun(address, task string, id int64) {
	ctx := d.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	key := runKey(id)
	for {
		state, known, err := d.taskExec.PollRun(key)
		switch {
		case err != nil:
			// transient (nexus-pm briefly unreachable) — keep waiting.
		case !known:
			// nexus-pm has no record (it restarted / the run was reaped): lost.
			_ = d.DB.FinishTaskRun(id, "failed", -1, time.Now())
			slog.Warn("task: run lost (no record in nexus-pm); marked failed", "task", address+"/"+task, "id", id)
			return
		case state.Done:
			status := "success"
			if state.ExitCode != 0 || state.Err != "" {
				status = "failed"
			}
			_ = d.DB.FinishTaskRun(id, status, state.ExitCode, time.Now())
			d.taskExec.AckRun(key)
			slog.Info("task: run finished", "task", address+"/"+task, "id", id, "status", status, "exit", state.ExitCode)
			if status == "success" {
				d.cascadeAfter(address, task)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(taskPollInterval):
		}
	}
}

// cascadeAfter fires the tasks whose after: names `task`, via the project's
// current scheduler.
func (d *Daemon) cascadeAfter(address, task string) {
	d.mu.RLock()
	ps := d.projects[address]
	d.mu.RUnlock()
	if ps == nil {
		return
	}
	ps.mu.RLock()
	s := ps.scheduler
	ps.mu.RUnlock()
	if s == nil {
		return
	}
	for _, dep := range s.afterOf[task] {
		s.fire(s.ctx, dep, "after:"+task)
	}
}

// recoverTaskRuns resumes polling for any task run left 'running' when the runtime
// last stopped — so a task that kept running under nexus-pm across a restart is
// finalised (and its after: cascade fired) rather than left dangling. Called at
// startup once schedulers are up.
func (d *Daemon) recoverTaskRuns() {
	if d.taskExec == nil {
		return
	}
	runs, err := d.DB.RunningTaskRuns()
	if err != nil {
		slog.Error("daemon: list running task runs", "err", err)
		return
	}
	for _, r := range runs {
		slog.Info("daemon: recovering in-flight task run", "task", r.Address+"/"+r.Task, "id", r.ID)
		go d.awaitTaskRun(r.Address, r.Task, r.ID)
	}
}

func runKey(id int64) string { return strconv.FormatInt(id, 10) }

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
