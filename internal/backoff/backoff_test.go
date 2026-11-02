package backoff

import (
	"testing"
	"time"
)

func TestDelayDoublesFromMinUpToMax(t *testing.T) {
	p := Policy{Min: time.Second, Max: 10 * time.Second, Jitter: func() float64 { return 0.5 }} // factor 1.0
	for attempt, want := range map[int]time.Duration{
		0: time.Second, 1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second,
		4: 8 * time.Second, 5: 10 * time.Second, 64: 10 * time.Second,
	} {
		if got := p.Delay(attempt); got != want {
			t.Errorf("Delay(%d) = %v, want %v", attempt, got, want)
		}
	}
}

func TestDelayJitterIsTwentyPercentAndNeverPastMax(t *testing.T) {
	low := Policy{Min: time.Second, Max: time.Minute, Jitter: func() float64 { return 0 }}
	high := Policy{Min: time.Second, Max: time.Minute, Jitter: func() float64 { return 1 }}
	if got := low.Delay(2); got != 1600*time.Millisecond {
		t.Errorf("low jitter: %v, want 1.6s", got)
	}
	if got := high.Delay(2); got != 2400*time.Millisecond {
		t.Errorf("high jitter: %v, want 2.4s", got)
	}
	if got := low.Delay(7); got != 51200*time.Millisecond { // 64s·0.8: jitter before the cap
		t.Errorf("low jitter just past the cap: %v, want 51.2s", got)
	}
	if got := low.Delay(8); got != time.Minute {
		t.Errorf("low jitter well past the cap: %v, want exactly 1m", got)
	}
}

func TestDelayDefaultJitterStaysInRange(t *testing.T) {
	p := Policy{Min: time.Second, Max: time.Hour}
	for range 100 {
		if d := p.Delay(3); d < 3200*time.Millisecond || d > 4800*time.Millisecond {
			t.Fatalf("Delay(3) = %v, want within ±20%% of 4s", d)
		}
	}
}

func TestBackoffCountsAttemptsAndResets(t *testing.T) {
	b := New(Policy{Min: time.Second, Max: time.Minute, Jitter: func() float64 { return 0.5 }})
	for _, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		if got := b.Next(); got != want {
			t.Fatalf("Next() = %v, want %v", got, want)
		}
	}
	b.Reset()
	if got := b.Next(); got != time.Second {
		t.Fatalf("after Reset, Next() = %v, want 1s", got)
	}
}
