package supervisor_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rdkal/nexus/internal/supervisor"
)

func waitDone(t *testing.T, s *supervisor.Supervisor, id string) supervisor.RunState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		st, known, _ := s.PollRun(id)
		if !known {
			t.Fatalf("run %s vanished", id)
		}
		if st.Done {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("run %s never finished", id)
	return supervisor.RunState{}
}

func TestTaskRunRegistry(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "task.log")
	s := &supervisor.Supervisor{}

	// Unknown id → not known.
	if _, known, _ := s.PollRun("nope"); known {
		t.Error("unknown run should not be known")
	}

	// Success run, output captured.
	if err := s.StartRun("1", supervisor.ServiceSpec{Command: "echo hi", WorkDir: dir, LogFile: log}); err != nil {
		t.Fatal(err)
	}
	if st := waitDone(t, s, "1"); st.ExitCode != 0 {
		t.Errorf("exit = %d, want 0", st.ExitCode)
	}
	if out, _ := os.ReadFile(log); string(out) != "hi\n" {
		t.Errorf("output = %q", out)
	}

	// Ack removes it.
	s.AckRun("1")
	if _, known, _ := s.PollRun("1"); known {
		t.Error("acked run should be gone")
	}

	// Non-zero exit is a done run with the code, not a lost run.
	if err := s.StartRun("2", supervisor.ServiceSpec{Command: "exit 5", WorkDir: dir, LogFile: log}); err != nil {
		t.Fatal(err)
	}
	if st := waitDone(t, s, "2"); st.ExitCode != 5 {
		t.Errorf("exit = %d, want 5", st.ExitCode)
	}

	// Idempotent: a repeated StartRun for a live id is a no-op.
	_ = s.StartRun("3", supervisor.ServiceSpec{Command: "sleep 5", WorkDir: dir, LogFile: log})
	_ = s.StartRun("3", supervisor.ServiceSpec{Command: "echo other", WorkDir: dir, LogFile: log})
	if st, known, _ := s.PollRun("3"); !known || st.Done {
		t.Errorf("run 3 should still be running: %+v known=%v", st, known)
	}
}
