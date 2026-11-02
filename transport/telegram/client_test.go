package telegram

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

const testToken = "7001:AAH-secret_token_value_0123456789abcdef"

// answer returns a server that answers every request with code and body
// and records the last request path and body.
func answer(t *testing.T, code int, body string) (*httptest.Server, *string, *string) {
	t.Helper()
	var path, reqBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		path, reqBody = r.URL.Path, string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, &path, &reqBody
}

func TestCallDecodesOK(t *testing.T) {
	srv, path, body := answer(t, 200, `{"ok":true,"result":{"id":7001,"is_bot":true,"first_name":"Test","username":"test_bot"}}`)
	c := newClient(srv.URL, testToken, nil)
	u, err := c.getMe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != 7001 || !u.IsBot || u.Username != "test_bot" {
		t.Fatalf("getMe = %+v", u)
	}
	if *path != "/bot"+testToken+"/getMe" || *body != "{}" {
		t.Fatalf("request %s %s", *path, *body)
	}
}

func TestCallMapsAPIError(t *testing.T) {
	srv, _, _ := answer(t, 400, `{"ok":false,"error_code":400,"description":"Bad Request: chat not found"}`)
	_, err := newClient(srv.URL, testToken, nil).sendMessage(context.Background(), sendParams{ChatID: 1, Text: "x"})
	var ae *apiError
	if !errors.As(err, &ae) {
		t.Fatalf("error %v (%T), want *apiError", err, err)
	}
	if ae.Code != 400 || ae.Method != "sendMessage" || ae.Description != "Bad Request: chat not found" || ae.RetryAfter != 0 {
		t.Fatalf("apiError = %+v", ae)
	}
	if got, want := err.Error(), "telegram: sendMessage: 400 Bad Request: chat not found"; got != want {
		t.Fatalf("error text %q, want %q", got, want)
	}
}

func TestCallReadsRetryAfter(t *testing.T) {
	srv, _, _ := answer(t, 429, `{"ok":false,"error_code":429,"description":"Too Many Requests: retry after 7","parameters":{"retry_after":7}}`)
	_, err := newClient(srv.URL, testToken, nil).sendMessage(context.Background(), sendParams{ChatID: 1, Text: "x"})
	var ae *apiError
	if !errors.As(err, &ae) || ae.Code != 429 || ae.RetryAfter != 7*time.Second {
		t.Fatalf("error %v, want 429 with retry after 7 s", err)
	}
}

func TestCallNonJSONErrorUsesHTTPStatus(t *testing.T) {
	srv, _, _ := answer(t, 502, `<html>Bad Gateway</html>`)
	_, err := newClient(srv.URL, testToken, nil).getMe(context.Background())
	var ae *apiError
	if !errors.As(err, &ae) || ae.Code != 502 {
		t.Fatalf("error %v, want apiError 502", err)
	}
}

func TestSendParamsEncoding(t *testing.T) {
	srv, _, body := answer(t, 200, `{"ok":true,"result":{"message_id":5,"chat":{"id":-100,"type":"supergroup"}}}`)
	c := newClient(srv.URL, testToken, nil)
	if _, err := c.sendMessage(context.Background(), sendParams{ChatID: -100, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	if want := `{"chat_id":-100,"text":"hi"}`; *body != want {
		t.Fatalf("plain body %s, want %s", *body, want)
	}
	if _, err := c.sendMessage(context.Background(), sendParams{ChatID: -100, MessageThreadID: 45, Text: "hi", DisableNotification: true}); err != nil {
		t.Fatal(err)
	}
	if want := `{"chat_id":-100,"message_thread_id":45,"text":"hi","disable_notification":true}`; *body != want {
		t.Fatalf("topic body %s, want %s", *body, want)
	}
}

func TestErrorsNeverContainToken(t *testing.T) {
	ctx := context.Background()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	srv500, _, _ := answer(t, 500, `internal error`)
	srvOK, _, _ := answer(t, 200, `{"ok":true,"result":{}}`)
	// A server that reflects the request path in the description.
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = io.WriteString(w, `{"ok":false,"error_code":400,"description":"bad path `+r.URL.Path+`"}`)
	}))
	t.Cleanup(echo.Close)

	cases := []struct {
		name string
		ctx  context.Context
		base string
	}{
		{"unreachable", ctx, "http://127.0.0.1:1"},
		{"500 with a non-JSON body", ctx, srv500.URL},
		{"cancelled context", cancelled, srvOK.URL},
		{"description echoes the path", ctx, echo.URL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := newClient(tc.base, testToken, nil).getMe(tc.ctx)
			if err == nil {
				t.Fatal("no error")
			}
			for e := err; e != nil; e = errors.Unwrap(e) {
				if strings.Contains(e.Error(), "secret_token") {
					t.Fatalf("error chain leaks the token: %q", e.Error())
				}
			}
		})
	}
	_, err := newClient(srvOK.URL, testToken, nil).getMe(cancelled)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled call: %v, want errors.Is context.Canceled", err)
	}
}

func TestClientAgainstFake(t *testing.T) {
	ctx := context.Background()
	fake := telegramtest.NewServer(t, testToken)
	c := newClient(fake.URL(), testToken, nil)

	me, err := c.getMe(ctx)
	if err != nil || me.ID != 7001 || !me.IsBot {
		t.Fatalf("getMe = %+v, %v", me, err)
	}
	id := fake.AddMessage(telegramtest.Incoming{ChatID: -100, ThreadID: 45, UserID: 42, Name: "Igor", Text: "hello"})
	ups, err := c.getUpdates(ctx, 0, 0)
	if err != nil || len(ups) != 1 {
		t.Fatalf("getUpdates = %+v, %v", ups, err)
	}
	m := ups[0].Message
	if ups[0].UpdateID != id || m == nil || m.Text != "hello" || m.Chat.ID != -100 || !m.Chat.IsForum ||
		m.MessageThreadID != 45 || m.From == nil || m.From.ID != 42 || m.From.FirstName != "Igor" {
		t.Fatalf("update = %+v / %+v", ups[0], m)
	}
	if _, err := c.sendMessage(ctx, sendParams{ChatID: -100, MessageThreadID: 45, Text: "hi", DisableNotification: true}); err != nil {
		t.Fatal(err)
	}
	if sent := fake.Sent(); len(sent) != 1 || sent[0] != (telegramtest.Outgoing{ChatID: -100, ThreadID: 45, Text: "hi", Silent: true}) {
		t.Fatalf("fake got %+v", sent)
	}

	_, err = newClient(fake.URL(), "7001:wrong", nil).getMe(ctx)
	var ae *apiError
	if !errors.As(err, &ae) || ae.Code != 401 {
		t.Fatalf("wrong token: %v, want 401", err)
	}
}
