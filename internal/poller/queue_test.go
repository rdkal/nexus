package poller_test

import (
	"sync"
	"testing"

	"github.com/rdkal/nexus/internal/poller"
)

func TestQueue_EmptyPop(t *testing.T) {
	var q poller.Queue
	if _, ok := q.Pop(); ok {
		t.Error("expected empty pop on zero-value queue")
	}
}

func TestQueue_PushPop(t *testing.T) {
	var q poller.Queue
	q.Push("sha1")
	sha, ok := q.Pop()
	if !ok || sha != "sha1" {
		t.Errorf("Pop() = %q, %v; want sha1, true", sha, ok)
	}
	if _, ok := q.Pop(); ok {
		t.Error("second pop should be empty")
	}
}

func TestQueue_LatestWins(t *testing.T) {
	var q poller.Queue
	q.Push("sha1")
	q.Push("sha2")
	q.Push("sha3")
	sha, ok := q.Pop()
	if !ok || sha != "sha3" {
		t.Errorf("Pop() = %q, %v; want sha3, true", sha, ok)
	}
}

func TestQueue_ConcurrentPushes(t *testing.T) {
	var q poller.Queue
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			q.Push("sha-x")
		}()
	}
	wg.Wait()
	sha, ok := q.Pop()
	if !ok || sha != "sha-x" {
		t.Errorf("Pop() = %q, %v after concurrent pushes", sha, ok)
	}
}
