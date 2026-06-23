package supervisor_test

import (
	"os"
	"sync"
	"testing"
	"time"

	"github.com/rdkal/nexus/internal/supervisor"
)

// --- mock process helpers ---

type testProcess struct {
	id     string
	exitCh chan struct{}
	code   int
	once   sync.Once
}

// blockProc blocks in Wait until signaled.
func blockProc() *testProcess {
	return &testProcess{id: "block", exitCh: make(chan struct{})}
}

// crashProc returns immediately with exit code 1.
func crashProc() *testProcess {
	p := &testProcess{id: "crash", exitCh: make(chan struct{}), code: 1}
	p.once.Do(func() { close(p.exitCh) }) // use Once so Signal() is a no-op
	return p
}

func (p *testProcess) Wait() (int, error) { <-p.exitCh; return p.code, nil }
func (p *testProcess) Signal(os.Signal) error {
	p.once.Do(func() { close(p.exitCh) })
	return nil
}
func (p *testProcess) ID() string { return p.id }

// --- tests ---

func TestNextBackoffGrowsAndCaps(t *testing.T) {
	sup := &supervisor.Supervisor{
		Runner:      supervisor.RunnerFunc(func(_ supervisor.ServiceSpec) (supervisor.Process, error) { return crashProc(), nil }),
		InitBackoff: time.Second,
		MaxBackoff:  60 * time.Second,
	}
	_ = sup // just referencing to confirm package compiles

	cases := []struct {
		in, max, want time.Duration
	}{
		{1 * time.Second, 60 * time.Second, 2 * time.Second},
		{32 * time.Second, 60 * time.Second, 60 * time.Second},
		{60 * time.Second, 60 * time.Second, 60 * time.Second},
	}
	for _, c := range cases {
		got := supervisor.NextBackoff(c.in, c.max)
		if got != c.want {
			t.Errorf("NextBackoff(%v, %v) = %v, want %v", c.in, c.max, got, c.want)
		}
	}
}

func TestSpawn_ServiceStartsImmediately(t *testing.T) {
	started := make(chan struct{}, 1)
	sup := &supervisor.Supervisor{
		Runner: supervisor.RunnerFunc(func(_ supervisor.ServiceSpec) (supervisor.Process, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			return blockProc(), nil
		}),
	}

	sup.Spawn("svc", supervisor.ServiceSpec{})
	defer sup.StopAll()

	select {
	case <-started:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("service did not start within 500ms")
	}
}

func TestSpawn_NoopWhenAlreadyRunning(t *testing.T) {
	var mu sync.Mutex
	starts := 0
	sup := &supervisor.Supervisor{
		Runner: supervisor.RunnerFunc(func(_ supervisor.ServiceSpec) (supervisor.Process, error) {
			mu.Lock()
			starts++
			mu.Unlock()
			return blockProc(), nil
		}),
	}

	sup.Spawn("svc", supervisor.ServiceSpec{})
	sup.Spawn("svc", supervisor.ServiceSpec{}) // second call is a no-op
	defer sup.StopAll()

	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	n := starts
	mu.Unlock()
	if n != 1 {
		t.Errorf("expected 1 start, got %d", n)
	}
}

func TestStop_SIGTERMsRunningProcess(t *testing.T) {
	var proc *testProcess
	sup := &supervisor.Supervisor{
		Runner: supervisor.RunnerFunc(func(_ supervisor.ServiceSpec) (supervisor.Process, error) {
			proc = blockProc()
			return proc, nil
		}),
		GracePeriod: 50 * time.Millisecond,
	}

	sup.Spawn("svc", supervisor.ServiceSpec{})

	// Wait until running
	deadline := time.After(500 * time.Millisecond)
	for {
		if st, ok := sup.Status("svc"); ok && st.Running {
			break
		}
		select {
		case <-deadline:
			t.Fatal("service never became running")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	sup.Stop("svc") // blocks until goroutine exits

	// proc.exitCh should be closed because Signal was called
	select {
	case <-proc.exitCh:
	default:
		t.Error("process was not signaled during Stop")
	}

	if _, ok := sup.Status("svc"); ok {
		t.Error("service should be absent after Stop")
	}
}

func TestRestart_OnUnexpectedExit(t *testing.T) {
	sup := &supervisor.Supervisor{
		Runner:      supervisor.RunnerFunc(func(_ supervisor.ServiceSpec) (supervisor.Process, error) { return crashProc(), nil }),
		InitBackoff: time.Millisecond,
		MaxBackoff:  time.Millisecond,
	}

	sup.Spawn("svc", supervisor.ServiceSpec{})

	deadline := time.After(500 * time.Millisecond)
	for {
		if st, ok := sup.Status("svc"); ok && st.Restarts >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("service did not restart at least twice within 500ms")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	sup.Stop("svc")
}

func TestDegraded_AfterThreshold(t *testing.T) {
	sup := &supervisor.Supervisor{
		Runner:      supervisor.RunnerFunc(func(_ supervisor.ServiceSpec) (supervisor.Process, error) { return crashProc(), nil }),
		InitBackoff: time.Millisecond,
		MaxBackoff:  time.Millisecond,
	}

	sup.Spawn("svc", supervisor.ServiceSpec{})

	deadline := time.After(500 * time.Millisecond)
	for {
		if st, ok := sup.Status("svc"); ok && st.Degraded {
			break
		}
		select {
		case <-deadline:
			t.Fatal("service did not become degraded within 500ms")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	st, _ := sup.Status("svc")
	if !st.Degraded {
		t.Error("expected Degraded=true")
	}
	if st.Running {
		t.Error("degraded service should not be running")
	}

	// After degraded the loop exits, so re-spawn should be allowed.
	started := make(chan struct{}, 1)
	sup2 := &supervisor.Supervisor{
		Runner: supervisor.RunnerFunc(func(_ supervisor.ServiceSpec) (supervisor.Process, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			return blockProc(), nil
		}),
	}
	// Borrow the same name on a fresh supervisor to confirm interface allows re-spawn.
	sup2.Spawn("svc", supervisor.ServiceSpec{})
	defer sup2.StopAll()
	select {
	case <-started:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("re-spawn did not start")
	}
}

func TestBackoff_GrowsBetweenRestarts(t *testing.T) {
	const initBackoff = 10 * time.Millisecond

	var mu sync.Mutex
	var startTimes []time.Time

	sup := &supervisor.Supervisor{
		Runner: supervisor.RunnerFunc(func(_ supervisor.ServiceSpec) (supervisor.Process, error) {
			mu.Lock()
			startTimes = append(startTimes, time.Now())
			mu.Unlock()
			return crashProc(), nil
		}),
		InitBackoff: initBackoff,
		MaxBackoff:  200 * time.Millisecond,
	}

	sup.Spawn("svc", supervisor.ServiceSpec{})

	// Wait for 4 starts: initial + 3 restarts with growing backoff.
	deadline := time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(startTimes)
		mu.Unlock()
		if n >= 4 {
			break
		}
		select {
		case <-deadline:
			mu.Lock()
			t.Fatalf("only %d starts after 2s", len(startTimes))
			mu.Unlock()
		default:
			time.Sleep(time.Millisecond)
		}
	}
	sup.Stop("svc")

	mu.Lock()
	defer mu.Unlock()

	gap0 := startTimes[1].Sub(startTimes[0])
	gap1 := startTimes[2].Sub(startTimes[1])
	gap2 := startTimes[3].Sub(startTimes[2])

	// Each gap should be roughly 2x the previous.
	// Allow 50% tolerance for scheduling jitter.
	if gap1 < gap0/2 {
		t.Errorf("backoff not growing: gap0=%v gap1=%v", gap0, gap1)
	}
	if gap2 < gap1/2 {
		t.Errorf("backoff not growing: gap1=%v gap2=%v", gap1, gap2)
	}
}

func TestStopAll_StopsAllServices(t *testing.T) {
	names := []string{"a", "b", "c"}
	sup := &supervisor.Supervisor{
		Runner: supervisor.RunnerFunc(func(_ supervisor.ServiceSpec) (supervisor.Process, error) {
			return blockProc(), nil
		}),
		GracePeriod: 50 * time.Millisecond,
	}

	for _, name := range names {
		sup.Spawn(name, supervisor.ServiceSpec{})
	}

	// Wait for all to be running
	deadline := time.After(500 * time.Millisecond)
	for {
		allRunning := true
		for _, name := range names {
			st, ok := sup.Status(name)
			if !ok || !st.Running {
				allRunning = false
				break
			}
		}
		if allRunning {
			break
		}
		select {
		case <-deadline:
			t.Fatal("not all services started within 500ms")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	sup.StopAll()

	for _, name := range names {
		if _, ok := sup.Status(name); ok {
			t.Errorf("service %q still present after StopAll", name)
		}
	}
}
