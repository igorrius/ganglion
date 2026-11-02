package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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
	return &client{base: strings.TrimRight(base, "/"), token: token, hc: hc}
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+"/bot"+c.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return c.fail(method, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return c.fail(method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return c.fail(method, err)
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		if resp.StatusCode != http.StatusOK {
			return &apiError{Method: method, Code: resp.StatusCode, Description: http.StatusText(resp.StatusCode)}
		}
		return c.fail(method, fmt.Errorf("decode answer: %w", err))
	}
	if !env.OK {
		e := &apiError{Method: method, Code: env.ErrorCode, Description: c.redact(env.Description)}
		if e.Code == 0 {
			e.Code = resp.StatusCode
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
// messages, waiting up to timeout for one to arrive.
func (c *client) getUpdates(ctx context.Context, offset int64, timeout time.Duration) ([]update, error) {
	params := struct {
		Offset         int64    `json:"offset,omitempty"`
		Timeout        int      `json:"timeout"`
		AllowedUpdates []string `json:"allowed_updates"`
	}{offset, int(timeout / time.Second), []string{"message"}}
	var ups []update
	err := c.call(ctx, "getUpdates", params, &ups)
	return ups, err
}

// sendMessage sends a text message.
func (c *client) sendMessage(ctx context.Context, p sendParams) (message, error) {
	var m message
	err := c.call(ctx, "sendMessage", p, &m)
	return m, err
}
