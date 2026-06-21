package supervisor

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// OSRunner starts services as native OS processes via sh -c.
// This is the default Runner used by the daemon.
//
// A future PodmanRunner would implement the same Runner interface,
// replacing sh -c with "podman run --detach ..." and Process methods
// with "podman kill / podman wait".
type OSRunner struct{}

// Start launches spec.Command via sh -c in spec.WorkDir,
// appending stdout and stderr to spec.LogFile.
func (r *OSRunner) Start(spec ServiceSpec) (Process, error) {
	if err := os.MkdirAll(filepath.Dir(spec.LogFile), 0o755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	f, err := os.OpenFile(spec.LogFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	cmd := exec.Command("sh", "-c", spec.Command)
	cmd.Dir = spec.WorkDir
	cmd.Env = spec.Env
	cmd.Stdout = f
	cmd.Stderr = f

	if err := cmd.Start(); err != nil {
		f.Close()
		return nil, fmt.Errorf("start: %w", err)
	}
	return &osProcess{cmd: cmd, log: f}, nil
}

type osProcess struct {
	cmd *exec.Cmd
	log *os.File
}

func (p *osProcess) Wait() (int, error) {
	err := p.cmd.Wait()
	p.log.Close()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), nil // -1 for signal kills
		}
		return -1, err
	}
	return 0, nil
}

func (p *osProcess) Signal(sig os.Signal) error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Signal(sig)
}

func (p *osProcess) ID() string {
	if p.cmd.Process == nil {
		return ""
	}
	return fmt.Sprintf("%d", p.cmd.Process.Pid)
}
