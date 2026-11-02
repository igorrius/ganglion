package telegramtest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

const token = "7001:test-secret"

type answer struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func post(t *testing.T, s *Server, tok, method string, params any) answer {
	t.Helper()
	body, _ := json.Marshal(params)
	resp, err := http.Post(s.URL()+"/bot"+tok+"/"+method, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Error(err)
		return answer{}
	}
	defer func() { _ = resp.Body.Close() }()
	var a answer
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		t.Error(err)
	}
	if !a.OK && resp.StatusCode != a.ErrorCode {
		t.Errorf("HTTP status %d, error_code %d", resp.StatusCode, a.ErrorCode)
	}
	return a
}

type poll struct {
	Offset  int64 `json:"offset"`
	Timeout int   `json:"timeout"`
}

func updateIDs(t *testing.T, a answer) []int64 {
	t.Helper()
	if !a.OK {
		t.Fatalf("getUpdates failed: %d %s", a.ErrorCode, a.Description)
	}
	var ups []struct {
		UpdateID int64 `json:"update_id"`
	}
	if err := json.Unmarshal(a.Result, &ups); err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, u := range ups {
		ids = append(ids, u.UpdateID)
	}
	return ids
}

func TestFakeOffsetConfirms(t *testing.T) {
	s := NewServer(t, token)
	id1 := s.AddMessage(Incoming{ChatID: 1, UserID: 1, Text: "a"})
	id2 := s.AddMessage(Incoming{ChatID: 1, UserID: 1, Text: "b"})
	if ids := updateIDs(t, post(t, s, token, "getUpdates", poll{})); len(ids) != 2 || ids[0] != id1 || ids[1] != id2 {
		t.Fatalf("first poll = %v", ids)
	}
	if s.Confirmed() != 0 {
		t.Fatalf("confirmed %d before any offset", s.Confirmed())
	}
	// Without an offset the same updates come back.
	if ids := updateIDs(t, post(t, s, token, "getUpdates", poll{})); len(ids) != 2 {
		t.Fatalf("unconfirmed updates not redelivered: %v", ids)
	}
	if ids := updateIDs(t, post(t, s, token, "getUpdates", poll{Offset: id1 + 1})); len(ids) != 1 || ids[0] != id2 {
		t.Fatalf("after offset id1+1: %v", ids)
	}
	if s.Confirmed() != id1 {
		t.Fatalf("confirmed = %d, want %d", s.Confirmed(), id1)
	}
	if ids := updateIDs(t, post(t, s, token, "getUpdates", poll{Offset: id2 + 1})); len(ids) != 0 {
		t.Fatalf("after offset id2+1: %v", ids)
	}
	if s.Confirmed() != id2 {
		t.Fatalf("confirmed = %d, want %d", s.Confirmed(), id2)
	}
}

func TestFakeLongPollWakesOnMessage(t *testing.T) {
	s := NewServer(t, token)
	got := make(chan answer, 1)
	start := time.Now()
	go func() { got <- post(t, s, token, "getUpdates", poll{Timeout: 10}) }()
	waitPolling(t, s)
	id := s.AddMessage(Incoming{ChatID: 1, UserID: 1, Text: "a"})
	a := <-got
	if ids := updateIDs(t, a); len(ids) != 1 || ids[0] != id {
		t.Fatalf("woken poll = %v", ids)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("long poll did not wake on the message")
	}
}

func TestFakeLongPollTimesOutEmpty(t *testing.T) {
	s := NewServer(t, token)
	start := time.Now()
	if ids := updateIDs(t, post(t, s, token, "getUpdates", poll{Timeout: 1})); len(ids) != 0 {
		t.Fatalf("got %v", ids)
	}
	if d := time.Since(start); d < 900*time.Millisecond {
		t.Fatalf("returned after %v, want about 1 s", d)
	}
}

func TestFakeSecondPollerConflicts(t *testing.T) {
	s := NewServer(t, token)
	got := make(chan answer, 1)
	go func() { got <- post(t, s, token, "getUpdates", poll{Timeout: 10}) }()
	waitPolling(t, s)
	second := make(chan answer, 1)
	go func() { second <- post(t, s, token, "getUpdates", poll{Timeout: 1}) }()

	a := <-got
	if a.OK || a.ErrorCode != 409 || a.Description != "Conflict: terminated by other getUpdates request; make sure that only one bot instance is running" {
		t.Fatalf("first poller got %+v, want 409", a)
	}
	if b := <-second; !b.OK {
		t.Fatalf("second poller got %+v", b)
	}
}

func TestFakeWebhookConflicts(t *testing.T) {
	s := NewServer(t, token)
	s.SetWebhook(true)
	if a := post(t, s, token, "getUpdates", poll{}); a.OK || a.ErrorCode != 409 {
		t.Fatalf("with webhook: %+v, want 409", a)
	}
	s.SetWebhook(false)
	if a := post(t, s, token, "getUpdates", poll{}); !a.OK {
		t.Fatalf("after webhook off: %+v", a)
	}
}

func TestFakeWrongTokenAndMethod(t *testing.T) {
	s := NewServer(t, token)
	if a := post(t, s, "7001:wrong", "getMe", nil); a.ErrorCode != 401 {
		t.Fatalf("wrong token: %+v, want 401", a)
	}
	if a := post(t, s, token, "sendSticker", nil); a.ErrorCode != 404 {
		t.Fatalf("unknown method: %+v, want 404", a)
	}
}

func TestFakeFailNext(t *testing.T) {
	s := NewServer(t, token)
	s.FailNext("sendMessage", 429, 7)
	send := map[string]any{"chat_id": 1, "text": "x"}
	a := post(t, s, token, "sendMessage", send)
	if a.OK || a.ErrorCode != 429 || a.Parameters.RetryAfter != 7 {
		t.Fatalf("first send: %+v, want 429 retry_after 7", a)
	}
	if a := post(t, s, token, "sendMessage", send); !a.OK {
		t.Fatalf("second send: %+v", a)
	}
	if len(s.Sent()) != 1 {
		t.Fatalf("sent %d, want 1: a failed send is not delivered", len(s.Sent()))
	}
}

func TestFakeSendChecks(t *testing.T) {
	s := NewServer(t, token)
	s.AddMessage(Incoming{ChatID: -100, ThreadID: 45, UserID: 1, Text: "a"})
	cases := []struct {
		name   string
		params map[string]any
		code   int
		desc   string
	}{
		{"known thread", map[string]any{"chat_id": -100, "message_thread_id": 45, "text": "x"}, 0, ""},
		{"unknown thread", map[string]any{"chat_id": -100, "message_thread_id": 46, "text": "x"}, 400, "Bad Request: message thread not found"},
		{"empty text", map[string]any{"chat_id": -100, "text": ""}, 400, "Bad Request: message text is empty"},
		{"too long", map[string]any{"chat_id": -100, "text": string(bytes.Repeat([]byte("я"), MaxTextLen+1))}, 400, "Bad Request: message is too long"},
	}
	for _, tc := range cases {
		a := post(t, s, token, "sendMessage", tc.params)
		if tc.code == 0 && !a.OK || tc.code != 0 && (a.ErrorCode != tc.code || a.Description != tc.desc) {
			t.Errorf("%s: %+v", tc.name, a)
		}
	}
	if a := post(t, s, token, "sendMessage", map[string]any{"chat_id": -100, "text": string(bytes.Repeat([]byte("я"), MaxTextLen))}); !a.OK {
		t.Errorf("exactly %d characters rejected: %+v", MaxTextLen, a)
	}
}

// waitPolling waits until a getUpdates call is held open.
func waitPolling(t *testing.T, s *Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		on := s.kick != nil
		s.mu.Unlock()
		if on {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no poll in flight")
}
