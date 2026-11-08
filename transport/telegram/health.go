package telegram

import (
	"context"
	"sync"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// pollStale is how long without a successful poll before /status warns. A
// long poll returns at least every pollTimeout, so a quiet chat stays fresh.
const pollStale = 2 * time.Minute

// Health is what /status shows of polling (R-OBS-11): the last successful
// poll, and a stop by 409 or 401 with its time. The transport writes it and
// /status reads it, so neither needs the other.
type Health struct {
	now     func() time.Time
	mu      sync.Mutex
	polled  time.Time
	stopped time.Time
	stop    texts.Key
}

// NewHealth returns an empty Health on the clock now.
func NewHealth(now func() time.Time) *Health { return &Health{now: now} }

// polledNow records a successful getUpdates.
func (h *Health) polledNow() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.polled = h.now()
}

// stoppedNow records why polling stopped.
func (h *Health) stoppedNow(reason texts.Key) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.stopped, h.stop = h.now(), reason
}

// Name implements status.Source.
func (*Health) Name() string { return Name }

// Status implements status.Source: polls count, not messages, so a quiet chat
// is no warning.
func (h *Health) Status(_ context.Context, s status.Scope) ([]status.Check, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := status.Check{Section: status.SectionDelivery, State: status.OK, Line: texts.StatusTelegram}
	if h.polled.IsZero() {
		c.State, c.Line = status.Warn, texts.StatusTelegramWaiting
	} else {
		c.Vars = texts.Vars{"time": status.Clock(h.polled, s)}
		if since := s.Now.Sub(h.polled); since > pollStale {
			c.State, c.Reason, c.Since = status.Warn, texts.StatusReasonPollStale, h.polled
			c.Vars["for"] = status.Duration(since)
		}
	}
	if h.stop != "" {
		c.State, c.Reason, c.Since = status.Fail, h.stop, h.stopped
	}
	return []status.Check{c}, nil
}
