package supervisor

import "os"

// Runner creates service instances. Swap implementations to change the runtime:
// OSRunner uses native OS processes; a Podman runner would use container images.
type Runner interface {
	// Start launches the service described by spec and returns a handle to it.
	// Start must return as soon as the service is launched — it must not block
	// until the service exits.
	Start(spec ServiceSpec) (Process, error)
}

// Process is a handle to one running service instance.
type Process interface {
	// Wait blocks until the process exits and returns the exit code.
	// Returns (exitCode, nil) for both zero and non-zero exits;
	// err indicates a system-level failure (not a non-zero exit).
	// For processes killed by a signal, exitCode is -1.
	Wait() (int, error)

	// Signal sends a signal to the process. No-op if the process has already exited.
	Signal(os.Signal) error

	// ID returns a human-readable process identifier (PID for OS, container ID for Podman).
	ID() string
}

// ServiceSpec describes how to launch one service.
type ServiceSpec struct {
	Command string   // shell command; launched via sh -c
	WorkDir string   // working directory (directory containing nexus.yaml)
	Env     []string // environment variables in KEY=VALUE format
	LogFile string   // path for capturing stdout and stderr
}

// RunnerFunc is a function that implements Runner, useful for testing and one-off runners.
type RunnerFunc func(ServiceSpec) (Process, error)

func (f RunnerFunc) Start(spec ServiceSpec) (Process, error) { return f(spec) }
