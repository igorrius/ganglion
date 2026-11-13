package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/internal/buckets"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// DefaultAPIURL is the Bot API server.
const DefaultAPIURL = "https://api.telegram.org"

// pollTimeout is how long a getUpdates call is held open by Telegram.
const pollTimeout = 50 * time.Second

// httpTimeout bounds every request: the poll timeout plus slack, so a long
// poll is never cut short by the client.
const httpTimeout = pollTimeout + 10*time.Second

// maxResponse bounds how much of a response body is read.
const maxResponse = 10 << 20

// client is a minimal Bot API client. Every error it returns is built
// here, with the token replaced by "<token>".
type client struct {
	base  string
	token string
	hc    *http.Client

	tracer trace.Tracer
	dur    metric.Float64Histogram
}

// newClient returns a client for base (DefaultAPIURL when empty). A nil hc
// gets an http.Client with httpTimeout.
func newClient(base, token string, hc *http.Client) *client {
	if base == "" {
		base = DefaultAPIURL
	}
	if hc == nil {
		hc = &http.Client{Timeout: httpTimeout}
	}
	c := &client{base: strings.TrimRight(base, "/"), token: token, hc: hc, tracer: otel.Tracer("ganglion/telegram")}
	c.dur, _ = otel.Meter("ganglion/telegram").Float64Histogram("ganglion.telegram.api.duration", metric.WithUnit("s"), buckets.Seconds,
		metric.WithDescription("Bot API call duration, by method and HTTP status (\"error\" when unreachable)."))
	return c
}

// apiError is an error answer of the Bot API. The transport maps it to
// transport errors.
type apiError struct {
	Method      string
	Code        int
	Description string
	RetryAfter  time.Duration // from parameters.retry_after; 0 if absent
}

func (e *apiError) Error() string {
	return fmt.Sprintf("telegram: %s: %d %s", e.Method, e.Code, e.Description)
}

// callError is a failure to reach the Bot API or to read its answer. Its
// text is redacted; it unwraps only to the context errors, never to the
// original error, whose text may hold the request URL and so the token.
type callError struct {
	method string
	msg    string
	cause  error // context.Canceled, context.DeadlineExceeded or nil
}

func (e *callError) Error() string { return "telegram: " + e.method + ": " + e.msg }
func (e *callError) Unwrap() error { return e.cause }

func (c *client) fail(method string, err error) error {
	ce := &callError{method: method, msg: c.redact(err.Error())}
	switch {
	case errors.Is(err, context.Canceled):
		ce.cause = context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		ce.cause = context.DeadlineExceeded
	}
	return ce
}

func (c *client) redact(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "<token>")
}

// envelope is the Bot API answer around every result.
type envelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  *struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// call POSTs params as JSON to method and decodes the result into result
// (skipped when result is nil).
func (c *client) call(ctx context.Context, method string, params, result any) error {
	body := []byte("{}")
	if params != nil {
		var err error
		if body, err = json.Marshal(params); err != nil {
			return c.fail(method, err)
		}
	}
	return c.do(ctx, method, "application/json", body, result)
}

// do posts body of type ctype to method, traced and timed, and decodes
// the answer into result.
func (c *client) do(ctx context.Context, method, ctype string, body []byte, result any) (err error) {
	ctx, span := c.tracer.Start(ctx, "telegram "+method, trace.WithSpanKind(trace.SpanKindClient))
	started, code := time.Now(), "error"
	defer func() {
		c.dur.Record(ctx, time.Since(started).Seconds(), metric.WithAttributes(attribute.String("method", method), attribute.String("code", code)))
		span.SetAttributes(attribute.String("http.status_code", code))
		if err != nil {
			span.SetStatus(codes.Error, code)
		}
		span.End()
	}()
	status, raw, err := c.post(ctx, method, ctype, body)
	if status != 0 {
		code = strconv.Itoa(status)
	}
	if err != nil {
		return err
	}
	return c.decode(method, status, raw, result)
}

// post sends body to method and returns the HTTP status and the answer.
func (c *client) post(ctx context.Context, method, ctype string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return 0, nil, c.fail(method, err)
	}
	req.Header.Set("Content-Type", ctype)
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, nil, c.fail(method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return resp.StatusCode, nil, c.fail(method, err)
	}
	return resp.StatusCode, raw, nil
}

// decode reads the Bot API envelope: an *apiError when it is not ok,
// otherwise the result into result (skipped when result is nil).
func (c *client) decode(method string, status int, raw []byte, result any) error {
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		if status != http.StatusOK {
			return &apiError{Method: method, Code: status, Description: http.StatusText(status)}
		}
		return c.fail(method, fmt.Errorf("decode answer: %w", err))
	}
	if !env.OK {
		e := &apiError{Method: method, Code: env.ErrorCode, Description: c.redact(env.Description)}
		if e.Code == 0 {
			e.Code = status
		}
		if env.Parameters != nil && env.Parameters.RetryAfter > 0 {
			e.RetryAfter = time.Duration(env.Parameters.RetryAfter) * time.Second
		}
		return e
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(env.Result, result); err != nil {
		return c.fail(method, fmt.Errorf("decode result: %w", err))
	}
	return nil
}

// getMe returns the bot's own user.
func (c *client) getMe(ctx context.Context) (user, error) {
	var u user
	err := c.call(ctx, "getMe", nil, &u)
	return u, err
}

// getUpdates confirms every update below offset and returns the next
// messages, button presses and changes of the bot's own membership, waiting up to timeout for one to arrive.
func (c *client) getUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]update, error) {
	params := struct {
		Offset         int64    `json:"offset,omitempty"`
		Timeout        int      `json:"timeout"`
		AllowedUpdates []string `json:"allowed_updates"`
	}{offset, int(timeout / time.Second), []string{"message", "callback_query", "my_chat_member"}}
	var ups []update
	err := c.call(ctx, "getUpdates", params, &ups)
	return ups, err
}

// sendChatAction shows an action such as "typing" in a chat for ~5 s.
func (c *client) sendChatAction(ctx context.Context, chatID, threadID int64, action string) error {
	return c.call(ctx, "sendChatAction", struct {
		ChatID          int64  `json:"chat_id"`
		MessageThreadID int64  `json:"message_thread_id,omitempty"`
		Action          string `json:"action"`
	}{chatID, threadID, action}, nil)
}

// sendMessage sends a text message.
func (c *client) sendMessage(ctx context.Context, p sendParams) (message, error) {
	var m message
	err := c.call(ctx, "sendMessage", p, &m)
	return m, err
}

// sendRichMessage sends a rich message.
func (c *client) sendRichMessage(ctx context.Context, p richParams) (message, error) {
	var m message
	err := c.call(ctx, "sendRichMessage", p, &m)
	return m, err
}

// editMessageText changes the text and the buttons of a message the bot
// sent.
func (c *client) editMessageText(ctx context.Context, p editParams) error {
	return c.call(ctx, "editMessageText", p, nil)
}

// editMessageReplyMarkup changes only the buttons of a message the bot
// sent.
func (c *client) editMessageReplyMarkup(ctx context.Context, p markupParams) error {
	return c.call(ctx, "editMessageReplyMarkup", p, nil)
}

// setMessageReaction sets the bot's reaction on a message, replacing the one it
// had; "" takes it off.
func (c *client) setMessageReaction(ctx context.Context, chat, msg int64, emoji string) error {
	type reaction struct {
		Type  string `json:"type"`
		Emoji string `json:"emoji"`
	}
	list := []reaction{}
	if emoji != "" {
		list = append(list, reaction{Type: "emoji", Emoji: emoji})
	}
	return c.call(ctx, "setMessageReaction", struct {
		ChatID    int64      `json:"chat_id"`
		MessageID int64      `json:"message_id"`
		Reaction  []reaction `json:"reaction"`
	}{chat, msg, list}, nil)
}

// answerCallbackQuery tells Telegram a button press was received, so the
// client stops its spinner, and shows text as a toast ("" shows none).
func (c *client) answerCallbackQuery(ctx context.Context, id, text string) error {
	return c.call(ctx, "answerCallbackQuery", struct {
		CallbackQueryID string `json:"callback_query_id"`
		Text            string `json:"text,omitempty"`
	}{id, text}, nil)
}

// setMyCommands sets the bot's command hints for clients in lang ("" for
// every language without its own list).
func (c *client) setMyCommands(ctx context.Context, cmds []botCommand, lang string) error {
	return c.call(ctx, "setMyCommands", struct {
		Commands     []botCommand `json:"commands"`
		LanguageCode string       `json:"language_code,omitempty"`
	}{cmds, lang}, nil)
}
