package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/transport"
)

// ErrConflict is returned by Run when Telegram answers 409: another
// process polls with this token, or a webhook is set. Polling stops and is
// not retried, so the two processes do not fight (R-DLV-8).
var ErrConflict = errors.New("telegram: another poller or a webhook holds this bot (409)")

// Transport is the Telegram transport.
type Transport struct {
	c           *client
	log         *slog.Logger
	now         func() time.Time
	pollTimeout time.Duration
	backoffMin  time.Duration
	backoffMax  time.Duration
}

var (
	_ transport.Transport = (*Transport)(nil)
	_ transport.Parter    = (*Transport)(nil)
)

type config struct {
	baseURL     string
	hc          *http.Client
	now         func() time.Time
	pollTimeout time.Duration
	log         *slog.Logger
}

// Option configures a Transport.
type Option func(*config)

// WithBaseURL sets the Bot API server (a fake one in tests).
func WithBaseURL(u string) Option { return func(c *config) { c.baseURL = u } }

// WithHTTPClient sets the HTTP client. Its timeout must exceed the poll
// timeout.
func WithHTTPClient(hc *http.Client) Option { return func(c *config) { c.hc = hc } }

// WithClock sets the clock used for event ids; the default is time.Now.
func WithClock(now func() time.Time) Option { return func(c *config) { c.now = now } }

// WithPollTimeout sets how long Telegram holds a getUpdates call; the
// default is 50 s.
func WithPollTimeout(d time.Duration) Option { return func(c *config) { c.pollTimeout = d } }

// WithLogger sets the logger; the default discards.
func WithLogger(l *slog.Logger) Option { return func(c *config) { c.log = l } }

// New returns a transport for token, which must look like
// <bot_id>:<secret>. It does not contact Telegram; Run does.
func New(token string, opts ...Option) (*Transport, error) {
	prefix, secret, ok := strings.Cut(token, ":")
	if id, err := strconv.ParseInt(prefix, 10, 64); !ok || err != nil || id <= 0 || secret == "" {
		return nil, errors.New("telegram: token is not <bot_id>:<secret>")
	}
	cfg := config{now: time.Now, pollTimeout: pollTimeout, log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.hc == nil {
		cfg.hc = &http.Client{Timeout: cfg.pollTimeout + 10*time.Second}
	}
	return &Transport{
		c:           newClient(cfg.baseURL, token, cfg.hc),
		log:         cfg.log,
		now:         cfg.now,
		pollTimeout: cfg.pollTimeout,
		backoffMin:  time.Second,
		backoffMax:  time.Minute,
	}, nil
}

// Name implements transport.Transport.
func (t *Transport) Name() string { return "telegram" }

// Run implements transport.Transport. It polls updates and hands each
// message to sink in order. The offset moves past an update only after
// sink returned nil for it, and Telegram learns the offset on the next
// poll, so an update is confirmed only once it is in the inbox (R-DLV-2).
// Run returns ErrConflict on 409, the error on 401, and ctx.Err() when ctx
// ends; other errors are retried with backoff.
func (t *Transport) Run(ctx context.Context, sink transport.Sink) error {
	pollBackoff := t.newBackoff()
	var me user
	for {
		var err error
		if me, err = t.c.getMe(ctx); err == nil {
			break
		}
		if stop := t.onError(ctx, err, pollBackoff); stop != nil {
			return stop
		}
	}
	botID := me.ID
	t.log.Info("telegram: polling", "bot_id", botID, "username", me.Username)

	sinkBackoff := t.newBackoff()
	var offset int64
	for {
		ups, err := t.c.getUpdates(ctx, offset, t.pollTimeout)
		if err != nil {
			if stop := t.onError(ctx, err, pollBackoff); stop != nil {
				return stop
			}
			continue
		}
		pollBackoff.reset()
		for _, u := range ups {
			if e, ok := t.toEvent(botID, u); ok {
				if err := sink(ctx, e); err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					// Keep the offset at this update: Telegram sends it
					// again on the next poll.
					d := sinkBackoff.next()
					t.log.Warn("telegram: sink failed, update not confirmed",
						"update_id", u.UpdateID, "conv", e.Conv, "err", err, "retry_in", d)
					if err := sleep(ctx, d); err != nil {
						return err
					}
					break
				}
				sinkBackoff.reset()
			}
			offset = u.UpdateID + 1
		}
	}
}

// onError decides what a failed Bot API call means for Run: a non-nil
// result stops Run, nil means poll again (after waiting).
func (t *Transport) onError(ctx context.Context, err error, b *backoff) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if ae, ok := errors.AsType[*apiError](err); ok {
		switch ae.Code {
		case http.StatusConflict:
			t.log.Error("telegram: 409, polling stopped", "err", err)
			return fmt.Errorf("%w: %s", ErrConflict, ae.Description)
		case http.StatusUnauthorized:
			t.log.Error("telegram: token rejected", "err", err)
			return err
		case http.StatusTooManyRequests:
			d := max(ae.RetryAfter, time.Second)
			t.log.Warn("telegram: rate limited", "retry_in", d)
			return sleep(ctx, d)
		}
	}
	d := b.next()
	t.log.Warn("telegram: poll failed", "err", err, "retry_in", d)
	return sleep(ctx, d)
}

// toEvent turns an update into an event. Only text messages (or captions)
// from people become events.
func (t *Transport) toEvent(botID int64, u update) (event.Event, bool) {
	m := u.Message
	if m == nil || m.From == nil || m.From.IsBot {
		t.log.Debug("telegram: update skipped", "update_id", u.UpdateID, "reason", "not a message from a person")
		return event.Event{}, false
	}
	text := m.Text
	if text == "" {
		text = m.Caption
	}
	if text == "" {
		t.log.Debug("telegram: update skipped", "update_id", u.UpdateID, "reason", "no text")
		return event.Event{}, false
	}
	var thread int64
	if m.IsTopicMessage {
		thread = m.MessageThreadID
	}
	payload, err := json.Marshal(event.Message{
		Text: text,
		From: Principal(m.From.ID),
		Name: strings.TrimSpace(m.From.FirstName + " " + m.From.LastName),
	})
	if err != nil {
		return event.Event{}, false // a struct of strings always marshals
	}
	now := t.now()
	at := now
	if m.Date > 0 {
		at = time.Unix(m.Date, 0).UTC()
	}
	return event.Event{
		ID:      event.NewID(now),
		IdemKey: "telegram:" + strconv.FormatInt(botID, 10) + ":" + strconv.FormatInt(u.UpdateID, 10),
		Source:  "telegram",
		Conv:    ConvKey(m.Chat.ID, thread),
		Kind:    "message",
		Payload: payload,
		At:      at,
	}, true
}

// Send implements transport.Transport. The text is sent as plain text. A
// 429 becomes transport.RetryAfterError; 400 and 403 (and a conversation
// that is not a Telegram one) wrap transport.ErrPermanent; anything else
// is returned as is and retried by the sender.
func (t *Transport) Send(ctx context.Context, msg event.Outgoing) error {
	chat, thread, err := ParseConv(msg.Conv)
	if err != nil {
		return fmt.Errorf("%w: %w", err, transport.ErrPermanent)
	}
	_, err = t.c.sendMessage(ctx, sendParams{
		ChatID:              chat,
		MessageThreadID:     thread,
		Text:                msg.Text,
		DisableNotification: msg.Silent,
	})
	ae, ok := errors.AsType[*apiError](err)
	if !ok {
		return err // nil, or a network error: retried
	}
	switch ae.Code {
	case http.StatusTooManyRequests:
		return &transport.RetryAfterError{After: max(ae.RetryAfter, time.Second)}
	case http.StatusBadRequest, http.StatusForbidden:
		return fmt.Errorf("%w: %w", err, transport.ErrPermanent)
	}
	return err
}

// backoff grows from min to max, doubling, with ±20% jitter.
type backoff struct {
	min, max, cur time.Duration
}

func (t *Transport) newBackoff() *backoff { return &backoff{min: t.backoffMin, max: t.backoffMax} }

func (b *backoff) next() time.Duration {
	if b.cur == 0 {
		b.cur = b.min
	} else {
		b.cur = min(2*b.cur, b.max)
	}
	return min(time.Duration(float64(b.cur)*(0.8+0.4*rand.Float64())), b.max)
}

func (b *backoff) reset() { b.cur = 0 }

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
