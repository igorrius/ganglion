package telegram

import (
	"cmp"
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
	txt "github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/texts"
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
	rich        bool
	richCount   richCounter
	typingEvery time.Duration
	commands    []transport.Command // listed in the command hints at start
	files       fileCounter
	groupMsgs   groupCounter

	health *Health // what /status shows of polling

	mu     sync.Mutex
	last   map[int64]int64 // chat → the newest incoming message id
	groups map[int64]bool  // chats that are groups or supergroups
	topics TopicFunc
	titles map[topicKey]topicTitle // the title last told of each forum topic
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
	commands    []transport.Command
	rich        bool
	health      *Health
	topics      TopicFunc
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

// WithRichMessages sets whether a message with a table or two headings goes out
// as a rich message (R-DLV-11); the default is off.
func WithRichMessages(on bool) Option { return func(c *config) { c.rich = on } }

// WithCommands sets the commands listed in Telegram's command hints at
// start (setMyCommands, rulings R43, R45); the default lists none and
// leaves the hints alone.
func WithCommands(cmds []transport.Command) Option { return func(c *config) { c.commands = cmds } }

// WithHealth sets where polling's health is kept for /status (R-OBS-11); the
// default keeps it where nothing reads it.
func WithHealth(h *Health) Option { return func(c *config) { c.health = h } }

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
	cfg := config{now: time.Now, pollTimeout: pollTimeout, log: slog.New(slog.DiscardHandler), replyMode: ReplyAuto, topics: noTopics}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.hc == nil {
		cfg.hc = &http.Client{Timeout: cfg.pollTimeout + 10*time.Second}
	}
	if cfg.health == nil {
		cfg.health = NewHealth(cfg.now)
	}
	return &Transport{
		c:           newClient(cfg.baseURL, token, cfg.hc),
		log:         cfg.log,
		files:       newFileCounter(),
		groupMsgs:   newGroupCounter(),
		now:         cfg.now,
		pollTimeout: cfg.pollTimeout,
		retry:       backoff.Policy{Min: time.Second, Max: time.Minute},
		replyMode:   cfg.replyMode,
		rich:        cfg.rich,
		richCount:   newRichCounter(),
		health:      cfg.health,
		typingEvery: 4 * time.Second,
		commands:    cfg.commands,
		last:        map[int64]int64{},
		groups:      map[int64]bool{},
		topics:      cfg.topics,
		titles:      map[topicKey]topicTitle{},
	}, nil
}

// Name implements transport.Transport.
func (t *Transport) Name() string { return Name }

// Run implements transport.Transport. At start it lists its commands in
// the command hints (registerCommands), beside polling, under one short
// deadline. It polls updates and hands each
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
	// The command hints go out beside polling, never ahead of it; Run
	// waits for them, bounded by their deadline, before it returns.
	hctx, stopHints := context.WithTimeout(ctx, commandsTimeout)
	var hints sync.WaitGroup
	hints.Go(func() { t.registerCommands(hctx) })
	defer func() { stopHints(); hints.Wait() }()
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
		t.health.polledNow()
		if offset, err = t.hand(ctx, sink, me, ups, offset, sinkRetry); err != nil {
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
func (t *Transport) hand(ctx context.Context, sink transport.Sink, me user, ups []update, offset int64, b *backoff.Backoff) (int64, error) {
	for _, u := range ups {
		t.seen(u)
		t.noteTopic(ctx, u)
		e, ok := t.toEvent(me, u)
		if !ok {
			t.answerCallback(ctx, u)
			offset = u.UpdateID + 1
			continue
		}
		err := sink(ctx, e)
		switch {
		case errors.Is(err, transport.ErrDropped):
			t.answerCallback(ctx, u) // no handler will
		case err != nil:
			if ctx.Err() != nil {
				return offset, ctx.Err()
			}
			d := b.Next()
			t.log.Warn("telegram: sink failed, update not confirmed",
				"update_id", u.UpdateID, "conv", e.Conv, "err", err, "retry_in", d)
			return offset, sleep(ctx, d)
		}
		b.Reset()
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
			t.health.stoppedNow(texts.StatusReasonConflict)
			return fmt.Errorf("%w: %s", ErrConflict, ae.Description)
		case http.StatusUnauthorized:
			t.log.Error("telegram: token rejected", "err", err)
			t.health.stoppedNow(texts.StatusReasonUnauthorized)
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

// toEvent turns an update into an event: a text message (or caption)
// from a person, a press of one of our buttons, or the bot being added to
// a group.
func (t *Transport) toEvent(me user, u update) (event.Event, bool) {
	if u.CallbackQuery != nil {
		return t.decisionEvent(me.ID, u)
	}
	if u.MyChatMember != nil {
		return t.joinedEvent(me, u)
	}
	return t.messageEvent(me, u)
}

// messageEvent turns a message update into an event. Messages from people
// with text, a caption or files become events; a file is described,
// never dropped (R-DLV-10). Text the sender did not write
// is marked (ruling R35): Telegram puts a forward in a message of its
// own, so a forward's whole text is someone else's. In a group only a
// message that addresses the bot becomes an event (R-DLV-16): the rest is
// dropped here, before the sink, and its text is never logged.
func (t *Transport) messageEvent(me user, u update) (event.Event, bool) {
	m := u.Message
	if m == nil || m.From == nil || m.From.IsBot {
		t.log.Debug("telegram: update skipped", "update_id", u.UpdateID, "reason", "not a message from a person")
		return event.Event{}, false
	}
	text := m.Text
	if text == "" {
		text = m.Caption
	}
	if text == "" && present(m.RichMessage) {
		text = t.richText(m.RichMessage)
	}
	fs := files(m)
	if text == "" && len(fs) == 0 {
		t.log.Debug("telegram: update skipped", "update_id", u.UpdateID, "reason", "no text and no file")
		return event.Event{}, false
	}
	if isGroup(m.Chat) && !addressed(m, me.ID, me.Username) {
		t.groupMsgs.add(context.Background(), groupUnaddressed)
		t.log.Debug("telegram: update skipped", "update_id", u.UpdateID, "reason", "a group message that does not address the bot")
		return event.Event{}, false
	}
	t.files.add(context.Background(), fs)
	payload, err := json.Marshal(t.incoming(me.ID, m, text, fs))
	if err != nil {
		return event.Event{}, false // a struct of strings always marshals
	}
	var thread int64
	if m.IsTopicMessage {
		thread = m.MessageThreadID
	}
	now := t.now()
	at := now
	if m.Date > 0 {
		at = time.Unix(m.Date, 0).UTC()
	}
	return event.Event{
		ID:      event.NewID(now),
		IdemKey: idemKey(me.ID, u.UpdateID),
		Source:  Name,
		Conv:    ConvKey(m.Chat.ID, thread),
		Kind:    event.KindMessage,
		Payload: payload,
		At:      at,
	}, true
}

// incoming is the message m as the rest of the system sees it, with text
// as its text and fs as its files.
func (t *Transport) incoming(botID int64, m *message, text string, fs []event.File) event.Message {
	msg := event.Message{
		Text:          text,
		From:          Principal(m.From.ID),
		Name:          strings.TrimSpace(m.From.FirstName + " " + m.From.LastName),
		Lang:          m.From.LanguageCode,
		Ref:           strconv.FormatInt(m.MessageID, 10),
		Forwarded:     present(m.ForwardOrigin) || m.IsAutomaticForward || present(m.RichMessage),
		ExternalReply: present(m.ExternalReply),
		Files:         fs,
	}
	if m.Quote != nil {
		msg.Quote = m.Quote.Text
	}
	if r := m.ReplyToMessage; r != nil && !topicRoot(m, r) {
		msg.Reply = t.replied(botID, m.From.ID, r)
	}
	if isGroup(m.Chat) {
		t.groupMsgs.add(context.Background(), groupAddressed)
		msg.Group, msg.GroupTitle = GroupKey(m.Chat.ID), m.Chat.Title
	}
	return msg
}

// topicRoot reports whether r, the message m replies to, is the root of m's forum
// topic: Telegram points every message in a topic that is no explicit reply at the
// service message that created the topic, whose id is the topic's. That is no
// reply, so it brings no note and no taint.
func topicRoot(m, r *message) bool {
	return (m.IsTopicMessage && r.MessageID == m.MessageThreadID) || present(r.ForumTopicCreated)
}

// replied is what a reply answers, as Telegram reports it (R-DLV-13). Its text is
// read the way an incoming message's is, cut, and its files only noted.
func (t *Transport) replied(botID, sender int64, r *message) *event.Replied {
	out := &event.Replied{Ref: strconv.FormatInt(r.MessageID, 10), HasFile: len(files(r)) > 0}
	if r.Date > 0 {
		out.At = time.Unix(r.Date, 0).UTC()
	}
	if r.From != nil {
		out.ByBot = r.From.ID == botID
		out.BySender = r.From.ID == sender && !r.From.IsBot
	}
	// A rich message comes from a bot (R-DLV-12): someone else's words, unless it is
	// this node's bot's own, which the agent reads from its outbox.
	out.Forwarded = present(r.ForwardOrigin) || r.IsAutomaticForward || (present(r.RichMessage) && !out.ByBot)
	body := cmp.Or(r.Text, r.Caption)
	if body == "" && present(r.RichMessage) {
		body = readRich(r.RichMessage) // not counted: richText's metric is for messages to the bot
	}
	out.Text = txt.Truncate(body, event.MaxRepliedRunes)
	return out
}

// Send implements transport.Transport. A message with Ack answers that
// button press with its Text as a toast. The Markdown text is sent with
// entities, and Buttons as an inline keyboard; a message with Edit
// changes that message instead (editMessageText, or editMessageReplyMarkup
// for an Edit without Text), where "not modified" counts as done. A 429 becomes transport.RetryAfterError; 400 and 403
// (a message to edit that is gone among them, and a conversation that is
// not a Telegram one) wrap transport.ErrPermanent; anything else is
// returned as is and retried by the sender.
func (t *Transport) Send(ctx context.Context, msg event.Outgoing) (string, error) {
	chat, thread, err := ParseConv(msg.Conv)
	if err != nil {
		return "", fmt.Errorf("%w: %w", err, transport.ErrPermanent)
	}
	if msg.Attach != nil {
		return "", t.sendAttachment(ctx, chat, thread, msg)
	}
	if msg.Ack != "" {
		return "", sendError(t.ack(ctx, msg.Ack, msg.Text))
	}
	if t.sendsRich(msg) {
		return t.sendRich(ctx, chat, thread, msg)
	}
	// An edit is never split, so one too long for a message changes only the
	// buttons: a long question was split when it went out, and this reference
	// holds its last part.
	if msg.Edit != "" && (msg.Text == "" || editUnits(msg) > maxTextUnits) {
		return msg.Edit, sendError(t.editButtons(ctx, chat, msg.Edit, msg.Buttons))
	}
	text, ents := visible(msg.Text)
	if msg.Signature != "" {
		text, ents = withSignature(text, ents, msg.Signature)
	}
	if msg.Superseded {
		ents = collapsed(text, ents)
	}
	if msg.Folded != "" {
		text, ents = withFolded(text, ents, msg.Folded)
	}
	if msg.Edit != "" {
		return msg.Edit, sendError(t.edit(ctx, chat, msg.Edit, text, ents, msg.Buttons))
	}
	if textUnits([]rune(text)) > maxTextUnits {
		return t.sendChunks(ctx, chat, thread, msg) // one row of a table can be too long alone
	}
	return t.sendText(ctx, chat, thread, msg, text, ents)
}

// sendText sends rendered text as a new message.
func (t *Transport) sendText(ctx context.Context, chat, thread int64, msg event.Outgoing, text string, ents []entity) (string, error) {
	m, err := t.c.sendMessage(ctx, sendParams{
		ChatID:              chat,
		MessageThreadID:     thread,
		Text:                text,
		Entities:            ents,
		DisableNotification: msg.Silent,
		ReplyParameters:     t.replyTo(chat, msg.ReplyTo),
		ReplyMarkup:         keyboard(msg.Buttons),
		LinkPreviewOptions:  previewOptions(msg),
	})
	if err != nil {
		return "", sendError(err)
	}
	return strconv.FormatInt(m.MessageID, 10), nil
}

// previewOptions turn the link preview off for a NoPreview message (R-RUN-16);
// nil leaves Telegram's default.
func previewOptions(msg event.Outgoing) *linkPreviewOptions {
	if !msg.NoPreview {
		return nil
	}
	return &linkPreviewOptions{IsDisabled: true}
}

// visible renders Markdown, or gives its source when nothing visible is left,
// rather than nothing.
func visible(src string) (string, []entity) {
	text, ents := render(src)
	if strings.TrimSpace(text) == "" {
		return src, nil
	}
	return text, ents
}

// richText is what the agent reads of a rich message that came to the bot. A person
// does not write these: they come from a bot, by a forward or through an inline bot
// with no forward's origin. So any message with one is outside content, whether its
// text is filled in or not (messageEvent, R-DLV-12).
func (t *Transport) richText(raw json.RawMessage) string {
	text := readRich(raw)
	if text == "" {
		t.richCount.add(context.Background(), richUnreadable)
		return richNothingReadable
	}
	t.richCount.add(context.Background(), richRead)
	return text
}

// sendError maps a Bot API error of a send to the sender's terms.
func sendError(err error) error {
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
	if isGroup(m.Chat) {
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
// answers a Telegram message or goes on after a press, nothing for other
// events.
func (t *Transport) TypingHook(ctx context.Context, e event.Event) (stop func()) {
	if e.Source != Name || (e.Kind != event.KindMessage && e.Kind != event.KindDecision) {
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
