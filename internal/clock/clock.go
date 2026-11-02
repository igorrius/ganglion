// Package clock holds calendar helpers shared by the daily limits and the
// router's daily budget.
package clock

import "time"

// DayStart returns midnight of t's day in loc.
func DayStart(t time.Time, loc *time.Location) time.Time {
	t = t.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
}
