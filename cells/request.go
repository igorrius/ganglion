package cells

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ErrDecided is returned when answering a request that is no longer
// pending (already answered, or expired).
var ErrDecided = errors.New("cells: request already decided")

// Request is what a request cell asks; stored as its desired value.
type Request struct {
	Question string          `json:"question"`
	Payload  json.RawMessage `json:"payload,omitempty"` // what will happen if approved
}

// Decision is a request's outcome; stored as its reported value.
type Decision struct {
	Status string `json:"status"` // pending, approved, denied, expired
	By     string `json:"by,omitempty"`
}

// DecisionOf reads the decision of a request cell.
func DecisionOf(c Cell) (Decision, error) {
	if c.Kind != KindRequest {
		return Decision{}, fmt.Errorf("%w: %s is a %s", ErrWrongKind, c.ID, c.Kind)
	}
	var d Decision
	if err := json.Unmarshal(c.Reported, &d); err != nil {
		return Decision{}, fmt.Errorf("cells: %s has no decision: %w", c.ID, err)
	}
	return d, nil
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func newRequestID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return "request." + hex.EncodeToString(b[:])
}

// Ask creates a pending request that expires after ttl.
func (s *Service) Ask(ctx context.Context, r Request, ttl time.Duration, src string) (Cell, error) {
	return s.AskAs(ctx, newRequestID(), r, ttl, src)
}

// AskAs creates a pending request with the given id, for a caller that
// names its requests after its own records (a pending tool call is
// "request:call:<id>"). An existing id is ErrExists.
func (s *Service) AskAs(ctx context.Context, id string, r Request, ttl time.Duration, src string) (Cell, error) {
	if r.Question == "" || ttl <= 0 {
		return Cell{}, fmt.Errorf("%w: a request needs a question and a positive ttl", ErrInvalidValue)
	}
	now := s.now().UTC()
	c := Cell{
		ID:       id,
		Kind:     KindRequest,
		Desired:  mustJSON(r),
		Reported: mustJSON(Decision{Status: StatusPending}),
		Version:  1,
		Updated:  now,
		Expires:  now.Add(ttl),
	}
	if _, err := s.store.Create(ctx, c, src); err != nil {
		return Cell{}, err
	}
	s.signal()
	return c, nil
}

// decide moves a pending request to status. A request past its expiry
// becomes expired instead, and ErrDecided is returned with that cell.
func (s *Service) decide(ctx context.Context, id, status, by string) (Cell, error) {
	var late bool
	c, err := s.update(ctx, id, 0, func(c *Cell) (Change, error) {
		if c.Kind != KindRequest {
			return Change{}, fmt.Errorf("%w: %s is a %s", ErrWrongKind, id, c.Kind)
		}
		if !IsPendingRequest(*c) {
			return Change{}, ErrDecided
		}
		d := Decision{Status: status, By: by}
		if !s.now().Before(c.Expires) && status != StatusExpired {
			d, late = Decision{Status: StatusExpired}, true
		}
		c.Reported, c.ReportedCause = mustJSON(d), CauseExternal
		return Change{Field: FieldReported, Value: c.Reported, Cause: CauseExternal, Source: by}, nil
	})
	if err != nil {
		if errors.Is(err, ErrDecided) {
			cur, gerr := s.store.Get(ctx, id)
			if gerr == nil {
				return cur, fmt.Errorf("%w: %s", ErrDecided, id)
			}
		}
		return Cell{}, err
	}
	if late {
		return c, fmt.Errorf("%w: %s expired before the answer", ErrDecided, id)
	}
	return c, nil
}

// Answer approves or denies a pending request.
func (s *Service) Answer(ctx context.Context, id string, approve bool, by string) (Cell, error) {
	status := StatusDenied
	if approve {
		status = StatusApproved
	}
	return s.decide(ctx, id, status, by)
}

// ExpireDue marks every pending request past its expiry as expired and
// returns how many it marked.
func (s *Service) ExpireDue(ctx context.Context) (int, error) {
	due, err := s.store.PendingRequestsBefore(ctx, s.now())
	if err != nil {
		return 0, err
	}
	n := 0
	for _, c := range due {
		if _, err := s.decide(ctx, c.ID, StatusExpired, "ttl"); err != nil {
			if errors.Is(err, ErrDecided) {
				continue // answered meanwhile
			}
			return n, err
		}
		n++
	}
	return n, nil
}
