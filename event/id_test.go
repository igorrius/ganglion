package event

import (
	"slices"
	"strings"
	"testing"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

func TestNewIDIsSortableAndUnique(t *testing.T) {
	start := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	ids := make([]string, 1000)
	seen := make(map[string]bool, len(ids))
	for i := range ids {
		id := NewID(start.Add(time.Duration(i) * time.Millisecond))
		if len(id) != 26 {
			t.Fatalf("id %q has length %d, want 26", id, len(id))
		}
		if strings.Trim(id, crockford) != "" {
			t.Fatalf("id %q has characters outside the Crockford alphabet", id)
		}
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
		ids[i] = id
	}
	if !slices.IsSorted(ids) {
		t.Fatal("ids made at increasing times do not sort in time order")
	}
}

func TestNewIDSameMillisecondDiffers(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	a, b := NewID(now), NewID(now)
	if a == b {
		t.Fatalf("two ids in the same millisecond are equal: %q", a)
	}
	if a[:10] != b[:10] {
		t.Fatalf("same millisecond, different time parts: %q, %q", a[:10], b[:10])
	}
}

func TestNewIDEncodesTime(t *testing.T) {
	// 2026-11-01T12:00:00Z is 1793534400000 ms since the epoch; the first
	// ten characters are its 48 bits in Crockford base32 (computed
	// independently).
	now := time.UnixMilli(1793534400000).UTC()
	if got, want := NewID(now)[:10], "01M6BFP5G0"; got != want {
		t.Fatalf("time part = %q, want %q", got, want)
	}
}
