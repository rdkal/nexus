package daemon

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/rdkal/nexus/internal/config"
	"github.com/rdkal/nexus/internal/db"
	"github.com/rdkal/nexus/internal/supervisor"
)

func runsByName(t *testing.T, d *Daemon) map[string]db.Run {
	t.Helper()
	runs, err := d.DB.ListRuns(100)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]db.Run{}
	for _, r := range runs {
		if _, seen := out[r.Name]; !seen { // newest first → keep the latest
			out[r.Name] = r
		}
	}
	return out
}

func waitRunStatus(t *testing.T, d *Daemon, name, status string) db.Run {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok := runsByName(t, d)[name]; ok && r.Status == status {
			return r
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %q never reached status %q", name, status)
	return db.Run{}
}

func TestManagedRun_AttachedByCwdAndFinishes(t *testing.T) {
	d, fe, dir := newTaskTestDaemon(t)
	injectTaskProject(d, dir, &config.ProjectFile{}) // worktree=dir, appDir=dir

	// Launched from inside the project's worktree → attributed to "app".
	id, addr, err := d.startManagedRun("backfill", "echo hi", dir)
	if err != nil {
		t.Fatalf("startManagedRun: %v", err)
	}
	if addr != "app" {
		t.Errorf("address = %q, want app", addr)
	}
	key := nextStart(t, fe)
	if key != managedRunKey(id) {
		t.Errorf("started key = %q, want %q", key, managedRunKey(id))
	}
	fe.complete(key, 0)

	r := waitRunStatus(t, d, "backfill", "success")
	if r.Address != "app" || r.Command != "echo hi" || r.WorkDir != dir {
		t.Errorf("run record wrong: %+v", r)
	}
}

func TestManagedRun_UnattachedWhenOutsideAnyProject(t *testing.T) {
	d, fe, dir := newTaskTestDaemon(t)
	injectTaskProject(d, dir, &config.ProjectFile{})

	// A directory that is not inside any project worktree → unattached run.
	outside := t.TempDir()
	_, addr, err := d.startManagedRun("adhoc", "true", outside)
	if err != nil {
		t.Fatalf("startManagedRun: %v", err)
	}
	if addr != "" {
		t.Errorf("address = %q, want empty (unattached)", addr)
	}
	fe.complete(nextStart(t, fe), 0)
	r := waitRunStatus(t, d, "adhoc", "success")
	if r.Address != "" {
		t.Errorf("unattached run address = %q, want empty", r.Address)
	}
}

func TestManagedRun_FailedExitRecorded(t *testing.T) {
	d, fe, dir := newTaskTestDaemon(t)
	injectTaskProject(d, dir, &config.ProjectFile{})

	_, _, err := d.startManagedRun("boom", "exit 3", dir)
	if err != nil {
		t.Fatal(err)
	}
	fe.complete(nextStart(t, fe), 3)
	r := waitRunStatus(t, d, "boom", "failed")
	if r.ExitCode == nil || *r.ExitCode != 3 {
		t.Errorf("exit = %v, want 3", r.ExitCode)
	}
}

func TestManagedRun_NoOverlapSameName(t *testing.T) {
	d, fe, dir := newTaskTestDaemon(t)
	injectTaskProject(d, dir, &config.ProjectFile{})

	if _, _, err := d.startManagedRun("dup", "sleep", dir); err != nil {
		t.Fatal(err)
	}
	_ = nextStart(t, fe) // first is live (not completed)

	// A second run with the same name is rejected while the first is running.
	_, _, err := d.startManagedRun("dup", "sleep", dir)
	if !errors.Is(err, db.ErrRunActive) {
		t.Fatalf("second start err = %v, want ErrRunActive", err)
	}
}

func TestManagedRun_InvalidName(t *testing.T) {
	d, _, dir := newTaskTestDaemon(t)
	injectTaskProject(d, dir, &config.ProjectFile{})
	for _, bad := range []string{"", "a/b", "../escape", "has space"} {
		if _, _, err := d.startManagedRun(bad, "true", dir); err == nil {
			t.Errorf("name %q accepted, want rejected", bad)
		}
	}
}

func TestManagedRun_DeepestProjectWins(t *testing.T) {
	d, _, dir := newTaskTestDaemon(t)
	// Root project at dir, nested project at dir/sub.
	injectTaskProject(d, dir, &config.ProjectFile{})
	sub := filepath.Join(dir, "sub")
	nested := &projectState{address: "app/child", cfg: &config.ProjectFile{}, sha: "s", worktree: sub}
	d.mu.Lock()
	d.projects["app/child"] = nested
	d.mu.Unlock()

	// A cwd inside the nested worktree resolves to the deeper project.
	proj := d.resolveRunProject(filepath.Join(sub, "deep"))
	if proj == nil || proj.address != "app/child" {
		t.Fatalf("deepest match = %v, want app/child", proj)
	}
}

func TestManagedRun_Stop(t *testing.T) {
	d, fe, dir := newTaskTestDaemon(t)
	injectTaskProject(d, dir, &config.ProjectFile{})

	id, _, err := d.startManagedRun("long", "sleep 100", dir)
	if err != nil {
		t.Fatal(err)
	}
	_ = nextStart(t, fe) // run is live

	// Stopping a running run signals it; the poll loop then records it failed.
	if err := d.stopManagedRun("long"); err != nil {
		t.Fatalf("stopManagedRun: %v", err)
	}
	fe.mu.Lock()
	gotStop := len(fe.stopped) == 1 && fe.stopped[0] == managedRunKey(id)
	fe.mu.Unlock()
	if !gotStop {
		t.Errorf("StopRun not called with the run key")
	}
	// A stop the operator requested is recorded distinctly as cancelled.
	waitRunStatus(t, d, "long", "cancelled")

	// Stopping a run that is not running is an error.
	if err := d.stopManagedRun("long"); err == nil {
		t.Error("stopping a finished run should error")
	}
	if err := d.stopManagedRun("ghost"); err == nil {
		t.Error("stopping an unknown run should error")
	}
}

func TestManagedRun_RecoverInFlight(t *testing.T) {
	d, fe, dir := newTaskTestDaemon(t)
	injectTaskProject(d, dir, &config.ProjectFile{})

	// Seed a run left 'running' when the runtime last stopped, already finished
	// under nexus-pm (exit 0).
	id, err := d.DB.AddRun("long", "app", "sleep 5", dir, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	key := managedRunKey(id)
	fe.mu.Lock()
	fe.live[key] = true
	fe.done[key] = supervisor.RunState{Done: true, ExitCode: 0}
	fe.mu.Unlock()

	d.recoverManagedRuns()
	waitRunStatus(t, d, "long", "success")
}
