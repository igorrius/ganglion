package event

import (
	"testing"
	"time"
)

func TestExpired(t *testing.T) {
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		ttl  time.Duration
		now  time.Time
		want bool
	}{
		{"no ttl never expires", 0, at.Add(48 * time.Hour), false},
		{"within ttl", 30 * time.Second, at.Add(29 * time.Second), false},
		{"exactly at deadline", 30 * time.Second, at.Add(30 * time.Second), false},
		{"past ttl", 30 * time.Second, at.Add(31 * time.Second), true},
		{"gate command two hours late", time.Minute, at.Add(2 * time.Hour), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := Event{At: at, TTL: tt.ttl}
			if got := e.Expired(tt.now); got != tt.want {
				t.Fatalf("Expired(%v) = %v, want %v", tt.now.Sub(at), got, tt.want)
			}
		})
	}
}
