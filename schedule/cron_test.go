package schedule

import (
	"errors"
	"testing"
	"time"
)

func TestParseCron(t *testing.T) {
	for _, s := range []string{
		"* * * * *",
		"0 * * * *",
		"*/15 * * * *",
		"0 9 * * 1-5",
		"0,30 8-18/2 1,15 */3 0",
		"59 23 31 12 6",
		"  0   3  *  *  *  ",
		"5-5 0 1 1 *",
	} {
		if _, err := ParseCron(s); err != nil {
			t.Errorf("ParseCron(%q): %v", s, err)
		}
	}
	for _, s := range []string{
		"",
		"* * * *",
		"* * * * * *",
		"60 * * * *",
		"* 24 * * *",
		"* * 0 * *",
		"* * 32 * *",
		"* * * 0 *",
		"* * * 13 *",
		"* * * * 7",
		"* * * * MON",
		"* * * JAN *",
		"*/0 * * * *",
		"5-1 * * * *",
		"1-2-3 * * * *",
		",1 * * * *",
		"1, * * * *",
		"a * * * *",
		"-1 * * * *",
		"1/2 * * * *",
		"*/x * * * *",
		"@hourly",
	} {
		if _, err := ParseCron(s); !errors.Is(err, ErrCron) {
			t.Errorf("ParseCron(%q): %v, want ErrCron", s, err)
		}
	}
}

func TestNext(t *testing.T) {
	utc := time.UTC
	at := func(s string) time.Time {
		v, err := time.Parse("2006-01-02 15:04:05", s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, tc := range []struct {
		cron, after, want string
	}{
		{"0 * * * *", "2026-11-03 10:00:00", "2026-11-03 11:00:00"}, // strictly after
		{"0 * * * *", "2026-11-03 10:59:59", "2026-11-03 11:00:00"},
		{"*/15 * * * *", "2026-11-03 10:16:00", "2026-11-03 10:30:00"},
		{"0 9 * * 1-5", "2026-11-06 09:00:00", "2026-11-09 09:00:00"}, // Friday → Monday
		{"30 23 31 * *", "2026-11-01 00:00:00", "2026-12-31 23:30:00"},
		{"0 0 29 2 *", "2026-03-01 00:00:00", "2028-02-29 00:00:00"},
		{"0 0 1 * 0", "2026-11-03 00:00:00", "2026-11-08 00:00:00"}, // day of month or day of week
		{"0 0 * * 0", "2026-11-03 00:00:00", "2026-11-08 00:00:00"},
		{"0 0 */10 * *", "2026-11-03 00:00:00", "2026-11-11 00:00:00"},
		{"0 0 */10 * 0", "2026-11-03 00:00:00", "2027-01-31 00:00:00"}, // a starred step is "*": both must match
		{"59 23 31 12 *", "2026-12-31 23:59:00", "2027-12-31 23:59:00"},
	} {
		spec, err := ParseCron(tc.cron)
		if err != nil {
			t.Fatal(err)
		}
		if got := spec.Next(at(tc.after), utc); !got.Equal(at(tc.want)) {
			t.Errorf("%q after %s: %s, want %s", tc.cron, tc.after, got, tc.want)
		}
	}
	never, err := ParseCron("0 0 31 2 *")
	if err != nil {
		t.Fatal(err)
	}
	if got := never.Next(at("2026-01-01 00:00:00"), utc); !got.IsZero() {
		t.Errorf("February 31st came at %s", got)
	}
}

// Europe/Kyiv in 2026: spring forward on March 29th, 03:00 EET → 04:00
// EEST; fall back on October 25th, 04:00 EEST → 03:00 EET.
func TestNextAcrossDST(t *testing.T) {
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	utc := func(s string) time.Time {
		v, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	for _, tc := range []struct {
		name, cron, after, want string
	}{
		// 03:00 does not exist on the spring day: the first valid minute after it.
		{"skipped hour", "0 3 * * *", "2026-03-28T23:00:00Z", "2026-03-29T01:00:00Z"}, // 04:00 EEST
		{"skipped half hour", "30 3 * * *", "2026-03-28T23:00:00Z", "2026-03-29T01:00:00Z"},
		{"the day after spring", "0 3 * * *", "2026-03-29T01:00:00Z", "2026-03-30T00:00:00Z"},          // 03:00 EEST
		{"hourly over the gap", "0 * * * *", "2026-03-29T00:00:00Z", "2026-03-29T01:00:00Z"},           // 02:00 → 04:00
		{"hourly over the repeated hour", "0 * * * *", "2026-10-25T00:00:00Z", "2026-10-25T02:00:00Z"}, // 03:00 EEST → 04:00 EET; the second 03:00 does not run
		{"04:00 on the spring day", "0 4 * * *", "2026-03-28T23:00:00Z", "2026-03-29T01:00:00Z"},
		// 03:00 happens twice on the autumn day: once, at the first.
		{"repeated hour", "0 3 * * *", "2026-10-24T22:00:00Z", "2026-10-25T00:00:00Z"}, // 03:00 EEST
		{"not again at the second 03:00", "0 3 * * *", "2026-10-25T00:00:00Z", "2026-10-26T01:00:00Z"},
		{"every 30 min in the first pass", "*/30 * * * *", "2026-10-25T00:00:00Z", "2026-10-25T00:30:00Z"},
		{"every 30 min skips the second pass", "*/30 * * * *", "2026-10-25T00:30:00Z", "2026-10-25T02:00:00Z"}, // 03:30 EEST → 04:00 EET
		{"from inside the second pass", "*/30 * * * *", "2026-10-25T01:10:00Z", "2026-10-25T02:00:00Z"},
	} {
		spec, err := ParseCron(tc.cron)
		if err != nil {
			t.Fatal(err)
		}
		if got := spec.Next(utc(tc.after), kyiv); !got.Equal(utc(tc.want)) {
			t.Errorf("%s: %q after %s: %s (%s), want %s", tc.name, tc.cron, tc.after, got.UTC(), got.In(kyiv), tc.want)
		}
	}
}
