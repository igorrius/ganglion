package host

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// SweepEvery is how often the janitor looks for what is past its age.
const SweepEvery = time.Hour

// Janitor is a node component that removes the entries of the node's places
// that are older than the place's MaxAge, at start and then every SweepEvery
// (R-CMD-8). It removes an entry whole, a symlink as the link, never its
// target.
type Janitor struct {
	places []Place
	log    *slog.Logger
	now    func() time.Time
	after  func(time.Duration) <-chan time.Time
	tel    janitorTelemetry
}

// JanitorOption configures a Janitor.
type JanitorOption func(*Janitor)

// WithJanitorClock sets the clock and the wait between sweeps; a nil after
// keeps time.After.
func WithJanitorClock(now func() time.Time, after func(time.Duration) <-chan time.Time) JanitorOption {
	return func(j *Janitor) {
		j.now = now
		if after != nil {
			j.after = after
		}
	}
}

// NewJanitor returns the janitor of places.
func NewJanitor(places []Place, log *slog.Logger, opts ...JanitorOption) *Janitor {
	j := &Janitor{places: places, log: log, now: time.Now, after: time.After, tel: newJanitorTelemetry()}
	for _, o := range opts {
		o(j)
	}
	return j
}

// Name implements node.Component.
func (*Janitor) Name() string { return "janitor" }

// Run sweeps until ctx ends.
func (j *Janitor) Run(ctx context.Context) error {
	for {
		if n, b := j.Sweep(ctx); n > 0 {
			j.log.Info("host: cleaned the node's old files", "entries", n, "bytes", b)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-j.after(SweepEvery):
		}
	}
}

// Sweep removes what is past its age and returns how many entries and
// bytes went.
func (j *Janitor) Sweep(ctx context.Context) (removed int, bytes int64) {
	now := j.now()
	for _, p := range j.places {
		if p.MaxAge <= 0 {
			continue
		}
		for _, path := range children(p) {
			info, err := os.Lstat(path)
			if err != nil || now.Sub(info.ModTime()) <= p.MaxAge {
				continue
			}
			size := Measure(Place{Path: path}).Bytes
			if err := os.RemoveAll(path); err != nil {
				j.log.Warn("host: an old file stays", "place", string(p.Name), "path", filepath.Base(path), "err", err)
				continue
			}
			removed, bytes = removed+1, bytes+size
			j.tel.cleaned(ctx, p.Name, size)
		}
	}
	return removed, bytes
}
