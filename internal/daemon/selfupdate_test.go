package daemon

import (
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/rdkal/nexus/internal/home"
	"github.com/rdkal/nexus/internal/supervisor"
)

// restartRecordingSup implements SupervisorAPI plus RestartRuntime, recording calls.
type restartRecordingSup struct {
	mu       sync.Mutex
	restarts int
	err      error
}

func (s *restartRecordingSup) Spawn(string, supervisor.ServiceSpec) {}
func (s *restartRecordingSup) Stop(string)                          {}
func (s *restartRecordingSup) Status(string) (supervisor.Status, bool) {
	return supervisor.Status{}, false
}
func (s *restartRecordingSup) RestartRuntime() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restarts++
	return s.err
}
func (s *restartRecordingSup) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.restarts
}

// plainSup implements only SupervisorAPI — no RestartRuntime capability.
type plainSup struct{}

func (plainSup) Spawn(string, supervisor.ServiceSpec)    {}
func (plainSup) Stop(string)                             {}
func (plainSup) Status(string) (supervisor.Status, bool) { return supervisor.Status{}, false }

func TestNew_SelfSpecPath(t *testing.T) {
	orig, had := os.LookupEnv("NEXUS_SELF_SPEC")
	t.Cleanup(func() {
		if had {
			os.Setenv("NEXUS_SELF_SPEC", orig)
		} else {
			os.Unsetenv("NEXUS_SELF_SPEC")
		}
	})

	// Unset → default module path.
	os.Unsetenv("NEXUS_SELF_SPEC")
	if d := New(nil, plainSup{}, home.Paths{}); d.SelfSpecPath != defaultSelfSpecPath {
		t.Errorf("default SelfSpecPath = %q, want %q", d.SelfSpecPath, defaultSelfSpecPath)
	}

	// Set → override (e.g. a fork).
	os.Setenv("NEXUS_SELF_SPEC", "github.com/fork/nexus")
	if d := New(nil, plainSup{}, home.Paths{}); d.SelfSpecPath != "github.com/fork/nexus" {
		t.Errorf("override SelfSpecPath = %q, want %q", d.SelfSpecPath, "github.com/fork/nexus")
	}

	// Explicitly empty → self-update restarts disabled.
	os.Setenv("NEXUS_SELF_SPEC", "")
	if d := New(nil, plainSup{}, home.Paths{}); d.SelfSpecPath != "" {
		t.Errorf("empty override SelfSpecPath = %q, want empty", d.SelfSpecPath)
	}
}

func TestIsSelf(t *testing.T) {
	d := &Daemon{SelfSpecPath: "github.com/rdkal/nexus"}
	if !d.isSelf("github.com/rdkal/nexus", "") {
		t.Error("expected match for the configured self spec path")
	}
	if d.isSelf("github.com/other/repo", "") {
		t.Error("unexpected match for a different spec path")
	}
	// A subdirectory project of the nexus repo (e.g. the web UI at .../nexus/web)
	// shares the repo-root spec path but is NOT nexus itself.
	if d.isSelf("github.com/rdkal/nexus", "web") {
		t.Error("a subdir project of the nexus repo must not be treated as self")
	}

	// An empty SelfSpecPath disables self detection entirely.
	d.SelfSpecPath = ""
	if d.isSelf("", "") {
		t.Error("empty SelfSpecPath must not match the empty string")
	}
	if d.isSelf("github.com/rdkal/nexus", "") {
		t.Error("empty SelfSpecPath must not match any path")
	}
}

func TestRestartRuntime_CapableSupervisor(t *testing.T) {
	sup := &restartRecordingSup{}
	d := &Daemon{Sup: sup}

	d.restartRuntime("nexus")

	if got := sup.count(); got != 1 {
		t.Errorf("expected RestartRuntime to be called once, got %d", got)
	}
}

func TestRestartRuntime_ReportsError(t *testing.T) {
	// A restart error must be handled (logged) without panicking.
	sup := &restartRecordingSup{err: errors.New("boom")}
	d := &Daemon{Sup: sup}

	d.restartRuntime("nexus")

	if got := sup.count(); got != 1 {
		t.Errorf("expected RestartRuntime to be attempted once, got %d", got)
	}
}

func TestRestartRuntime_IncapableSupervisor(t *testing.T) {
	// A supervisor without RestartRuntime (in-process, non-split) must not panic.
	d := &Daemon{Sup: plainSup{}}
	d.restartRuntime("nexus") // reaching here without panic is the assertion
}
