package supervisor

import (
	"log/slog"
	"os"
	"sync"
	"syscall"
	"time"
)

const (
	defaultGracePeriod = 30 * time.Second
	defaultInitBackoff = 1 * time.Second
	defaultMaxBackoff  = 60 * time.Second

	// >5 crashes within degradedWindow marks the service degraded.
	degradedThreshold = 5
	degradedWindow    = 60 * time.Second
)

// Supervisor manages a set of named services: spawning them, restarting on crash,
// and stopping them gracefully. It is Runner-agnostic — swap OSRunner for a
// PodmanRunner to run services in containers instead of OS processes.
type Supervisor struct {
	Runner      Runner        // if nil, OSRunner is used
	GracePeriod time.Duration // SIGTERM-to-SIGKILL window (default 30s)
	InitBackoff time.Duration // first restart delay (default 1s)
	MaxBackoff  time.Duration // backoff cap (default 60s)

	mu   sync.Mutex
	svcs map[string]*svcEntry
}

// Status is a snapshot of a service's current state.
type Status struct {
	Running  bool
	Degraded bool
	Restarts int    // times restarted after unexpected exit
	PID      string // empty when not running
}

type svcEntry struct {
	proc     Process       // nil when not running
	stopCh   chan struct{}  // closed by Stop/StopAll
	doneCh   chan struct{}  // closed when the loop goroutine exits
	degraded bool
	restarts int
}

// Spawn starts the named service and enters its restart loop in the background.
// If the service is already running the call is a no-op.
// If the service previously became degraded or was stopped, it is re-spawned.
func (s *Supervisor) Spawn(name string, spec ServiceSpec) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.svcs == nil {
		s.svcs = make(map[string]*svcEntry)
	}
	if e, exists := s.svcs[name]; exists {
		select {
		case <-e.doneCh:
			// Goroutine already exited (degraded or stopped) — allow re-spawn.
			delete(s.svcs, name)
		default:
			return // still running
		}
	}
	e := &svcEntry{
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
	s.svcs[name] = e
	go s.loop(name, spec, e)
}

// Stop gracefully stops the named service.
// Sends SIGTERM, waits up to GracePeriod, then SIGKILL.
// Blocks until the service goroutine exits. No-op if not running.
func (s *Supervisor) Stop(name string) {
	s.mu.Lock()
	e, ok := s.svcs[name]
	if !ok {
		s.mu.Unlock()
		return
	}
	delete(s.svcs, name)
	s.mu.Unlock()

	close(e.stopCh)
	<-e.doneCh
}

// StopAll stops all running services concurrently and waits for all to exit.
func (s *Supervisor) StopAll() {
	s.mu.Lock()
	entries := make([]*svcEntry, 0, len(s.svcs))
	for _, e := range s.svcs {
		entries = append(entries, e)
	}
	s.svcs = make(map[string]*svcEntry)
	s.mu.Unlock()

	for _, e := range entries {
		close(e.stopCh)
	}
	for _, e := range entries {
		<-e.doneCh
	}
}

// Status returns a snapshot of the named service's state.
func (s *Supervisor) Status(name string) (Status, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.svcs[name]
	if !ok {
		return Status{}, false
	}
	st := Status{
		Running:  e.proc != nil,
		Degraded: e.degraded,
		Restarts: e.restarts,
	}
	if e.proc != nil {
		st.PID = e.proc.ID()
	}
	return st, true
}

func (s *Supervisor) loop(name string, spec ServiceSpec, e *svcEntry) {
	defer close(e.doneCh)

	runner := s.Runner
	if runner == nil {
		runner = &OSRunner{}
	}

	backoff := s.initBackoff()
	var crashes []time.Time

	for {
		proc, err := runner.Start(spec)
		if err != nil {
			slog.Error("supervisor: start failed", "service", name, "err", err)
			// Fall through to crash handling.
		} else {
			s.mu.Lock()
			e.proc = proc
			s.mu.Unlock()

			done := make(chan int, 1)
			go func() {
				code, _ := proc.Wait()
				done <- code
			}()

			select {
			case <-e.stopCh:
				s.terminate(name, proc, done)
				return
			case code := <-done:
				s.mu.Lock()
				e.proc = nil
				s.mu.Unlock()
				// Race: stop may have fired at the same time as exit.
				select {
				case <-e.stopCh:
					return
				default:
				}
				slog.Warn("supervisor: service exited unexpectedly",
					"service", name, "exit_code", code)
			}
		}

		// Respect an in-flight stop before sleeping.
		select {
		case <-e.stopCh:
			return
		default:
		}

		// Degraded detection: sliding 60s window.
		now := time.Now()
		crashes = append(crashes, now)
		cutoff := now.Add(-degradedWindow)
		for len(crashes) > 0 && crashes[0].Before(cutoff) {
			crashes = crashes[1:]
		}
		if len(crashes) > degradedThreshold {
			s.mu.Lock()
			e.degraded = true
			s.mu.Unlock()
			slog.Error("supervisor: service degraded, stopping auto-restart", "service", name)
			return
		}

		s.mu.Lock()
		e.restarts++
		s.mu.Unlock()

		select {
		case <-time.After(backoff):
		case <-e.stopCh:
			return
		}
		backoff = NextBackoff(backoff, s.maxBackoff())
	}
}

func (s *Supervisor) terminate(name string, proc Process, done <-chan int) {
	slog.Info("supervisor: stopping service", "service", name)
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		slog.Warn("supervisor: SIGTERM failed", "service", name, "err", err)
	}
	grace := s.GracePeriod
	if grace == 0 {
		grace = defaultGracePeriod
	}
	select {
	case <-done:
	case <-time.After(grace):
		slog.Warn("supervisor: grace period exceeded, sending SIGKILL", "service", name)
		proc.Signal(os.Signal(syscall.SIGKILL)) //nolint
		<-done
	}
}

func (s *Supervisor) initBackoff() time.Duration {
	if s.InitBackoff > 0 {
		return s.InitBackoff
	}
	return defaultInitBackoff
}

func (s *Supervisor) maxBackoff() time.Duration {
	if s.MaxBackoff > 0 {
		return s.MaxBackoff
	}
	return defaultMaxBackoff
}

// NextBackoff doubles d up to max. Exported for testing.
func NextBackoff(d, max time.Duration) time.Duration {
	d *= 2
	if d > max {
		return max
	}
	return d
}
