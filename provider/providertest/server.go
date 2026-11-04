// Package providertest is a fake OpenAI-compatible Chat Completions API for
// tests: scripted answers, tool calls, failures and hangs, and a record of
// every request.
package providertest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
)

// Request is one recorded call.
type Request struct {
	Model     string           `json:"model"`
	Messages  []map[string]any `json:"messages"`
	Tools     []map[string]any `json:"tools"`
	MaxTokens int              `json:"max_tokens"`
}

type step struct {
	text       string
	toolName   string
	toolArgs   string
	status     int
	retryAfter int
	hang       bool
}

// Server is the fake API.
type Server struct {
	ts     *httptest.Server
	key    string
	closed chan struct{}

	mu      sync.Mutex
	queue   []step
	respond Responder
	reqs    []Request
	calls   int
	tools   int // tool calls answered so far: ids are call_1, call_2, …
}

// T is what the fake needs from its owner: a test, or chatlab's program,
// which cannot implement testing.TB (ADR-0048).
type T interface {
	Helper()
	Cleanup(func())
}

// NewServer starts a fake API that accepts apiKey; it is closed by
// t.Cleanup. Every answer reports 10 prompt tokens (4 of them cached) and 5
// completion tokens.
func NewServer(t T, apiKey string) *Server {
	t.Helper()
	s := &Server{key: apiKey, closed: make(chan struct{})}
	s.ts = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(func() {
		close(s.closed)
		s.ts.Close()
	})
	return s
}

// URL is the base URL for the client (it ends in /v1).
func (s *Server) URL() string { return s.ts.URL + "/v1" }

func (s *Server) push(st step) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, st)
}

// Reply queues an answer with text.
func (s *Server) Reply(text string) { s.push(step{text: text}) }

// CallTool queues an answer with one tool call; args is its JSON.
func (s *Server) CallTool(name, args string) { s.push(step{toolName: name, toolArgs: args}) }

// Answer is what a Responder gives: a tool call when Tool is set, the
// text otherwise.
type Answer struct {
	Text       string
	Tool, Args string
}

// Responder answers a request when nothing is queued, so a test can drive
// any number of turns by what each request holds (a tool result to reply
// to, or a person's message).
type Responder func(Request) Answer

// Respond sets how requests are answered when nothing is queued; without
// it they get "ok".
func (s *Server) Respond(f Responder) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.respond = f
}

// Fail queues a failure with status; retryAfter > 0 sets Retry-After.
func (s *Server) Fail(status, retryAfter int) { s.push(step{status: status, retryAfter: retryAfter}) }

// Hang queues a call that blocks until its request is cancelled.
func (s *Server) Hang() { s.push(step{hang: true}) }

// Requests returns the calls received so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.reqs...)
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/chat/completions" {
		writeErr(w, http.StatusNotFound, "not found", 0)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+s.key {
		writeErr(w, http.StatusUnauthorized, "Incorrect API key provided", 0)
		return
	}
	var req Request
	_ = json.NewDecoder(r.Body).Decode(&req)
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	st := step{text: "ok"}
	switch {
	case len(s.queue) > 0:
		st, s.queue = s.queue[0], s.queue[1:]
	case s.respond != nil:
		a := s.respond(req)
		st = step{text: a.Text, toolName: a.Tool, toolArgs: a.Args}
	}
	s.calls++
	n := s.calls
	if st.toolName != "" {
		s.tools++
	}
	toolN := s.tools
	s.mu.Unlock()

	switch {
	case st.hang:
		select {
		case <-r.Context().Done():
		case <-s.closed:
		}
		return
	case st.status != 0:
		writeErr(w, st.status, http.StatusText(st.status), st.retryAfter)
		return
	}
	msg := map[string]any{"role": "assistant", "content": st.text}
	finish := "stop"
	if st.toolName != "" {
		msg["content"] = nil
		msg["tool_calls"] = []any{map[string]any{
			"id": fmt.Sprintf("call_%d", toolN), "type": "function",
			"function": map[string]any{"name": st.toolName, "arguments": st.toolArgs},
		}}
		finish = "tool_calls"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":      fmt.Sprintf("chatcmpl-%d", n),
		"object":  "chat.completion",
		"model":   req.Model,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
		"usage": map[string]any{
			"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15,
			"prompt_tokens_details": map[string]any{"cached_tokens": 4},
		},
	})
}

func writeErr(w http.ResponseWriter, status int, msg string, retryAfter int) {
	w.Header().Set("Content-Type", "application/json")
	if retryAfter > 0 {
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	}
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "code": status}})
}
