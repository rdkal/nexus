package daemon

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rdkal/nexus/internal/config"
	"github.com/rdkal/nexus/internal/db"
	"github.com/rdkal/nexus/internal/home"
	"github.com/rdkal/nexus/internal/poller"
)

func taskName(env []string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, "NEXUS_TASK="); ok {
			return v
		}
	}
	return ""
}

func newTaskTestDaemon(t *testing.T) (*Daemon, string) {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "nexus.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })
	return New(database, plainSup{}, home.NewPaths(dir)), dir
}

func injectTaskProject(d *Daemon, dir string, cfg *config.ProjectFile) *projectState {
	ps := &projectState{address: "app", queue: &poller.Queue{}, cfg: cfg, sha: "sha1", worktree: dir}
	d.mu.Lock()
	d.projects["app"] = ps
	d.mu.Unlock()
	return ps
}

func TestTaskAfterCascade_StopsOnFailure(t *testing.T) {
	d, dir := newTaskTestDaemon(t)

	var mu sync.Mutex
	var order []string
	done := make(chan string, 16)
	d.runTask = func(ctx context.Context, cmd, wd string, env []string, log string) (int, error) {
		name := taskName(env)
		mu.Lock()
		order = append(order, name)
		mu.Unlock()
		exit := 0
		if cmd == "fail" {
			exit = 1
		}
		done <- name
		return exit, nil
	}

	cfg := &config.ProjectFile{Tasks: map[string]config.Task{
		"a":     {Run: "ok"},
		"b":     {Run: "ok", After: "a"},
		"c":     {Run: "ok", After: "b"},
		"boom":  {Run: "fail"},
		"never": {Run: "ok", After: "boom"},
	}}
	ps := injectTaskProject(d, dir, cfg)
	d.startTasks(context.Background(), ps)

	// Firing a cascades a → b → c.
	if err := d.triggerTask("app", "a"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatalf("timeout waiting for cascade; got %v", order)
		}
	}
	mu.Lock()
	got := strings.Join(order, ",")
	mu.Unlock()
	if got != "a,b,c" {
		t.Errorf("cascade order = %q, want a,b,c", got)
	}

	// Firing boom (which fails) must NOT run its dependent.
	if err := d.triggerTask("app", "boom"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done: // boom itself
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for boom")
	}
	time.Sleep(200 * time.Millisecond) // give a wrong cascade time to (not) happen
	mu.Lock()
	defer mu.Unlock()
	for _, n := range order {
		if n == "never" {
			t.Errorf("dependent of a failed task ran: %v", order)
		}
	}
}

func TestTaskNoSelfOverlap(t *testing.T) {
	d, dir := newTaskTestDaemon(t)

	release := make(chan struct{})
	started := make(chan struct{}, 4)
	var runs int
	var mu sync.Mutex
	d.runTask = func(ctx context.Context, cmd, wd string, env []string, log string) (int, error) {
		mu.Lock()
		runs++
		mu.Unlock()
		started <- struct{}{}
		<-release // block until the test releases
		return 0, nil
	}

	cfg := &config.ProjectFile{Tasks: map[string]config.Task{"slow": {Run: "ok"}}}
	ps := injectTaskProject(d, dir, cfg)
	d.startTasks(context.Background(), ps)

	_ = d.triggerTask("app", "slow") // first run — blocks in runTask
	<-started
	_ = d.triggerTask("app", "slow") // second — should be skipped (already running)
	time.Sleep(200 * time.Millisecond)

	close(release)
	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if runs != 1 {
		t.Errorf("overlapping fire ran %d times, want 1", runs)
	}
}
