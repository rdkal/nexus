package poller_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rdkal/nexus/internal/poller"
)

func mockResolve(shas ...string) func(string, string) (string, error) {
	i := 0
	return func(_, _ string) (string, error) {
		if i < len(shas) {
			s := shas[i]
			i++
			return s, nil
		}
		return shas[len(shas)-1], nil
	}
}

func TestPoller_Poll_DetectsNewSHA(t *testing.T) {
	var q poller.Queue
	p := &poller.Poller{
		RepoDir: "/fake",
		Ref:     "@main",
		Resolve: mockResolve("sha1", "sha2"),
	}

	last := p.Poll(&q, "")
	if last != "sha1" {
		t.Errorf("last = %q, want sha1", last)
	}
	sha, ok := q.Pop()
	if !ok || sha != "sha1" {
		t.Errorf("Pop() = %q, %v; want sha1, true", sha, ok)
	}

	last = p.Poll(&q, last)
	if last != "sha2" {
		t.Errorf("last = %q, want sha2", last)
	}
	sha, ok = q.Pop()
	if !ok || sha != "sha2" {
		t.Errorf("Pop() = %q, %v; want sha2, true", sha, ok)
	}
}

func TestPoller_Poll_SkipsUnchangedSHA(t *testing.T) {
	var q poller.Queue
	p := &poller.Poller{
		RepoDir: "/fake",
		Ref:     "@main",
		Resolve: mockResolve("sha1"),
	}

	p.Poll(&q, "sha1") // same as current
	if _, ok := q.Pop(); ok {
		t.Error("should not push unchanged SHA")
	}
}

func TestPoller_Poll_SwallowsResolveError(t *testing.T) {
	var q poller.Queue
	p := &poller.Poller{
		RepoDir: "/fake",
		Ref:     "@main",
		Resolve: func(_, _ string) (string, error) { return "", errors.New("network down") },
	}

	last := p.Poll(&q, "sha1")
	if last != "sha1" {
		t.Errorf("last should be unchanged on error, got %q", last)
	}
	if _, ok := q.Pop(); ok {
		t.Error("should not push on resolve error")
	}
}

func TestPoller_Run_ExitsOnCancel(t *testing.T) {
	var q poller.Queue
	p := &poller.Poller{
		RepoDir:  "/fake",
		Ref:      "@main",
		Interval: 5 * time.Millisecond,
		Resolve:  mockResolve("sha1"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx, &q, "")
		close(done)
	}()

	// Immediate first poll should push sha1.
	deadline := time.After(500 * time.Millisecond)
	for {
		if _, ok := q.Pop(); ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timeout: first poll did not push a SHA")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("poller goroutine did not exit after context cancel")
	}
}
