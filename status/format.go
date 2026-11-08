package status

import (
	"fmt"
	"time"

	"github.com/igorrius/ganglion/internal/clock"
)

// Bytes is n in the largest binary unit that keeps it at least 1: "512 B",
// "45 MB", with one decimal from a gigabyte up.
func Bytes(n int64) string {
	units := []string{"B", "KB", "MB", "GB", "TB"}
	f, u := float64(n), 0
	for f >= 1024 && u < len(units)-1 {
		f, u = f/1024, u+1
	}
	if u >= 3 {
		return fmt.Sprintf("%.1f %s", f, units[u])
	}
	return fmt.Sprintf("%d %s", int64(f), units[u])
}

// Units are the words of days, hours, minutes and seconds in one language,
// short forms that do not change with the number.
type Units struct{ Day, Hour, Min, Sec string }

// EnglishUnits are the English units.
var EnglishUnits = Units{Day: "d", Hour: "h", Min: "min", Sec: "s"}

// Duration is d in its two largest units, cut, not rounded: "40 s",
// "12 min", "3 h 5 min", "3 d 4 h" in English.
func Duration(d time.Duration, u Units) string {
	d = max(d, 0)
	days, hours, mins := int(d/(24*time.Hour)), int(d/time.Hour)%24, int(d/time.Minute)%60
	switch {
	case days > 0:
		return pair(days, u.Day, hours, u.Hour)
	case hours > 0:
		return pair(hours, u.Hour, mins, u.Min)
	case mins > 0:
		return fmt.Sprintf("%d %s", mins, u.Min)
	default:
		return fmt.Sprintf("%d %s", int(d/time.Second), u.Sec)
	}
}

func pair(a int, au string, b int, bu string) string {
	if b == 0 {
		return fmt.Sprintf("%d %s", a, au)
	}
	return fmt.Sprintf("%d %s %d %s", a, au, b, bu)
}

// Clock is t's time of day in s.Location when it is on s.Now's day there,
// else its date and time.
func Clock(t time.Time, s Scope) string {
	loc := s.Location
	if loc == nil {
		loc = time.UTC
	}
	if clock.DayStart(t, loc).Equal(clock.DayStart(s.Now, loc)) {
		return t.In(loc).Format("15:04")
	}
	return t.In(loc).Format("2006-01-02 15:04")
}
