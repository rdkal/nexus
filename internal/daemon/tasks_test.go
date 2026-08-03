package daemon

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/rdkal/nexus/internal/config"
	"github.com/rdkal/nexus/internal/db"
	"github.com/rdkal/nexus/internal/home"
	"github.com/rdkal/nexus/internal/poller"
	"github.com/rdkal/nexus/internal/supervisor"
)

// fakeExec is an in-test taskExecutor: StartRun records the run and blocks its
// completion until the test calls complete(id, exit).
type fakeExec struct {
	mu      sync.Mutex
	done    map[string]supervisor.RunState
	live    map[string]bool
	started chan string
}

func newFakeExec() *fakeExec {
	return &fakeExec{done: map[string]supervisor.RunState{}, live: map[string]bool{}, started: make(chan string, 32)}
}

func (f *fakeExec) StartRun(id string, _ supervisor.ServiceSpec) error {
	f.mu.Lock()
	f.live[id] = true
	f.mu.Unlock()
	f.started <- id
	return nil
}

func (f *fakeExec) PollRun(id string) (supervisor.RunState, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.live[id] {
		return supervisor.RunState{}, false, nil
	}
	if st, ok := f.done[id]; ok {
		return st, true, nil
	}
	return supervisor.RunState{Done: false}, true, nil
}

func (f *fakeExec) AckRun(id string) {
	f.mu.Lock()
	delete(f.live, id)
	delete(f.done, id)
	f.mu.Unlock()
}

func (f *fakeExec) complete(id string, exit int) {
	f.mu.Lock()
	f.done[id] = supervisor.RunState{Done: true, ExitCode: exit}
	f.mu.Unlock()
}

func newTaskTestDaemon(t *testing.T) (*Daemon, *fakeExec, string) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "nexus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	d := New(database, plainSup{}, home.NewPaths(dir))
	d.taskPollInterval = 5 * time.Millisecond // this daemon only; no shared global
	fe := newFakeExec()
	d.taskExec = fe
	d.ctx = context.Background()
	return d, fe, dir
}

func injectTaskProject(d *Daemon, dir string, cfg *config.ProjectFile) *projectState {
	ps := &projectState{address: "app", queue: &poller.Queue{}, cfg: cfg, sha: "sha1", worktree: dir}
	d.mu.Lock()
	d.projects["app"] = ps
	d.mu.Unlock()
	return ps
}

// nextStart waits for the next run to start and returns its id.
func nextStart(t *testing.T, fe *fakeExec) string {
	t.Helper()
	select {
	case id := <-fe.started:
		return id
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for a task run to start")
		return ""
	}
}

func tasksByName(t *testing.T, d *Daemon) map[string]db.TaskRun {
	t.Helper()
	runs, err := d.DB.ListTaskRuns("app", 100)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]db.TaskRun{}
	for _, r := range runs {
		out[r.Task] = r
	}
	return out
}

func TestTaskAfterCascade_StopsOnFailure(t *testing.T) {
	d, fe, dir := newTaskTestDaemon(t)
	cfg := &config.ProjectFile{Tasks: map[string]config.Task{
		"a":     {Run: "ok"},
		"b":     {Run: "ok", After: config.AfterList{"a"}},
		"c":     {Run: "ok", After: config.AfterList{"b"}},
		"boom":  {Run: "fail"},
		"never": {Run: "ok", After: config.AfterList{"boom"}},
	}}
	ps := injectTaskProject(d, dir, cfg)
	d.startTasks(context.Background(), ps)

	// a → b → c: three successful runs in sequence.
	if err := d.triggerTask("app", "a"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		fe.complete(nextStart(t, fe), 0)
	}
	// Wait for c to be recorded.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r, ok := tasksByName(t, d)["c"]; ok && r.Status == "success" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := tasksByName(t, d)
	for _, n := range []string{"a", "b", "c"} {
		if got[n].Status != "success" {
			t.Errorf("%s status = %q, want success", n, got[n].Status)
		}
	}
	if got["b"].Reason != "after:a" || got["c"].Reason != "after:b" {
		t.Errorf("cascade reasons wrong: b=%q c=%q", got["b"].Reason, got["c"].Reason)
	}

	// boom fails → its dependent never runs.
	if err := d.triggerTask("app", "boom"); err != nil {
		t.Fatal(err)
	}
	fe.complete(nextStart(t, fe), 1)
	time.Sleep(150 * time.Millisecond)
	if _, ran := tasksByName(t, d)["never"]; ran {
		t.Error("dependent of a failed task ran")
	}
}

func TestTaskFanInJoin(t *testing.T) {
	d, fe, dir := newTaskTestDaemon(t)
	cfg := &config.ProjectFile{Tasks: map[string]config.Task{
		"a":    {Run: "ok"},
		"b":    {Run: "ok"},
		"join": {Run: "ok", After: config.AfterList{"a", "b"}},
	}}
	ps := injectTaskProject(d, dir, cfg)
	d.startTasks(context.Background(), ps)

	// One parent succeeds: the join must NOT fire (the barrier needs both).
	if err := d.triggerTask("app", "a"); err != nil {
		t.Fatal(err)
	}
	fe.complete(nextStart(t, fe), 0) // a succeeds
	select {
	case id := <-fe.started:
		t.Fatalf("join fired with only one parent (id %s)", id)
	case <-time.After(150 * time.Millisecond):
	}

	// The second parent succeeds: now the join fires exactly once.
	if err := d.triggerTask("app", "b"); err != nil {
		t.Fatal(err)
	}
	fe.complete(nextStart(t, fe), 0) // b succeeds
	fe.complete(nextStart(t, fe), 0) // join runs
	select {
	case id := <-fe.started:
		t.Fatalf("join fired twice for one pair of successes (id %s)", id)
	case <-time.After(150 * time.Millisecond):
	}

	got := tasksByName(t, d)
	if got["join"].Status != "success" {
		t.Fatalf("join status = %q, want success", got["join"].Status)
	}
	if got["join"].Reason != "after:a,b" {
		t.Errorf("join reason = %q, want after:a,b", got["join"].Reason)
	}

	// Barrier consumed both successes: a succeeding alone must not refire the join
	// (b has no fresh success past the join's last run).
	if err := d.triggerTask("app", "a"); err != nil {
		t.Fatal(err)
	}
	fe.complete(nextStart(t, fe), 0) // a again
	select {
	case id := <-fe.started:
		t.Fatalf("join refired without a fresh second parent (id %s)", id)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestTaskNoSelfOverlap(t *testing.T) {
	d, fe, dir := newTaskTestDaemon(t)
	cfg := &config.ProjectFile{Tasks: map[string]config.Task{"slow": {Run: "ok"}}}
	ps := injectTaskProject(d, dir, cfg)
	d.startTasks(context.Background(), ps)

	_ = d.triggerTask("app", "slow") // first run — left in progress (not completed)
	id := nextStart(t, fe)

	_ = d.triggerTask("app", "slow") // second — must be skipped (one already running)
	select {
	case <-fe.started:
		t.Fatal("overlapping run started")
	case <-time.After(150 * time.Millisecond):
	}
	fe.complete(id, 0) // let the first finish
}

func TestRecoverTaskRuns(t *testing.T) {
	d, fe, dir := newTaskTestDaemon(t)
	cfg := &config.ProjectFile{Tasks: map[string]config.Task{
		"long": {Run: "ok"},
		"next": {Run: "ok", After: config.AfterList{"long"}},
	}}
	ps := injectTaskProject(d, dir, cfg)
	d.startTasks(context.Background(), ps)

	// Simulate a run that was in-flight when the runtime last stopped: a 'running'
	// task_runs row for `long`, already finished (exit 0) under nexus-pm.
	id, err := d.DB.AddTaskRun("app", "long", "schedule", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	fe.mu.Lock()
	fe.live[runKey(id)] = true
	fe.done[runKey(id)] = supervisor.RunState{Done: true, ExitCode: 0}
	fe.mu.Unlock()

	d.recoverTaskRuns()

	// long is finalised success, and its after: dependent fires and runs.
	nextID := nextStart(t, fe) // `next` starting
	fe.complete(nextID, 0)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		g := tasksByName(t, d)
		if g["long"].Status == "success" && g["next"].Status == "success" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	g := tasksByName(t, d)
	t.Fatalf("recovery incomplete: long=%q next=%q", g["long"].Status, g["next"].Status)
}
