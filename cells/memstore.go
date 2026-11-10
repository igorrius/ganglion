package cells

import (
	"context"
	"slices"
	"strings"
	"sync"
	"time"
)

// MemStore is an in-memory Store for tests and ephemeral nodes.
type MemStore struct {
	mu      sync.Mutex
	cells   map[string]Cell
	changes []Change
	seq     int64
}

// NewMemStore returns an empty store.
func NewMemStore() *MemStore {
	return &MemStore{cells: map[string]Cell{}}
}

func (s *MemStore) append(ch Change) Change {
	s.seq++
	ch.Seq = s.seq
	s.changes = append(s.changes, ch)
	return ch
}

// Create implements Store.
func (s *MemStore) Create(_ context.Context, c Cell, src string) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.cells[c.ID]; ok {
		return Change{}, ErrExists
	}
	s.cells[c.ID] = c
	return s.append(Change{CellID: c.ID, Version: c.Version, Field: FieldCreated, Source: src, At: c.Updated}), nil
}

// Get implements Store.
func (s *MemStore) Get(_ context.Context, id string) (Cell, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cells[id]
	if !ok {
		return Cell{}, ErrNotFound
	}
	return c, nil
}

// List implements Store.
func (s *MemStore) List(_ context.Context) ([]Cell, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Cell, 0, len(s.cells))
	for _, c := range s.cells {
		out = append(out, c)
	}
	slices.SortFunc(out, func(a, b Cell) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// Update implements Store.
func (s *MemStore) Update(_ context.Context, c Cell, prevVersion int64, ch Change) (Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.cells[c.ID]
	if !ok {
		return Change{}, ErrNotFound
	}
	if cur.Version != prevVersion {
		return Change{}, ErrConflict
	}
	c.Version = prevVersion + 1
	s.cells[c.ID] = c
	ch.CellID, ch.Version = c.ID, c.Version
	return s.append(ch), nil
}

// ChangesSince implements Store.
func (s *MemStore) ChangesSince(_ context.Context, seq int64, limit int) ([]Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i, _ := slices.BinarySearchFunc(s.changes, seq+1, func(c Change, t int64) int { return int(c.Seq - t) })
	end := min(len(s.changes), i+limit)
	return slices.Clone(s.changes[i:end]), nil
}

// History implements Store.
func (s *MemStore) History(_ context.Context, id string, limit int) ([]Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Change
	for i := len(s.changes) - 1; i >= 0 && len(out) < limit; i-- {
		if s.changes[i].CellID == id {
			out = append(out, s.changes[i])
		}
	}
	return out, nil
}

// PendingRequestsBefore implements Store.
func (s *MemStore) PendingRequestsBefore(_ context.Context, t time.Time) ([]Cell, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Cell
	for _, c := range s.cells {
		if IsPendingRequest(c) && !c.Expires.IsZero() && !c.Expires.After(t) {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b Cell) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

// HistorySince implements Store.
func (s *MemStore) HistorySince(_ context.Context, id string, sinceMS int64, limit int) ([]Change, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Change
	for _, ch := range s.changes {
		if len(out) >= limit {
			break
		}
		if ch.CellID == id && ch.At.UnixMilli() >= sinceMS {
			out = append(out, ch)
		}
	}
	return out, nil
}

// SetOwner implements Store.
func (s *MemStore) SetOwner(_ context.Context, id, owner, about string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cells[id]
	if !ok {
		return ErrNotFound
	}
	c.Owner, c.About = owner, about
	s.cells[id] = c
	return nil
}

// Disown implements Store.
func (s *MemStore) Disown(_ context.Context, owner string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, c := range s.cells {
		if c.Owner == owner {
			c.Owner = ""
			s.cells[id] = c
			n++
		}
	}
	return n, nil
}
