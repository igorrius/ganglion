package status_test

import (
	"testing"
	"time"

	"github.com/igorrius/ganglion/status"
)

func TestBytes(t *testing.T) {
	for n, want := range map[int64]string{0: "0 B", 512: "512 B", 2048: "2 KB", 45 << 20: "45 MB", 1288490188: "1.2 GB"} {
		if got := status.Bytes(n); got != want {
			t.Errorf("Bytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0: "0 s", 40 * time.Second: "40 s", 12 * time.Minute: "12 min", 12*time.Minute + 30*time.Second: "12 min",
		3*time.Hour + 5*time.Minute: "3 h 5 min", 3 * time.Hour: "3 h", 76 * time.Hour: "3 d 4 h", 48 * time.Hour: "2 d",
	} {
		if got := status.Duration(d); got != want {
			t.Errorf("Duration(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestClock(t *testing.T) {
	kyiv, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		t.Fatal(err)
	}
	s := status.Scope{Now: time.Date(2026, 11, 8, 15, 0, 0, 0, kyiv), Location: kyiv}
	if got := status.Clock(time.Date(2026, 11, 8, 10, 40, 0, 0, time.UTC), s); got != "12:40" {
		t.Errorf("today: %q", got)
	}
	if got := status.Clock(time.Date(2026, 11, 7, 10, 40, 0, 0, time.UTC), s); got != "2026-11-07 12:40" {
		t.Errorf("yesterday: %q", got)
	}
}
