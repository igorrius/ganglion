// Package cellskv is a cells.Store on the cells bucket of the node's bus
// (R-BUS-3, ADR-0107). A cell is one key whose value holds the cell and the
// change that wrote it. A write is a compare-and-set on the key's last
// revision, so two writers never overwrite each other: the one that loses
// gets cells.ErrConflict and reads again.
//
// A change's Seq is the revision of its entry, that is the sequence of the
// bucket's stream: global, and increasing across keys. A cell's Version stays
// the per-cell counter the cells.Store contract asks for.
package cellskv

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/cells"
	"github.com/nats-io/nats.go/jetstream"
)

// streamName is the stream behind the cells bucket.
const streamName = "KV_" + bus.BucketCells

// maxAttempts bounds how often Disown tries one cell again after a lost
// compare-and-set (R-BUS-3).
const maxAttempts = 5

// Store is a cells.Store on the cells bucket.
type Store struct {
	kv jetstream.KeyValue
	js jetstream.JetStream
}

var _ cells.Store = (*Store)(nil)

// New opens the cells bucket, which natsbus.Bus.Ensure has made.
func New(ctx context.Context, js jetstream.JetStream) (*Store, error) {
	kv, err := js.KeyValue(ctx, bus.BucketCells)
	if err != nil {
		return nil, fmt.Errorf("cellskv: bucket %s: %w", bus.BucketCells, err)
	}
	return &Store{kv: kv, js: js}, nil
}

// Create implements cells.Store.
func (s *Store) Create(ctx context.Context, c cells.Cell, src string) (cells.Change, error) {
	ch := cells.Change{CellID: c.ID, Version: c.Version, Field: cells.FieldCreated, Source: src, At: c.Updated}
	data, err := encode(c, &ch)
	if err != nil {
		return cells.Change{}, err
	}
	rev, err := s.kv.Create(ctx, key(c.ID), data)
	if errors.Is(err, jetstream.ErrKeyExists) {
		return cells.Change{}, cells.ErrExists
	}
	if err != nil {
		return cells.Change{}, fmt.Errorf("cellskv: create %s: %w", c.ID, err)
	}
	ch.Seq = int64(rev)
	return ch, nil
}

// read returns the entry of id with the revision it was read at.
func (s *Store) read(ctx context.Context, id string) (entry, uint64, error) {
	kve, err := s.kv.Get(ctx, key(id))
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return entry{}, 0, cells.ErrNotFound
	}
	if err != nil {
		return entry{}, 0, fmt.Errorf("cellskv: get %s: %w", id, err)
	}
	e, err := decode(kve.Value())
	return e, kve.Revision(), err
}

// Get implements cells.Store.
func (s *Store) Get(ctx context.Context, id string) (cells.Cell, error) {
	e, _, err := s.read(ctx, id)
	return e.Cell.cell(), err
}

// List implements cells.Store.
func (s *Store) List(ctx context.Context) ([]cells.Cell, error) {
	w, err := s.kv.WatchAll(ctx, jetstream.IgnoreDeletes())
	if err != nil {
		return nil, fmt.Errorf("cellskv: list: %w", err)
	}
	defer func() { _ = w.Stop() }()
	var out []cells.Cell
	for {
		select {
		case kve := <-w.Updates():
			if kve == nil { // the current values are all in
				slices.SortFunc(out, func(a, b cells.Cell) int { return strings.Compare(a.ID, b.ID) })
				return out, nil
			}
			e, err := decode(kve.Value())
			if err != nil {
				return nil, err
			}
			out = append(out, e.Cell.cell())
		case <-ctx.Done():
			return nil, fmt.Errorf("cellskv: list: %w", ctx.Err())
		}
	}
}

// write stores next over the entry read at rev; a key that moved since is
// cells.ErrConflict. It returns the new revision.
func (s *Store) write(ctx context.Context, next cells.Cell, ch *cells.Change, rev uint64) (uint64, error) {
	data, err := encode(next, ch)
	if err != nil {
		return 0, err
	}
	newRev, err := s.kv.Update(ctx, key(next.ID), data, rev)
	if errors.Is(err, jetstream.ErrKeyRevisionMismatch) {
		return 0, cells.ErrConflict
	}
	if err != nil {
		return 0, fmt.Errorf("cellskv: write %s: %w", next.ID, err)
	}
	return newRev, nil
}

// Update implements cells.Store. The owner and the about of the stored cell
// stay; SetOwner changes them.
func (s *Store) Update(ctx context.Context, c cells.Cell, prevVersion int64, ch cells.Change) (cells.Change, error) {
	cur, rev, err := s.read(ctx, c.ID)
	if err != nil {
		return cells.Change{}, err
	}
	if cur.Cell.Version != prevVersion {
		return cells.Change{}, cells.ErrConflict
	}
	c.Version = prevVersion + 1
	c.Owner, c.About = cur.Cell.Owner, cur.Cell.About
	ch.CellID, ch.Version = c.ID, c.Version
	newRev, err := s.write(ctx, c, &ch, rev)
	if err != nil {
		return cells.Change{}, err
	}
	ch.Seq = int64(newRev)
	return ch, nil
}

// SetOwner implements cells.Store.
func (s *Store) SetOwner(ctx context.Context, id, owner, about string) error {
	cur, rev, err := s.read(ctx, id)
	if err != nil {
		return err
	}
	c := cur.Cell.cell()
	c.Owner, c.About = owner, about
	_, err = s.write(ctx, c, nil, rev)
	return err
}

// Disown implements cells.Store. A cell that changes under it is read
// again, up to maxAttempts times.
func (s *Store) Disown(ctx context.Context, owner string) (int, error) {
	all, err := s.List(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range all {
		if c.Owner != owner {
			continue
		}
		done, err := s.disown(ctx, c.ID, owner)
		if err != nil {
			return n, err
		}
		if done {
			n++
		}
	}
	return n, nil
}

// disown clears the owner of one cell if it still is owner's.
func (s *Store) disown(ctx context.Context, id, owner string) (bool, error) {
	for range maxAttempts {
		cur, rev, err := s.read(ctx, id)
		if err != nil || cur.Cell.Owner != owner {
			return false, err
		}
		c := cur.Cell.cell()
		c.Owner = ""
		if _, err := s.write(ctx, c, nil, rev); !errors.Is(err, cells.ErrConflict) {
			return err == nil, err
		}
	}
	return false, fmt.Errorf("%w: %s keeps changing", cells.ErrConflict, id)
}

// history returns the changes of one cell, oldest first.
func (s *Store) history(ctx context.Context, id string) ([]cells.Change, error) {
	entries, err := s.kv.History(ctx, key(id))
	if errors.Is(err, jetstream.ErrKeyNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cellskv: history of %s: %w", id, err)
	}
	var out []cells.Change
	for _, kve := range entries {
		if !isLive(kve) {
			continue
		}
		e, err := decode(kve.Value())
		if err != nil {
			return nil, err
		}
		if ch, ok := e.change(kve.Revision()); ok {
			out = append(out, ch)
		}
	}
	return out, nil
}

// History implements cells.Store. It is what the bucket keeps of the key:
// the last 64 values, writes that made no change included, so it can hold
// fewer than 64 changes.
func (s *Store) History(ctx context.Context, id string, limit int) ([]cells.Change, error) {
	out, err := s.history(ctx, id)
	if err != nil {
		return nil, err
	}
	slices.Reverse(out)
	return out[:max(0, min(limit, len(out)))], nil
}

// HistorySince implements cells.Store. Like History it reaches back as far
// as the bucket keeps the key's values.
func (s *Store) HistorySince(ctx context.Context, id string, sinceMS int64, limit int) ([]cells.Change, error) {
	all, err := s.history(ctx, id)
	if err != nil {
		return nil, err
	}
	var out []cells.Change
	for _, ch := range all {
		if len(out) >= limit {
			break
		}
		if ch.At.UnixMilli() >= sinceMS {
			out = append(out, ch)
		}
	}
	return out, nil
}

// PendingRequestsBefore implements cells.Store.
func (s *Store) PendingRequestsBefore(ctx context.Context, t time.Time) ([]cells.Cell, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []cells.Cell
	for _, c := range all {
		if cells.IsPendingRequest(c) && !c.Expires.IsZero() && !c.Expires.After(t) {
			out = append(out, c)
		}
	}
	return out, nil
}
