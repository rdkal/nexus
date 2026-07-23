package daemon

import (
	"testing"
	"time"
)

func TestRetryBackoff(t *testing.T) {
	cases := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 1 * time.Second}, // guard
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{6, 32 * time.Second},
		{7, 60 * time.Second},  // capped
		{99, 60 * time.Second}, // shift overflow → cap
	}
	for _, c := range cases {
		if got := retryBackoff(c.attempt); got != c.want {
			t.Errorf("retryBackoff(%d) = %v, want %v", c.attempt, got, c.want)
		}
	}
}
