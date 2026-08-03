package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rdkal/nexus/internal/penv"
	"github.com/rdkal/nexus/internal/supervisor"
)

// managedRunKey is the nexus-pm run id for a managed run. Prefixed so it never
// collides with a task run's key (a bare row id), since both share nexus-pm's
// one-shot run keyspace.
func managedRunKey(id int64) string { return "run-" + strconv.FormatInt(id, 10) }

// validRunName reports whether a managed-run name is usable: a single path
// segment of reasonable characters, so it is unambiguous in a URL and a log path.
func validRunName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		ok := r == '-' || r == '_' || r == '.' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
		if !ok {
			return false
		}
	}
	// A leading dot would let ".." escape the log directory.
	return !strings.HasPrefix(name, ".")
}

// runProject is a snapshot of the project a managed run is attached to, captured
// under lock, holding just what penv.Build needs.
type runProject struct {
	address    string
	ref        string
	sha        string
	appDir     string
	ownVolumes map[string]struct{}
	projectEnv map[string]string
	parentEnv  map[string]string
}

// resolveRunProject finds the deployed project whose application directory (the
// worktree holding its nexus.yaml) contains cwd — the project a run launched from
// there belongs to. The deepest match wins when projects nest. Returns nil when
// cwd is under no known project (an unattached, host-global run).
func (d *Daemon) resolveRunProject(cwd string) *runProject {
	cwd = filepath.Clean(cwd)

	d.mu.RLock()
	states := make([]*projectState, 0, len(d.projects))
	for _, ps := range d.projects {
		states = append(states, ps)
	}
	d.mu.RUnlock()

	var best *runProject
	bestLen := -1
	for _, ps := range states {
		ps.mu.RLock()
		worktree := ps.worktree
		var snap *runProject
		if worktree != "" {
			appDir := filepath.Join(worktree, ps.subdir)
			if rel, err := filepath.Rel(appDir, cwd); err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				snap = &runProject{
					address:    ps.address,
					ref:        ps.ref,
					sha:        ps.sha,
					appDir:     appDir,
					projectEnv: ps.cfg.Environment,
					parentEnv:  ps.parentEnv,
				}
				if ps.cfg != nil {
					snap.ownVolumes = ps.cfg.Volumes
				}
			}
		}
		ps.mu.RUnlock()
		if snap != nil && len(snap.appDir) > bestLen {
			best, bestLen = snap, len(snap.appDir)
		}
	}
	return best
}

// startManagedRun registers and launches a managed run: it resolves the owning
// project by cwd, builds the environment, claims a durable run record (rejected
// if a run of the same name is already active), and hands the command to nexus-pm.
func (d *Daemon) startManagedRun(name, command, cwd string) (int64, string, error) {
	if !validRunName(name) {
		return 0, "", fmt.Errorf("invalid run name %q: use letters, digits, - _ . (no slash)", name)
	}
	if strings.TrimSpace(command) == "" {
		return 0, "", fmt.Errorf("empty command")
	}
	if d.taskExec == nil {
		return 0, "", fmt.Errorf("run executor not available")
	}
	cwd = filepath.Clean(cwd)

	proj := d.resolveRunProject(cwd)
	address := ""
	in := penv.Input{
		Paths:         d.Paths,
		WorkDir:       cwd,
		GlobalVolumes: d.globalVolumeEnv(),
	}
	if proj != nil {
		address = proj.address
		in.Address = proj.address
		in.Ref = proj.ref
		in.SHA = proj.sha
		in.OwnVolumes = proj.ownVolumes
		in.ProjectEnv = proj.projectEnv
		in.ParentEnv = proj.parentEnv
	}
	env, err := penv.Build(in)
	if err != nil {
		return 0, "", fmt.Errorf("resolve environment: %w", err)
	}
	env = append(env, "NEXUS_RUN="+name)

	// Claim a durable record; the DB's partial unique index rejects a second run
	// with the same live name (hard no-overlap).
	id, err := d.DB.AddRun(name, address, command, cwd, time.Now())
	if err != nil {
		return 0, "", err
	}

	logFile := d.Paths.RunLog(address, name)
	spec := supervisor.ServiceSpec{Command: command, WorkDir: cwd, Env: env, LogFile: logFile}
	if err := d.taskExec.StartRun(managedRunKey(id), spec); err != nil {
		_ = d.DB.FinishRun(id, "failed", -1, time.Now())
		return 0, "", fmt.Errorf("start run: %w", err)
	}
	slog.Info("run: started", "name", name, "address", address, "id", id)
	go d.awaitManagedRun(id, name)
	return id, address, nil
}

// stopManagedRun signals a running managed run to terminate. The poll loop in
// awaitManagedRun then observes the process exit and finalises the record (a
// stopped run is recorded as failed, since it did not complete). Returns an error
// if no run by that name is currently running.
func (d *Daemon) stopManagedRun(name string) error {
	run, ok, err := d.DB.GetRun(name)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("run %q not found", name)
	}
	if run.Status != "running" {
		return fmt.Errorf("run %q is not running", name)
	}
	if d.taskExec == nil {
		return fmt.Errorf("run executor not available")
	}
	// Record the intent first (durably), so the finaliser marks it 'cancelled'
	// even if the runtime restarts between the signal and the process exiting.
	if err := d.DB.MarkRunStopRequested(run.ID); err != nil {
		return err
	}
	d.taskExec.StopRun(managedRunKey(run.ID))
	slog.Info("run: stop requested", "name", name, "id", run.ID)
	return nil
}

// runStopRequested reports whether the operator asked to stop this run.
func (d *Daemon) runStopRequested(id int64) bool {
	req, err := d.DB.RunStopRequested(id)
	if err != nil {
		slog.Warn("run: stop-requested lookup failed", "id", id, "err", err)
		return false
	}
	return req
}

// awaitManagedRun polls nexus-pm until the run finishes (or is lost) and finalises
// its record. Runs under the daemon root context so it is not abandoned by a
// redeploy; on shutdown it leaves the row 'running' for recoverManagedRuns.
func (d *Daemon) awaitManagedRun(id int64, name string) {
	ctx := d.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	key := managedRunKey(id)
	for {
		state, known, err := d.taskExec.PollRun(key)
		switch {
		case err != nil:
			// transient (nexus-pm briefly unreachable) — keep waiting.
		case !known:
			// nexus-pm has no record: the run was interrupted (nexus-pm restart or
			// reboot). A managed run is never silently re-run; it's cancelled if a
			// stop was requested, otherwise failed.
			status := "failed"
			if d.runStopRequested(id) {
				status = "cancelled"
			}
			_ = d.DB.FinishRun(id, status, -1, time.Now())
			slog.Warn("run: interrupted (no record in nexus-pm)", "name", name, "id", id, "status", status)
			return
		case state.Done:
			status := "success"
			if state.ExitCode != 0 || state.Err != "" {
				status = "failed"
			}
			// A stop the operator asked for is recorded distinctly, not as a failure.
			if d.runStopRequested(id) {
				status = "cancelled"
			}
			_ = d.DB.FinishRun(id, status, state.ExitCode, time.Now())
			d.taskExec.AckRun(key)
			slog.Info("run: finished", "name", name, "id", id, "status", status, "exit", state.ExitCode)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(d.taskPollInterval):
		}
	}
}

// recoverManagedRuns resumes polling for any managed run left 'running' when the
// runtime last stopped, so one that kept running under nexus-pm across a runtime
// restart is finalised rather than left dangling. Called at startup.
func (d *Daemon) recoverManagedRuns() {
	if d.taskExec == nil {
		return
	}
	runs, err := d.DB.RunningRuns()
	if err != nil {
		slog.Error("daemon: list running runs", "err", err)
		return
	}
	for _, r := range runs {
		slog.Info("daemon: recovering in-flight run", "name", r.Name, "id", r.ID)
		go d.awaitManagedRun(r.ID, r.Name)
	}
}
