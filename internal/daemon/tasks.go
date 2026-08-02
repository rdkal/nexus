package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rdkal/nexus/internal/config"
	"github.com/rdkal/nexus/internal/cron"
	"github.com/rdkal/nexus/internal/db"
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

	fireMu sync.Mutex      // serialises claim-a-run so parents can't double-fire a join
	ctx    context.Context // set in start; used by manual triggers
}

func (d *Daemon) newTaskScheduler(ps *projectState, cfg *config.ProjectFile) *taskScheduler {
	afterOf := make(map[string][]string)
	for name, t := range cfg.Tasks {
		for _, parent := range t.After {
			afterOf[parent] = append(afterOf[parent], name)
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
			s.launch(ctx, name, "schedule")
		}
	}
}

// launch claims a run slot for a task (no self-overlap, atomic under fireMu so it
// holds against concurrent triggers) and runs it. A no-op if a run is already in
// progress for that task — enforced against the DB, so it survives restarts.
func (s *taskScheduler) launch(ctx context.Context, name, reason string) {
	if ctx.Err() != nil {
		return
	}
	s.fireMu.Lock()
	id, ok := s.claimLocked(name, reason)
	s.fireMu.Unlock()
	if ok {
		go s.d.runTask(s, name, id)
	}
}

// claimLocked records a 'running' task_runs row unless one already exists for the
// task, returning the new run id. The caller holds fireMu, so concurrent triggers
// in this process serialise here; but the guarantee does not rest on that lock —
// the DB's partial unique index rejects a second 'running' row, so AddTaskRun
// returning ErrTaskRunning is the hard backstop that also covers a scheduler swap
// or a second process racing the in-process check below.
func (s *taskScheduler) claimLocked(name, reason string) (int64, bool) {
	// Fast path: skip cleanly when a run is already active (the common case).
	running, err := s.d.DB.HasRunningTaskRun(s.address, name)
	if err != nil {
		slog.Error("task: overlap check failed", "task", s.key(name), "err", err)
		return 0, false
	}
	if running {
		slog.Warn("task: previous run still active; skipping", "task", s.key(name), "reason", reason)
		return 0, false
	}
	id, err := s.d.DB.AddTaskRun(s.address, name, reason, time.Now())
	if err != nil {
		if errors.Is(err, db.ErrTaskRunning) {
			// Lost the race after the check above: another claim inserted first.
			slog.Warn("task: previous run still active; skipping", "task", s.key(name), "reason", reason)
			return 0, false
		}
		slog.Error("task: could not record run", "task", s.key(name), "err", err)
		return 0, false
	}
	return id, true
}

// fireJoin fires a fan-in join (a task with several after: parents) only when the
// barrier is satisfied: every parent has a successful run newer than the join's
// own last run. Re-evaluated under fireMu against the freshly-claimed run id, so
// two parents finishing at once fire the join exactly once and each parent
// success is consumed by the fire it completes.
func (s *taskScheduler) fireJoin(ctx context.Context, dep string, parents []string) {
	if ctx.Err() != nil {
		return
	}
	s.fireMu.Lock()
	defer s.fireMu.Unlock()

	last, err := s.d.DB.LastTaskRunID(s.address, dep)
	if err != nil {
		slog.Error("task: join watermark lookup failed", "task", s.key(dep), "err", err)
		return
	}
	for _, p := range parents {
		ready, err := s.d.DB.HasSuccessfulTaskRunAfter(s.address, p, last)
		if err != nil {
			slog.Error("task: join readiness check failed", "task", s.key(dep), "parent", p, "err", err)
			return
		}
		if !ready {
			return // a parent has no fresh success yet — barrier not met
		}
	}
	reason := joinReason(parents)
	id, ok := s.claimLocked(dep, reason)
	if ok {
		go s.d.runTask(s, dep, id)
	}
}

// joinReason renders a join's trigger reason deterministically, e.g. "after:a,b".
func joinReason(parents []string) string {
	ps := append([]string(nil), parents...)
	sort.Strings(ps)
	return "after:" + strings.Join(ps, ",")
}

func (s *taskScheduler) key(name string) string { return s.address + "/" + name }

// runTask resolves the (already-claimed) run's environment and hands the command
// to nexus-pm, then awaits the outcome by polling. The task_runs row (id) was
// recorded by claimLocked; any early failure finalises it as failed.
func (d *Daemon) runTask(s *taskScheduler, name string, id int64) {
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
		_ = d.DB.FinishTaskRun(id, "failed", -1, time.Now())
		slog.Error("task: environment could not be resolved", "task", s.key(name), "err", err)
		return
	}
	env = append(env, "NEXUS_TASK="+name)

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
	slog.Info("task: run started", "task", s.key(name), "id", id)
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
		// A single-parent after: fires as soon as its one parent succeeds; a
		// fan-in join (several parents) fires only once the barrier is met.
		if parents := s.tasks[dep].After; len(parents) > 1 {
			s.fireJoin(s.ctx, dep, parents)
		} else {
			s.launch(s.ctx, dep, "after:"+task)
		}
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
	s.launch(s.ctx, task, "manual")
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
