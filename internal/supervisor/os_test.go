package supervisor_test

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/rdkal/nexus/internal/supervisor"
)

func TestOSRunner_CleanExit(t *testing.T) {
	tmp := t.TempDir()
	spec := supervisor.ServiceSpec{
		Command: "echo hello",
		WorkDir: tmp,
		Env:     os.Environ(),
		LogFile: filepath.Join(tmp, "svc.log"),
	}

	r := &supervisor.OSRunner{}
	proc, err := r.Start(spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	code, err := proc.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	data, err := os.ReadFile(spec.LogFile)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Errorf("log = %q; want to contain 'hello'", data)
	}
}

func TestOSRunner_NonZeroExit(t *testing.T) {
	tmp := t.TempDir()
	spec := supervisor.ServiceSpec{
		Command: "exit 42",
		WorkDir: tmp,
		Env:     os.Environ(),
		LogFile: filepath.Join(tmp, "svc.log"),
	}

	r := &supervisor.OSRunner{}
	proc, err := r.Start(spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	code, err := proc.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if code != 42 {
		t.Errorf("exit code = %d, want 42", code)
	}
}

func TestOSRunner_SIGTERMExits(t *testing.T) {
	tmp := t.TempDir()
	spec := supervisor.ServiceSpec{
		Command: "sleep 60",
		WorkDir: tmp,
		Env:     os.Environ(),
		LogFile: filepath.Join(tmp, "svc.log"),
	}

	r := &supervisor.OSRunner{}
	proc, err := r.Start(spec)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	if id := proc.ID(); id == "" {
		t.Error("expected non-empty process ID")
	}

	if err := proc.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}

	code, _ := proc.Wait()
	// Signal-killed processes return -1 from ExitCode().
	if code == 0 {
		t.Errorf("expected non-zero exit code for SIGTERM'd process, got 0")
	}
}
