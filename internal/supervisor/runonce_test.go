package supervisor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rdkal/nexus/internal/supervisor"
)

func TestRunOnce(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "task.log")
	s := &supervisor.Supervisor{}

	// Success: exit 0, output captured.
	code, err := s.RunOnce(supervisor.ServiceSpec{
		Command: "echo hello",
		WorkDir: dir,
		LogFile: log,
	})
	if err != nil || code != 0 {
		t.Fatalf("RunOnce(echo) = %d, %v", code, err)
	}
	out, _ := os.ReadFile(log)
	if string(out) != "hello\n" {
		t.Errorf("captured output = %q", out)
	}

	// Non-zero exit is returned, not an error.
	code, err = s.RunOnce(supervisor.ServiceSpec{Command: "exit 7", WorkDir: dir, LogFile: log})
	if err != nil {
		t.Fatalf("RunOnce(exit 7) err = %v", err)
	}
	if code != 7 {
		t.Errorf("exit code = %d, want 7", code)
	}

	// RunOnce is not tracked in Status.
	if _, ok := s.Status("anything"); ok {
		t.Error("RunOnce should not register a tracked service")
	}
}
