package cells

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"
)

// Errors returned by the service.
var (
	ErrWrongKind    = errors.New("cells: operation not allowed for this kind")
	ErrInvalidValue = errors.New("cells: invalid value")
	ErrInvalidID    = errors.New("cells: invalid id")
)

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$`)

// Def declares a cell, usually from the node config.
type Def struct {
	ID      string
	Kind    Kind
	Backend string // "" for none
}

// Service enforces the rules of each kind on top of a Store, calls
// backends and wakes watchers.
type Service struct {
	store    Store
	now      func() time.Time
	backends map[string]Backend

	mu   sync.Mutex
	wake chan struct{} // closed and replaced after every write
}

// Option configures a Service.
type Option func(*Service)

// WithClock sets the clock; the default is time.Now.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// NewService returns a service over store.
func NewService(store Store, opts ...Option) *Service {
	s := &Service{store: store, now: time.Now, wake: make(chan struct{})}
	for _, o := range opts {
		o(s)
	}
	return s
}

func (s *Service) signal() {
	s.mu.Lock()
	close(s.wake)
	s.wake = make(chan struct{})
	s.mu.Unlock()
}

func (s *Service) wakeup() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wake
}

// Define creates a cell if it does not exist. Defining an existing cell
// with the same kind and backend is a no-op; a different kind or backend
// is ErrConflict.
func (s *Service) Define(ctx context.Context, d Def) error {
	if !idPattern.MatchString(d.ID) {
		return fmt.Errorf("%w: %q", ErrInvalidID, d.ID)
	}
	switch d.Kind {
	case KindSwitch, KindSensor:
	default:
		return fmt.Errorf("%w: cannot define a %q cell", ErrWrongKind, d.Kind)
	}
	if _, ok := s.backends[d.Backend]; d.Backend != "" && !ok {
		return fmt.Errorf("%w: backend %q of %s is not registered", ErrInvalidValue, d.Backend, d.ID)
	}
	_, err := s.store.Create(ctx, Cell{ID: d.ID, Kind: d.Kind, Backend: d.Backend, Version: 1, Updated: s.now().UTC()}, "config")
	if errors.Is(err, ErrExists) {
		cur, gerr := s.store.Get(ctx, d.ID)
		if gerr != nil {
			return gerr
		}
		if cur.Kind != d.Kind || cur.Backend != d.Backend {
			return fmt.Errorf("%w: %s is a %s on %q, not a %s on %q", ErrConflict, d.ID, cur.Kind, cur.Backend, d.Kind, d.Backend)
		}
		return nil
	}
	if err == nil {
		s.signal()
	}
	return err
}

// Get returns a cell.
func (s *Service) Get(ctx context.Context, id string) (Cell, error) {
	return s.store.Get(ctx, id)
}

// History returns up to limit changes of a cell, newest first.
func (s *Service) History(ctx context.Context, id string, limit int) ([]Change, error) {
	return s.store.History(ctx, id, limit)
}

func validSwitchValue(v json.RawMessage) bool {
	var str string
	return json.Unmarshal(v, &str) == nil && (str == "on" || str == "off")
}

// update applies fn to the current cell and stores it, retrying on version
// conflicts when expectVersion is 0.
func (s *Service) update(ctx context.Context, id string, expectVersion int64, fn func(*Cell) (Change, error)) (Cell, error) {
	for range 10 {
		cur, err := s.store.Get(ctx, id)
		if err != nil {
			return Cell{}, err
		}
		if expectVersion != 0 && cur.Version != expectVersion {
			return Cell{}, fmt.Errorf("%w: %s is at version %d, not %d", ErrConflict, id, cur.Version, expectVersion)
		}
		next := cur
		ch, err := fn(&next)
		if err != nil {
			return Cell{}, err
		}
		next.Updated = s.now().UTC()
		ch.At = next.Updated
		if _, err := s.store.Update(ctx, next, cur.Version, ch); err != nil {
			if errors.Is(err, ErrConflict) && expectVersion == 0 {
				continue
			}
			return Cell{}, err
		}
		s.signal()
		next.Version = cur.Version + 1
		return next, nil
	}
	return Cell{}, fmt.Errorf("%w: %s keeps changing", ErrConflict, id)
}

// SetDesired sets the desired value of a switch ("on" or "off").
// expectVersion 0 accepts any current version; otherwise the cell must be
// at that version or ErrConflict is returned.
//
// If the switch has a backend, the backend is asked to act once the desired
// value is stored; a backend failure is recorded and returned.
func (s *Service) SetDesired(ctx context.Context, id string, v json.RawMessage, src string, expectVersion int64) (Cell, error) {
	c, err := s.update(ctx, id, expectVersion, func(c *Cell) (Change, error) {
		if c.Kind != KindSwitch {
			return Change{}, fmt.Errorf("%w: %s is a %s", ErrWrongKind, id, c.Kind)
		}
		if !validSwitchValue(v) {
			return Change{}, fmt.Errorf("%w: a switch takes \"on\" or \"off\", got %s", ErrInvalidValue, v)
		}
		c.Desired = v
		// A new command: whatever was reported before is no longer evidence
		// of a manual change. Only an external report after this command is.
		c.ReportedCause = ""
		return Change{Field: FieldDesired, Value: v, Source: src}, nil
	})
	if err != nil || c.Backend == "" {
		return c, err
	}
	return s.apply(ctx, c)
}

// Report records the reported value of a switch or a sensor and why it
// changed.
func (s *Service) Report(ctx context.Context, id string, v json.RawMessage, cause Cause, src string) (Cell, error) {
	return s.update(ctx, id, 0, func(c *Cell) (Change, error) {
		switch c.Kind {
		case KindSwitch:
			if !validSwitchValue(v) {
				return Change{}, fmt.Errorf("%w: a switch reports \"on\" or \"off\", got %s", ErrInvalidValue, v)
			}
		case KindSensor:
			if !json.Valid(v) {
				return Change{}, fmt.Errorf("%w: not JSON: %s", ErrInvalidValue, v)
			}
		default:
			return Change{}, fmt.Errorf("%w: %s is a %s", ErrWrongKind, id, c.Kind)
		}
		c.Reported, c.ReportedCause = v, cause
		return Change{Field: FieldReported, Value: v, Cause: cause, Source: src}, nil
	})
}

// Watch streams every change with Seq > fromSeq: first the stored ones,
// then new ones as they happen. The store is the source of truth, so a slow
// reader never loses a change. The channel closes when ctx ends.
func (s *Service) Watch(ctx context.Context, fromSeq int64) <-chan Change {
	out := make(chan Change, 64)
	go func() {
		defer close(out)
		seq := fromSeq
		const page = 100
		for {
			wake := s.wakeup() // taken before reading, so no write is missed
			changes, err := s.store.ChangesSince(ctx, seq, page)
			if err != nil {
				select {
				case <-ctx.Done():
					return
				case <-time.After(100 * time.Millisecond):
					continue
				}
			}
			for _, c := range changes {
				select {
				case out <- c:
					seq = c.Seq
				case <-ctx.Done():
					return
				}
			}
			if len(changes) == page {
				continue
			}
			select {
			case <-wake:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// InSync reports whether the reported value equals the desired one.
func (c Cell) InSync() bool {
	return c.Desired != nil && c.Reported != nil && jsonEqual(c.Desired, c.Reported)
}

// Manual reports whether a commanded switch is out of sync because its
// state was changed externally (for example by hand) after the last
// command — not by a late report of an earlier command, and not by a report
// that predates the command.
func (c Cell) Manual() bool {
	return c.Kind == KindSwitch && c.Desired != nil && c.Reported != nil &&
		!c.InSync() && c.ReportedCause == CauseExternal
}

func jsonEqual(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return false
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}
