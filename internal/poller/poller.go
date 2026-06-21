package poller

import (
	"context"
	"log/slog"
	"time"

	"github.com/rdkal/nexus/internal/git"
)

// Poller watches a single git ref and pushes new SHAs into a Queue.
type Poller struct {
	RepoDir  string
	Ref      string
	Interval time.Duration
	// Resolve resolves RepoDir+Ref to a commit SHA. Defaults to git.ResolveRef.
	Resolve func(repoDir, ref string) (string, error)
}

// Poll performs a single resolve-and-push cycle.
// Returns the current SHA (updated when a new one is detected, unchanged otherwise).
func (p *Poller) Poll(q *Queue, lastSHA string) string {
	resolve := p.Resolve
	if resolve == nil {
		resolve = git.ResolveRef
	}
	sha, err := resolve(p.RepoDir, p.Ref)
	if err != nil {
		slog.Error("poller resolve", "repo", p.RepoDir, "ref", p.Ref, "err", err)
		return lastSHA
	}
	if sha != lastSHA {
		q.Push(sha)
		return sha
	}
	return lastSHA
}

// Run polls until ctx is cancelled, starting immediately and then every Interval.
// lastSHA is the last-known SHA (e.g. from the database) to avoid re-triggering on restart.
func (p *Poller) Run(ctx context.Context, q *Queue, lastSHA string) {
	interval := p.Interval
	if interval == 0 {
		interval = 30 * time.Second
	}

	lastSHA = p.Poll(q, lastSHA)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			lastSHA = p.Poll(q, lastSHA)
		}
	}
}
