package clock

import (
	"testing"
	"time"
)

func TestDayStartIsMidnightInTheLocation(t *testing.T) {
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	// 23:30 UTC on 1 Nov is 01:30 on 2 Nov in Kyiv (UTC+2).
	got := DayStart(time.Date(2026, 11, 1, 23, 30, 0, 0, time.UTC), kyiv)
	if want := time.Date(2026, 11, 2, 0, 0, 0, 0, kyiv); !got.Equal(want) || got.Location() != kyiv {
		t.Fatalf("DayStart = %v, want %v", got, want)
	}
}
