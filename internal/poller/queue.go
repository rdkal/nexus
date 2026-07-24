package poller

import (
	"context"
	"sync"
	"time"
)

// Queue is a thread-safe latest-wins pending-SHA store for one deployment.
// At most one SHA is held; pushing while a SHA is pending replaces it.
type Queue struct {
	mu      sync.Mutex
	pending string
}

// Push sets the pending SHA, replacing any previously queued value.
func (q *Queue) Push(sha string) {
	q.mu.Lock()
	q.pending = sha
	q.mu.Unlock()
}

// Pop returns and clears the pending SHA.
// Returns ("", false) if nothing is pending.
func (q *Queue) Pop() (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending == "" {
		return "", false
	}
	s := q.pending
	q.pending = ""
	return s, true
}

// WaitPop blocks until a SHA is available or ctx is cancelled.
// Returns ("", false) when the context is cancelled.
func (q *Queue) WaitPop(ctx context.Context) (string, bool) {
	for {
		if sha, ok := q.Pop(); ok {
			return sha, true
		}
		select {
		case <-ctx.Done():
			return "", false
		case <-time.After(200 * time.Millisecond):
		}
	}
}

// WaitPopTimeout is WaitPop bounded by d: it returns ("", false) when d elapses
// (or ctx is cancelled) with nothing pending. Callers distinguish the two via
// ctx.Err(). Used to wait for a superseding SHA while a failed deploy backs off.
func (q *Queue) WaitPopTimeout(ctx context.Context, d time.Duration) (string, bool) {
	deadline := time.NewTimer(d)
	defer deadline.Stop()
	for {
		if sha, ok := q.Pop(); ok {
			return sha, true
		}
		select {
		case <-ctx.Done():
			return "", false
		case <-deadline.C:
			return "", false
		case <-time.After(200 * time.Millisecond):
		}
	}
}
