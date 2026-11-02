package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/backoff"
	"github.com/igorrius/ganglion/transport"
)

// ErrConflict is returned by Run when Telegram answers 409: another
// process polls with this token, or a webhook is set. Polling stops and is
// not retried, so the two processes do not fight (R-DLV-8).
var ErrConflict = errors.New("telegram: another poller or a webhook holds this bot (409)")

var tokenRE = regexp.MustCompile(`^[1-9][0-9]*:[A-Za-z0-9_-]+$`)

// ReplyMode decides when an answer is sent as a reply to the person's
// message (ADR-0025).
type ReplyMode string

// Reply modes.
const (
	ReplyAuto   ReplyMode = "auto"   // in groups, or when newer messages came after the one answered
	ReplyAlways ReplyMode = "always" // every answer that has a ReplyTo
	ReplyOff    ReplyMode = "off"
)

// Name is the transport's name: the Source of its events and the
// Transport of messages for it.
const Name = "telegram"

// Transport is the Telegram transport.
type Transport struct {
	c           *client
	log         *slog.Logger
	now         func() time.Time
	pollTimeout time.Duration
	retry       backoff.Policy // between failed polls and failed sinks
	replyMode   ReplyMode
	typingEvery time.Duration

	mu     sync.Mutex
	last   map[int64]int64 // chat → the newest incoming message id
	groups map[int64]bool  // chats that are groups or supergroups
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
	replyMode   ReplyMode
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

// WithReplyMode sets when answers are sent as replies; the default is
// ReplyAuto.
func WithReplyMode(m ReplyMode) Option { return func(c *config) { c.replyMode = m } }

// WithLogger sets the logger; the default discards.
func WithLogger(l *slog.Logger) Option { return func(c *config) { c.log = l } }

// New returns a transport for token, which must look like
// <bot_id>:<secret>. It does not contact Telegram; Run does.
func New(token string, opts ...Option) (*Transport, error) {
	// Strict: a token with whitespace or other characters would be escaped
	// inside URL errors, where redaction cannot find it.
	if !tokenRE.MatchString(token) {
		return nil, errors.New("telegram: token is not <bot_id>:<secret> (digits, a colon, then letters, digits, _ or -)")
	}
	cfg := config{now: time.Now, pollTimeout: pollTimeout, log: slog.New(slog.DiscardHandler), replyMode: ReplyAuto}
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
		retry:       backoff.Policy{Min: time.Second, Max: time.Minute},
		replyMode:   cfg.replyMode,
		typingEvery: 4 * time.Second,
		last:        map[int64]int64{},
		groups:      map[int64]bool{},
	}, nil
}

// Name implements transport.Transport.
func (t *Transport) Name() string { return Name }

// Run implements transport.Transport. It polls updates and hands each
// message to sink in order. The offset moves past an update only after
// sink returned nil for it, and Telegram learns the offset on the next
// poll, so an update is confirmed only once it is in the inbox (R-DLV-2).
// Run returns ErrConflict on 409, the error on 401, and ctx.Err() when ctx
// ends; other errors are retried with backoff.
func (t *Transport) Run(ctx context.Context, sink transport.Sink) error {
	poll := backoff.New(t.retry)
	me, err := t.whoAmI(ctx, poll)
	if err != nil {
		return err
	}
	t.log.Info("telegram: polling", "bot_id", me.ID, "username", me.Username)
	sinkRetry := backoff.New(t.retry)
	var offset int64
	for {
		ups, err := t.c.getUpdates(ctx, offset, t.pollTimeout)
		if err != nil {
			if stop := t.onError(ctx, err, poll); stop != nil {
				return stop
			}
			continue
		}
		poll.Reset()
		if offset, err = t.hand(ctx, sink, me.ID, ups, offset, sinkRetry); err != nil {
			return err
		}
	}
}

// whoAmI asks for the bot's identity until it gets it or onError stops.
func (t *Transport) whoAmI(ctx context.Context, b *backoff.Backoff) (user, error) {
	for {
		me, err := t.c.getMe(ctx)
		if err == nil {
			return me, nil
		}
		if stop := t.onError(ctx, err, b); stop != nil {
			return user{}, stop
		}
	}
}

// hand gives the updates to sink in order and returns the offset past the
// last one handled. When sink fails, the offset stays at that update, so
// Telegram sends it again on the next poll, after a wait.
func (t *Transport) hand(ctx context.Context, sink transport.Sink, botID int64, ups []update, offset int64, b *backoff.Backoff) (int64, error) {
	for _, u := range ups {
		t.seen(u)
		if e, ok := t.toEvent(botID, u); ok {
			if err := sink(ctx, e); err != nil {
				if ctx.Err() != nil {
					return offset, ctx.Err()
				}
				d := b.Next()
				t.log.Warn("telegram: sink failed, update not confirmed",
					"update_id", u.UpdateID, "conv", e.Conv, "err", err, "retry_in", d)
				return offset, sleep(ctx, d)
			}
			b.Reset()
		}
		offset = u.UpdateID + 1
	}
	return offset, nil
}

// onError decides what a failed Bot API call means for Run: a non-nil
// result stops Run, nil means poll again (after waiting).
func (t *Transport) onError(ctx context.Context, err error, b *backoff.Backoff) error {
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
	d := b.Next()
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
		Ref:  strconv.FormatInt(m.MessageID, 10),
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
		Source:  Name,
		Conv:    ConvKey(m.Chat.ID, thread),
		Kind:    event.KindMessage,
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
	text, ents := render(msg.Text)
	if strings.TrimSpace(text) == "" {
		text, ents = msg.Text, nil // nothing visible left: send the source rather than nothing
	}
	_, err = t.c.sendMessage(ctx, sendParams{
		ChatID:              chat,
		MessageThreadID:     thread,
		Text:                text,
		Entities:            ents,
		DisableNotification: msg.Silent,
		ReplyParameters:     t.replyTo(chat, msg.ReplyTo),
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

// seen records the newest message id and the kind of each chat, for
// ReplyAuto.
func (t *Transport) seen(u update) {
	m := u.Message
	if m == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.last[m.Chat.ID] = max(t.last[m.Chat.ID], m.MessageID)
	if m.Chat.Type == "group" || m.Chat.Type == "supergroup" {
		t.groups[m.Chat.ID] = true
	}
}

// replyTo decides whether an answer quotes the message ref.
func (t *Transport) replyTo(chat int64, ref string) *replyParameters {
	id, err := strconv.ParseInt(ref, 10, 64)
	if err != nil || id <= 0 || t.replyMode == ReplyOff {
		return nil
	}
	if t.replyMode != ReplyAlways {
		t.mu.Lock()
		newer, group := t.last[chat] > id, t.groups[chat]
		t.mu.Unlock()
		if !newer && !group {
			return nil
		}
	}
	return &replyParameters{MessageID: id, AllowSendingWithoutReply: true}
}

// TypingHook is a core.TurnHook: "typing…" for the length of a turn that
// answers a Telegram message, nothing for other events.
func (t *Transport) TypingHook(ctx context.Context, e event.Event) (stop func()) {
	if e.Source != Name || e.Kind != event.KindMessage {
		return func() {}
	}
	ctx, stop = context.WithCancel(ctx)
	go t.Typing(ctx, e.Conv)
	return stop
}

// Typing shows "typing…" in conv until ctx ends, renewing it every few
// seconds (Telegram shows it for about five). Errors are only logged.
func (t *Transport) Typing(ctx context.Context, conv string) {
	chat, thread, err := ParseConv(conv)
	if err != nil {
		return
	}
	tick := time.NewTicker(t.typingEvery)
	defer tick.Stop()
	for {
		if err := t.c.sendChatAction(ctx, chat, thread, "typing"); err != nil && ctx.Err() == nil {
			t.log.Debug("telegram: typing not shown", "conv", conv, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

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
