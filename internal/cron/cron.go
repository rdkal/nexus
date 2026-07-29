// Package cron parses a task's schedule — a 5-field cron expression, an
// "@every <duration>" interval, or a named shorthand (@daily, @hourly, …) — into
// a Schedule that yields the next fire time. Minimal on purpose: minute
// resolution, UTC, no seconds/years field. Schedules are evaluated in UTC.
package cron

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule computes the next fire time strictly after a given time.
type Schedule interface {
	Next(after time.Time) time.Time
}

var shorthands = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

// Parse parses a schedule spec.
func Parse(spec string) (Schedule, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return nil, fmt.Errorf("empty schedule")
	}
	if rest, ok := strings.CutPrefix(spec, "@every "); ok {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil {
			return nil, fmt.Errorf("@every: %w", err)
		}
		if d <= 0 {
			return nil, fmt.Errorf("@every: duration must be positive")
		}
		return interval{d}, nil
	}
	if canon, ok := shorthands[spec]; ok {
		spec = canon
	}
	return parseCron(spec)
}

type interval struct{ d time.Duration }

func (i interval) Next(after time.Time) time.Time { return after.Add(i.d) }

// cronSchedule holds one bitset per field. Bit N set means value N is allowed.
type cronSchedule struct {
	minute, hour, dom, month, dow uint64
	domStar, dowStar              bool // an unrestricted "*" field
}

type fieldSpec struct {
	name     string
	min, max uint
}

var fields = []fieldSpec{
	{"minute", 0, 59},
	{"hour", 0, 23},
	{"day-of-month", 1, 31},
	{"month", 1, 12},
	{"day-of-week", 0, 6},
}

func parseCron(spec string) (Schedule, error) {
	parts := strings.Fields(spec)
	if len(parts) != 5 {
		return nil, fmt.Errorf("cron expression must have 5 fields, got %d: %q", len(parts), spec)
	}
	var masks [5]uint64
	for i, p := range parts {
		m, err := parseField(p, fields[i])
		if err != nil {
			return nil, fmt.Errorf("%s field: %w", fields[i].name, err)
		}
		masks[i] = m
	}
	return &cronSchedule{
		minute: masks[0], hour: masks[1], dom: masks[2], month: masks[3], dow: masks[4],
		domStar: parts[2] == "*", dowStar: parts[4] == "*",
	}, nil
}

// parseField parses one cron field (supports *, N, a-b, */s, a-b/s, comma lists).
func parseField(s string, f fieldSpec) (uint64, error) {
	var mask uint64
	for _, part := range strings.Split(s, ",") {
		rng, step := part, uint(1)
		if slash := strings.IndexByte(part, '/'); slash >= 0 {
			var err error
			if step, err = atou(part[slash+1:]); err != nil || step == 0 {
				return 0, fmt.Errorf("bad step in %q", part)
			}
			rng = part[:slash]
		}
		lo, hi := f.min, f.max
		if rng != "*" {
			if dash := strings.IndexByte(rng, '-'); dash >= 0 {
				var err1, err2 error
				lo, err1 = atou(rng[:dash])
				hi, err2 = atou(rng[dash+1:])
				if err1 != nil || err2 != nil {
					return 0, fmt.Errorf("bad range %q", rng)
				}
			} else {
				v, err := atou(rng)
				if err != nil {
					return 0, fmt.Errorf("bad value %q", rng)
				}
				lo, hi = v, v
			}
		}
		// day-of-week accepts 7 as Sunday.
		if f.name == "day-of-week" {
			if lo == 7 {
				lo = 0
			}
			if hi == 7 {
				hi = 0
			}
		}
		if lo < f.min || hi > f.max || lo > hi {
			return 0, fmt.Errorf("%q out of range %d-%d", part, f.min, f.max)
		}
		for v := lo; v <= hi; v += step {
			mask |= 1 << v
		}
	}
	return mask, nil
}

func atou(s string) (uint, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n < 0 {
		return 0, fmt.Errorf("not a non-negative integer: %q", s)
	}
	return uint(n), nil
}

func (c *cronSchedule) match(t time.Time) bool {
	if c.minute&(1<<uint(t.Minute())) == 0 ||
		c.hour&(1<<uint(t.Hour())) == 0 ||
		c.month&(1<<uint(t.Month())) == 0 {
		return false
	}
	domOK := c.dom&(1<<uint(t.Day())) != 0
	dowOK := c.dow&(1<<uint(int(t.Weekday()))) != 0
	// Vixie semantics: if both day fields are restricted, either may match; if one
	// is "*", the other governs.
	switch {
	case c.domStar && c.dowStar:
		return true
	case c.domStar:
		return dowOK
	case c.dowStar:
		return domOK
	default:
		return domOK || dowOK
	}
}

// Next returns the first minute strictly after `after` that matches, in UTC.
func (c *cronSchedule) Next(after time.Time) time.Time {
	t := after.UTC().Truncate(time.Minute).Add(time.Minute)
	// Cap the search so an impossible date (e.g. Feb 31) terminates.
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		if c.match(t) {
			return t
		}
		t = t.Add(time.Minute)
	}
	return time.Time{} // never fires
}
