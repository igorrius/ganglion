// Package backoff computes waits between retries: doubling from a
// minimum, capped at a maximum, with ±20% jitter so that retries from many
// places do not line up.
package backoff

import (
	"math/rand/v2"
	"time"
)

// Policy is a doubling backoff. Jitter returns a number in [0, 1); nil
// means math/rand.
type Policy struct {
	Min, Max time.Duration
	Jitter   func() float64
}

// Delay returns the wait before retry number attempt, counted from 1
// (0 counts as 1): Min·2^(attempt-1) within ±20%, then capped at Max.
// The jitter applies before the cap, so once the doubling passes Max by a
// quarter the wait is exactly Max.
func (p Policy) Delay(attempt int) time.Duration {
	shift := max(attempt, 1) - 1
	if shift >= 62 || p.Min<<shift <= 0 || p.Min<<shift > 2*p.Max {
		return p.Max // far past the cap; also guards the shift overflowing
	}
	jitter := rand.Float64
	if p.Jitter != nil {
		jitter = p.Jitter
	}
	return min(time.Duration(float64(p.Min<<shift)*(0.8+0.4*jitter())), p.Max)
}

// Backoff counts consecutive failures for a Policy.
type Backoff struct {
	p       Policy
	attempt int
}

// New returns a Backoff with no failures counted.
func New(p Policy) *Backoff { return &Backoff{p: p} }

// Next counts a failure and returns the wait before the next try.
func (b *Backoff) Next() time.Duration {
	b.attempt++
	return b.p.Delay(b.attempt)
}

// Reset forgets the failures after a success.
func (b *Backoff) Reset() { b.attempt = 0 }
