package poller

import "sync"

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
