package cells

import (
	"context"
	"encoding/json"
	"fmt"
)

// Reporter records reported values; *Service implements it. Backends get
// one in Apply and report through it, now or later.
type Reporter interface {
	Report(ctx context.Context, id string, v json.RawMessage, cause Cause, src string) (Cell, error)
}

// Backend makes a switch's reported state follow its desired state: the
// virtual backend in tests and shadow mode, a smart plug, a relay. Apply
// may report synchronously or later; it must report with CauseCommanded.
type Backend interface {
	Apply(ctx context.Context, c Cell, r Reporter) error
}

// WithBackend registers a backend under name; cells refer to it by name.
func WithBackend(name string, b Backend) Option {
	return func(s *Service) {
		if s.backends == nil {
			s.backends = map[string]Backend{}
		}
		s.backends[name] = b
	}
}

// apply asks the cell's backend to act and records a failure as an error
// change, so the history shows why the cell is out of sync.
func (s *Service) apply(ctx context.Context, c Cell) (Cell, error) {
	b, ok := s.backends[c.Backend]
	if !ok {
		return c, fmt.Errorf("%w: backend %q of %s is not registered", ErrInvalidValue, c.Backend, c.ID)
	}
	err := b.Apply(ctx, c, s)
	if err == nil {
		return c, nil
	}
	msg := mustJSON(map[string]string{"error": err.Error()})
	recorded, rerr := s.update(ctx, c.ID, 0, func(*Cell) (Change, error) {
		return Change{Field: FieldError, Value: msg, Source: "backend:" + c.Backend}, nil
	})
	if rerr != nil {
		return c, fmt.Errorf("%w (and recording it failed: %v)", err, rerr)
	}
	return recorded, fmt.Errorf("cells: backend %s failed for %s: %w", c.Backend, c.ID, err)
}

// Reconcile asks backends to act on every switch that is out of sync,
// except those changed by hand (Manual), for instance after a restart that
// interrupted an apply. It returns how many cells it applied.
func (s *Service) Reconcile(ctx context.Context) (int, error) {
	all, err := s.store.List(ctx)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range all {
		if c.Kind != KindSwitch || c.Backend == "" || c.Desired == nil || c.InSync() || c.Manual() {
			continue
		}
		if _, err := s.apply(ctx, c); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
