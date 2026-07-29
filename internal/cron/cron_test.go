package cron

import (
	"testing"
	"time"
)

func mustParse(t *testing.T, spec string) Schedule {
	t.Helper()
	s, err := Parse(spec)
	if err != nil {
		t.Fatalf("Parse(%q): %v", spec, err)
	}
	return s
}

func TestNext(t *testing.T) {
	base := time.Date(2026, 1, 5, 10, 30, 0, 0, time.UTC) // Mon
	cases := []struct {
		spec string
		want time.Time
	}{
		{"* * * * *", base.Add(time.Minute)},
		{"0 3 * * *", time.Date(2026, 1, 6, 3, 0, 0, 0, time.UTC)},         // next 03:00
		{"30 10 * * *", time.Date(2026, 1, 6, 10, 30, 0, 0, time.UTC)},     // same time tomorrow (strictly after)
		{"*/15 * * * *", time.Date(2026, 1, 5, 10, 45, 0, 0, time.UTC)},    // next quarter hour
		{"0 0 1 * *", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)},         // first of next month
		{"0 0 * * 0", time.Date(2026, 1, 11, 0, 0, 0, 0, time.UTC)},        // next Sunday
		{"@daily", time.Date(2026, 1, 6, 0, 0, 0, 0, time.UTC)},
		{"@hourly", time.Date(2026, 1, 5, 11, 0, 0, 0, time.UTC)},
	}
	for _, c := range cases {
		if got := mustParse(t, c.spec).Next(base); !got.Equal(c.want) {
			t.Errorf("Parse(%q).Next(%v) = %v, want %v", c.spec, base, got, c.want)
		}
	}
}

func TestEvery(t *testing.T) {
	base := time.Date(2026, 1, 5, 10, 30, 0, 0, time.UTC)
	if got := mustParse(t, "@every 15m").Next(base); !got.Equal(base.Add(15 * time.Minute)) {
		t.Errorf("@every 15m = %v", got)
	}
	// Sub-minute intervals are allowed for @every (cron fields stay minute-resolution).
	if got := mustParse(t, "@every 5s").Next(base); !got.Equal(base.Add(5 * time.Second)) {
		t.Errorf("@every 5s = %v", got)
	}
}

func TestParseErrors(t *testing.T) {
	for _, bad := range []string{"", "* * * *", "60 * * * *", "* 24 * * *", "* * * 13 *", "a * * * *", "@every", "@every zzz"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) should error", bad)
		}
	}
}

func TestDomOrDow(t *testing.T) {
	// Both day fields restricted → either matches (Vixie semantics).
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) // Thu Jan 1
	s := mustParse(t, "0 0 13 * 0")                      // 13th OR Sunday
	// Jan 4 2026 is the first Sunday after Jan 1.
	if got := s.Next(base); !got.Equal(time.Date(2026, 1, 4, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("dom-or-dow = %v", got)
	}
}
