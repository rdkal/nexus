package supervisor

import (
	"sync"
	"time"
)

// RunState is a snapshot of a one-shot task run.
type RunState struct {
	Done     bool
	ExitCode int
	Err      string // system-level failure (start/wait); "" on a clean exit
}

// taskRunTTL is how long a finished-but-uncollected run is retained before it is
// reaped, so a runtime that never comes back to Ack doesn't leak memory.
const taskRunTTL = 24 * time.Hour

type taskRun struct {
	proc Process

	mu       sync.Mutex
	done     bool
	exit     int
	err      string
	finished time.Time
}

// StartRun launches a one-shot command tracked by run id, without supervision or
// restart, and returns immediately. Idempotent: a repeated StartRun for a live id
// is a no-op. A failure to start is recorded as a done, failed run (observable via
// PollRun) rather than returned as an error.
func (s *Supervisor) StartRun(id string, spec ServiceSpec) error {
	s.runsMu.Lock()
	if s.runs == nil {
		s.runs = make(map[string]*taskRun)
	}
	s.reapLocked()
	if _, exists := s.runs[id]; exists {
		s.runsMu.Unlock()
		return nil
	}
	runner := s.Runner
	if runner == nil {
		runner = &OSRunner{}
	}
	proc, err := runner.Start(spec)
	if err != nil {
		s.runs[id] = &taskRun{done: true, exit: -1, err: err.Error(), finished: time.Now()}
		s.runsMu.Unlock()
		return nil
	}
	tr := &taskRun{proc: proc}
	s.runs[id] = tr
	s.runsMu.Unlock()

	go func() {
		code, werr := proc.Wait()
		tr.mu.Lock()
		tr.done = true
		tr.exit = code
		if werr != nil {
			tr.err = werr.Error()
		}
		tr.finished = time.Now()
		tr.mu.Unlock()
	}()
	return nil
}

// PollRun reports a run's current state. known is false if the id is unknown
// (never started, or reaped) — the caller treats that as a lost run. The error
// return is always nil for the in-process supervisor (it exists to match the
// RemoteSupervisor, whose PollRun can hit a transport error).
func (s *Supervisor) PollRun(id string) (state RunState, known bool, err error) {
	s.runsMu.Lock()
	tr, ok := s.runs[id]
	s.runsMu.Unlock()
	if !ok {
		return RunState{}, false, nil
	}
	tr.mu.Lock()
	defer tr.mu.Unlock()
	return RunState{Done: tr.done, ExitCode: tr.exit, Err: tr.err}, true, nil
}

// AckRun drops a run record once the caller has recorded its outcome.
func (s *Supervisor) AckRun(id string) {
	s.runsMu.Lock()
	delete(s.runs, id)
	s.runsMu.Unlock()
}

// reapLocked drops finished runs older than the TTL. Caller holds runsMu.
func (s *Supervisor) reapLocked() {
	cutoff := time.Now().Add(-taskRunTTL)
	for id, tr := range s.runs {
		tr.mu.Lock()
		stale := tr.done && tr.finished.Before(cutoff)
		tr.mu.Unlock()
		if stale {
			delete(s.runs, id)
		}
	}
}
