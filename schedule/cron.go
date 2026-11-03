package schedule

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ErrCron is a cron expression that does not parse.
var ErrCron = errors.New("schedule: bad cron expression")

// searchYears bounds how far Next looks: a leap day on a given weekday
// comes back within 28 years.
const searchYears = 30

// Spec is a parsed five-field cron expression: minute, hour, day of
// month, month, day of week. Each field is a set of allowed values.
type Spec struct {
	minute, hour, dom, month, dow uint64
	// domStar and dowStar are true when the field starts with "*". As in
	// Vixie cron, a day matches both fields when either is starred, and
	// either field when neither is.
	domStar, dowStar bool
}

// cronField is the name and range of one field.
type cronField struct {
	name     string
	min, max int
}

var cronFields = [5]cronField{
	{"minute", 0, 59}, {"hour", 0, 23}, {"day of month", 1, 31}, {"month", 1, 12}, {"day of week", 0, 6},
}

// ParseCron parses five fields separated by spaces: numbers, ranges a-b,
// lists a,b, steps */n and a-b/n; day of week 0-6 with 0 for Sunday.
// Names, @-macros and seconds are not supported. Errors wrap ErrCron.
func ParseCron(s string) (Spec, error) {
	parts := strings.Fields(s)
	if len(parts) != len(cronFields) {
		return Spec{}, fmt.Errorf("%w: %q has %d fields, want 5", ErrCron, s, len(parts))
	}
	var sets [5]uint64
	for i, f := range cronFields {
		set, err := parseField(parts[i], f)
		if err != nil {
			return Spec{}, fmt.Errorf("%w: %q: %s", ErrCron, s, err.Error())
		}
		sets[i] = set
	}
	return Spec{
		minute: sets[0], hour: sets[1], dom: sets[2], month: sets[3], dow: sets[4],
		domStar: strings.HasPrefix(parts[2], "*"), dowStar: strings.HasPrefix(parts[4], "*"),
	}, nil
}

// parseField parses a comma-separated list of one field into a bit set.
func parseField(s string, f cronField) (uint64, error) {
	var set uint64
	for item := range strings.SplitSeq(s, ",") {
		lo, hi, step, err := parseItem(item, f)
		if err != nil {
			return 0, fmt.Errorf("%s field %q: %w", f.name, s, err)
		}
		for v := lo; v <= hi; v += step {
			set |= 1 << v
		}
	}
	return set, nil
}

// parseItem parses "*", "*/n", "a", "a-b" or "a-b/n" into a range and a
// step.
func parseItem(item string, f cronField) (lo, hi, step int, err error) {
	rng, stepText, stepped := strings.Cut(item, "/")
	step = 1
	if stepped {
		if step, err = strconv.Atoi(stepText); err != nil || step < 1 {
			return 0, 0, 0, fmt.Errorf("step %q is not a positive number", stepText)
		}
	}
	if rng == "*" {
		return f.min, f.max, step, nil
	}
	loText, hiText, isRange := strings.Cut(rng, "-")
	if stepped && !isRange {
		return 0, 0, 0, errors.New("a step needs * or a range")
	}
	if lo, err = number(loText, f); err != nil {
		return 0, 0, 0, err
	}
	hi = lo
	if isRange {
		if hi, err = number(hiText, f); err != nil {
			return 0, 0, 0, err
		}
	}
	if hi < lo {
		return 0, 0, 0, fmt.Errorf("range %q runs backwards", rng)
	}
	return lo, hi, step, nil
}

// number parses one value of the field.
func number(s string, f cronField) (int, error) {
	v, err := strconv.Atoi(s)
	if err != nil || strings.ContainsAny(s, "+-") {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	if v < f.min || v > f.max {
		return 0, fmt.Errorf("%d is outside %d-%d", v, f.min, f.max)
	}
	return v, nil
}

// has reports whether v is in set.
func has(set uint64, v int) bool { return set&(1<<v) != 0 }

// Next returns the first time strictly after after whose wall clock in
// loc matches the spec, or the zero time when none comes within 30
// years (e.g. February 31st). Around a change of the clock (R-SCH-1):
//   - a wall time skipped by the spring change runs at the first valid
//     minute after it, the moment the clock jumps;
//   - a wall time that happens twice in autumn runs once, at its first
//     occurrence.
func (s Spec) Next(after time.Time, loc *time.Location) time.Time {
	c := wall(after, loc).Truncate(time.Minute).Add(time.Minute)
	limit := c.AddDate(searchYears, 0, 0)
	for c.Before(limit) {
		next, ok := s.match(c)
		if !ok {
			c = next
			continue
		}
		if t := instant(c, loc); t.After(after) {
			return t
		}
		c = c.Add(time.Minute)
	}
	return time.Time{}
}

// match reports whether wall time c matches; when it does not, next is
// the first wall time worth trying after it.
func (s Spec) match(c time.Time) (next time.Time, ok bool) {
	y, m, d := c.Date()
	switch {
	case !has(s.month, int(m)):
		return time.Date(y, m+1, 1, 0, 0, 0, 0, time.UTC), false
	case !s.day(c):
		return time.Date(y, m, d+1, 0, 0, 0, 0, time.UTC), false
	case !has(s.hour, c.Hour()):
		return c.Truncate(time.Hour).Add(time.Hour), false
	case !has(s.minute, c.Minute()):
		return c.Add(time.Minute), false
	}
	return c, true
}

// day reports whether the date of c matches the day fields.
func (s Spec) day(c time.Time) bool {
	dom, dow := has(s.dom, c.Day()), has(s.dow, int(c.Weekday()))
	if s.domStar || s.dowStar {
		return dom && dow
	}
	return dom || dow
}

// wall is t's wall clock in loc, carried in a UTC time: calendar
// arithmetic on it ignores the clock changes of loc.
func wall(t time.Time, loc *time.Location) time.Time {
	w := t.In(loc)
	return time.Date(w.Year(), w.Month(), w.Day(), w.Hour(), w.Minute(), w.Second(), w.Nanosecond(), time.UTC)
}

// instant is the first moment whose wall clock in loc is c; for a wall
// time the clock skips, the moment it jumps past it. The offsets a day
// before and after c cover any one change of the clock near c.
func instant(c time.Time, loc *time.Location) time.Time {
	var first time.Time
	for _, probe := range []time.Duration{-24 * time.Hour, 24 * time.Hour} {
		t := c.Add(-offset(c.Add(probe), loc))
		if wall(t, loc).Equal(c) && (first.IsZero() || t.Before(first)) {
			first = t
		}
	}
	if !first.IsZero() {
		return first
	}
	// In the gap: c read with the earlier offset lands after the jump,
	// in the zone that starts at the jump.
	start, _ := c.Add(-offset(c.Add(-24*time.Hour), loc)).In(loc).ZoneBounds()
	return start
}

// offset is loc's offset from UTC at t.
func offset(t time.Time, loc *time.Location) time.Duration {
	_, sec := t.In(loc).Zone()
	return time.Duration(sec) * time.Second
}
