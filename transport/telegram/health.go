package telegram

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// pollStale is how long without a successful poll before /status warns. A
// long poll returns at least every pollTimeout, so a quiet chat stays fresh.
const pollStale = 2 * time.Minute

// Health is what /status shows of receiving (R-OBS-11): the last successful
// poll, or in webhook mode the last update and what waits at Telegram, and
// a stop with its time. The transport writes it and /status reads it, so
// neither needs the other.
type Health struct {
	now     func() time.Time
	mu      sync.Mutex
	polled  time.Time
	stopped time.Time
	stop    texts.Key

	hook       bool      // the webhook is registered: the lines are the webhook's
	received   time.Time // the last update the webhook brought
	pending    int       // updates waiting at Telegram, at the last reading
	failing    bool      // the last reading: updates wait and deliveries fail
	fallback   texts.Key // why the node polls though the file says webhook
	fallbackAt time.Time
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

// hookOn records that the webhook is registered.
func (h *Health) hookOn() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hook = true
}

// receivedNow records an update the webhook brought.
func (h *Health) receivedNow() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.received = h.now()
}

// hookRead records a reading of getWebhookInfo.
func (h *Health) hookRead(pending int, failing bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.pending, h.failing = pending, failing
}

// fellBack records that the webhook is off for this run and why; the lines
// are polling's again.
func (h *Health) fellBack(reason texts.Key) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.hook, h.fallback, h.fallbackAt = false, reason, h.now()
}

// Name implements status.Source.
func (*Health) Name() string { return Name }

// Status implements status.Source: polls count, not messages, so a quiet chat
// is no warning. A node that polls though its file says webhook warns for
// the whole run (R-DLV-19).
func (h *Health) Status(_ context.Context, s status.Scope) ([]status.Check, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	c := h.polling(s)
	if h.hook {
		c = h.webhook(s)
	} else if h.fallback != "" && c.State == status.OK {
		c.State, c.Reason, c.Since = status.Warn, h.fallback, h.fallbackAt
	}
	if h.stop != "" {
		c.State, c.Reason, c.Since = status.Fail, h.stop, h.stopped
	}
	return []status.Check{c}, nil
}

// webhook is the line of the webhook mode: a quiet chat is no warning,
// updates that wait at Telegram while its deliveries fail are.
func (h *Health) webhook(s status.Scope) status.Check {
	c := status.Check{Section: status.SectionDelivery, State: status.OK, Line: texts.StatusTelegramWebhookQuiet, Vars: texts.Vars{}}
	if !h.received.IsZero() {
		c.Line, c.Vars["time"] = texts.StatusTelegramWebhook, status.Clock(h.received, s)
	}
	if h.failing {
		c.State, c.Reason = status.Warn, texts.StatusReasonWebhookPending
		c.Vars["count"] = strconv.Itoa(h.pending)
	}
	return c
}

// polling is the line of the polling mode.
func (h *Health) polling(s status.Scope) status.Check {
	c := status.Check{Section: status.SectionDelivery, State: status.OK, Line: texts.StatusTelegram}
	if h.polled.IsZero() {
		c.State, c.Line = status.Warn, texts.StatusTelegramWaiting
	} else {
		c.Vars = texts.Vars{"time": status.Clock(h.polled, s)}
		if since := s.Now.Sub(h.polled); since > pollStale {
			c.State, c.Reason, c.Since = status.Warn, texts.StatusReasonPollStale, h.polled
			c.Spans = map[string]time.Duration{"for": since}
		}
	}
	return c
}
