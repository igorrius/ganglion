// Package telegramtest is a fake Telegram Bot API for tests and the crash
// harness. It keeps the semantics our transport relies on: an offset
// confirms updates, long polling wakes on a new message, a second poller
// ends the first with 409, a webhook blocks polling with 409, and errors
// carry error_code, description and retry_after like the real API.
package telegramtest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
)

// MaxTextLen is the longest message text sendMessage accepts, in UTF-16
// code units, as Telegram counts it.
const MaxTextLen = 4096

// maxUpdates is how many updates one getUpdates call returns at most.
const maxUpdates = 100

// Incoming is a message a person sends to the bot.
type Incoming struct {
	ChatID   int64 // negative for a group
	ThreadID int64 // forum topic; 0 for none
	UserID   int64
	Name     string
	Text     string
}

// Outgoing is a message the bot sent.
type Outgoing struct {
	ChatID   int64
	ThreadID int64
	Text     string
	Silent   bool
}

type failure struct {
	code       int
	retryAfter int
}

// Server is a fake Bot API on an httptest.Server.
type Server struct {
	ts     *httptest.Server
	token  string
	botID  int64
	closed chan struct{}

	mu         sync.Mutex
	updates    []map[string]any // pending, in update_id order
	nextUpdate int64
	nextMsg    int64
	confirmed  int64
	sent       []Outgoing
	fail       map[string]failure
	webhook    bool
	kick       chan struct{}            // closed to end the getUpdates call in flight
	notify     chan struct{}            // closed and replaced when a message arrives
	threads    map[int64]map[int64]bool // chat → known forum topics
}

// NewServer starts a fake Bot API for token (<bot_id>:<secret>). It is
// closed by t.Cleanup.
func NewServer(t testing.TB, token string) *Server {
	t.Helper()
	prefix, _, _ := strings.Cut(token, ":")
	id, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil {
		t.Fatalf("telegramtest: token %q has no numeric bot id", prefix)
	}
	s := &Server{
		token:      token,
		botID:      id,
		closed:     make(chan struct{}),
		nextUpdate: 100000,
		fail:       map[string]failure{},
		notify:     make(chan struct{}),
		threads:    map[int64]map[int64]bool{},
	}
	s.ts = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(func() {
		close(s.closed) // releases held polls, so Close does not wait for them
		s.ts.Close()
	})
	return s
}

// URL is the base URL to give the client instead of https://api.telegram.org.
func (s *Server) URL() string { return s.ts.URL }

// AddMessage queues a message to the bot and returns its update_id.
func (s *Server) AddMessage(m Incoming) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextUpdate++
	s.nextMsg++
	chatType := "private"
	if m.ChatID < 0 {
		chatType = "supergroup"
	}
	name := m.Name
	if name == "" {
		name = "User"
	}
	msg := map[string]any{
		"message_id": s.nextMsg,
		"date":       time.Now().Unix(),
		"chat":       map[string]any{"id": m.ChatID, "type": chatType, "is_forum": m.ThreadID != 0},
		"from":       map[string]any{"id": m.UserID, "is_bot": false, "first_name": name},
		"text":       m.Text,
	}
	if m.ThreadID != 0 {
		msg["message_thread_id"] = m.ThreadID
		msg["is_topic_message"] = true
		if s.threads[m.ChatID] == nil {
			s.threads[m.ChatID] = map[int64]bool{}
		}
		s.threads[m.ChatID][m.ThreadID] = true
	}
	s.updates = append(s.updates, map[string]any{"update_id": s.nextUpdate, "message": msg})
	close(s.notify)
	s.notify = make(chan struct{})
	return s.nextUpdate
}

// Sent returns the messages delivered by sendMessage, in order.
func (s *Server) Sent() []Outgoing {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Outgoing(nil), s.sent...)
}

// Confirmed returns the highest update_id confirmed by an offset, or 0.
func (s *Server) Confirmed() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.confirmed
}

// FailNext makes the next call of method fail with code; retryAfter > 0
// adds parameters.retry_after (use it with 429).
func (s *Server) FailNext(method string, code, retryAfter int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[method] = failure{code: code, retryAfter: retryAfter}
}

// SetWebhook turns a webhook on or off; while it is on, getUpdates
// returns 409.
func (s *Server) SetWebhook(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.webhook = on
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/bot")
	i := strings.LastIndex(rest, "/")
	if !ok || i < 0 {
		writeErr(w, http.StatusNotFound, "Not Found", 0)
		return
	}
	token, method := rest[:i], rest[i+1:]
	if token != s.token {
		writeErr(w, http.StatusUnauthorized, "Unauthorized", 0)
		return
	}
	params := map[string]json.RawMessage{}
	if err := json.NewDecoder(r.Body).Decode(&params); err != nil && err.Error() != "EOF" {
		writeErr(w, http.StatusBadRequest, "Bad Request: can't parse JSON", 0)
		return
	}

	s.mu.Lock()
	if f, ok := s.fail[method]; ok {
		delete(s.fail, method)
		s.mu.Unlock()
		desc := http.StatusText(f.code)
		if f.code == http.StatusTooManyRequests && f.retryAfter > 0 {
			desc = fmt.Sprintf("Too Many Requests: retry after %d", f.retryAfter)
		}
		writeErr(w, f.code, desc, f.retryAfter)
		return
	}
	s.mu.Unlock()

	switch method {
	case "getMe":
		writeOK(w, map[string]any{"id": s.botID, "is_bot": true, "first_name": "Test", "username": "test_bot"})
	case "getUpdates":
		s.getUpdates(w, r, params)
	case "sendMessage":
		s.sendMessage(w, params)
	default:
		writeErr(w, http.StatusNotFound, "Not Found", 0)
	}
}

func intParam(params map[string]json.RawMessage, name string) int64 {
	var v int64
	_ = json.Unmarshal(params[name], &v)
	return v
}

func (s *Server) getUpdates(w http.ResponseWriter, r *http.Request, params map[string]json.RawMessage) {
	offset := intParam(params, "offset")
	timer := time.NewTimer(time.Duration(intParam(params, "timeout")) * time.Second)
	defer timer.Stop()

	s.mu.Lock()
	if s.webhook {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict,
			"Conflict: can't use getUpdates method while webhook is active; use deleteWebhook to delete the webhook first", 0)
		return
	}
	if offset > 0 {
		kept := s.updates[:0]
		for _, u := range s.updates {
			if u["update_id"].(int64) >= offset {
				kept = append(kept, u)
			}
		}
		s.updates = kept
		s.confirmed = max(s.confirmed, offset-1)
	}
	if s.kick != nil {
		close(s.kick) // the older poller gets 409
	}
	mine := make(chan struct{})
	s.kick = mine

	for {
		select {
		case <-mine:
			s.mu.Unlock()
			writeErr(w, http.StatusConflict,
				"Conflict: terminated by other getUpdates request; make sure that only one bot instance is running", 0)
			return
		default:
		}
		if n := min(len(s.updates), maxUpdates); n > 0 {
			ups := append([]map[string]any(nil), s.updates[:n]...)
			s.kick = nil
			s.mu.Unlock()
			writeOK(w, ups)
			return
		}
		notify := s.notify
		s.mu.Unlock()
		select {
		case <-notify:
		case <-mine:
		case <-timer.C:
			s.release(mine)
			writeOK(w, []any{})
			return
		case <-r.Context().Done():
			s.release(mine)
			return
		case <-s.closed:
			s.release(mine)
			writeOK(w, []any{})
			return
		}
		s.mu.Lock()
	}
}

// release clears the in-flight poll unless a newer one replaced it.
func (s *Server) release(mine chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.kick == mine {
		s.kick = nil
	}
}

func (s *Server) sendMessage(w http.ResponseWriter, params map[string]json.RawMessage) {
	var p struct {
		ChatID              int64  `json:"chat_id"`
		MessageThreadID     int64  `json:"message_thread_id"`
		Text                string `json:"text"`
		DisableNotification bool   `json:"disable_notification"`
	}
	raw, _ := json.Marshal(params)
	if err := json.Unmarshal(raw, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "Bad Request: "+err.Error(), 0)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case p.ChatID == 0:
		writeErr(w, http.StatusBadRequest, "Bad Request: chat_id is empty", 0)
		return
	case p.MessageThreadID != 0 && !s.threads[p.ChatID][p.MessageThreadID]:
		writeErr(w, http.StatusBadRequest, "Bad Request: message thread not found", 0)
		return
	case p.Text == "":
		writeErr(w, http.StatusBadRequest, "Bad Request: message text is empty", 0)
		return
	case len(utf16.Encode([]rune(p.Text))) > MaxTextLen:
		writeErr(w, http.StatusBadRequest, "Bad Request: message is too long", 0)
		return
	}
	s.sent = append(s.sent, Outgoing{ChatID: p.ChatID, ThreadID: p.MessageThreadID, Text: p.Text, Silent: p.DisableNotification})
	s.nextMsg++
	msg := map[string]any{
		"message_id": s.nextMsg,
		"date":       time.Now().Unix(),
		"chat":       map[string]any{"id": p.ChatID, "type": "private"},
		"text":       p.Text,
	}
	if p.MessageThreadID != 0 {
		msg["message_thread_id"] = p.MessageThreadID
	}
	writeOK(w, msg)
}

func writeOK(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func writeErr(w http.ResponseWriter, code int, desc string, retryAfter int) {
	body := map[string]any{"ok": false, "error_code": code, "description": desc}
	if retryAfter > 0 {
		body["parameters"] = map[string]any{"retry_after": retryAfter}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
