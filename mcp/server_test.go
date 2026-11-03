package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/provider"
)

var tools = []provider.Tool{
	{Name: "schedule.list", Description: "List the jobs.", InputSchema: json.RawMessage(`{"type":"object","properties":{}}`)},
	{Name: "schedule.run_now", Description: "Run a job now.", InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}}}`)},
}

// session is a client talking to a served Server over pipes.
type session struct {
	in   *io.PipeWriter
	out  *bufio.Reader
	done chan error
}

func serve(t *testing.T, exec provider.Exec) *session {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &session{in: inW, out: bufio.NewReader(outR), done: make(chan error, 1)}
	srv := NewServer("ganglion", "1.2.3", tools, exec)
	go func() {
		err := srv.Serve(context.Background(), struct {
			io.Reader
			io.Writer
		}{inR, outW})
		_ = outW.Close()
		s.done <- err
	}()
	t.Cleanup(func() {
		_ = inW.Close()
		go func() { _, _ = io.Copy(io.Discard, outR) }()
		select {
		case err := <-s.done:
			_ = err // the tests that care check Serve's error themselves
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after the input closed")
		}
	})
	return s
}

func (s *session) send(t *testing.T, line string) {
	t.Helper()
	if _, err := io.WriteString(s.in, line+"\n"); err != nil {
		t.Fatal(err)
	}
}

type reply struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (s *session) recv(t *testing.T) reply {
	t.Helper()
	line, err := s.out.ReadString('\n')
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	var r reply
	if err := json.Unmarshal([]byte(line), &r); err != nil {
		t.Fatalf("reply %q: %v", line, err)
	}
	if r.JSONRPC != "2.0" {
		t.Fatalf("reply %q is not JSON-RPC 2.0", line)
	}
	return r
}

func noExec(context.Context, provider.ToolCall) (string, error) {
	return "", errors.New("no tools here")
}

func TestServerInitializeAndList(t *testing.T) {
	s := serve(t, noExec)
	s.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"claude-code","version":"2"}}}`)
	r := s.recv(t)
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
		Capabilities    struct {
			Tools *struct{} `json:"tools"`
		} `json:"capabilities"`
		ServerInfo struct{ Name, Version string } `json:"serverInfo"`
	}
	if err := json.Unmarshal(r.Result, &init); err != nil || string(r.ID) != "1" {
		t.Fatalf("initialize: %s %v", r.Result, err)
	}
	if init.ProtocolVersion != "2025-06-18" || init.Capabilities.Tools == nil || init.ServerInfo.Name != "ganglion" || init.ServerInfo.Version != "1.2.3" {
		t.Fatalf("initialize result %+v", init)
	}

	// A notification gets no answer: the next line answers the ping.
	s.send(t, `{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	s.send(t, `{"jsonrpc":"2.0","id":"p","method":"ping"}`)
	if r := s.recv(t); string(r.ID) != `"p"` || string(r.Result) != "{}" {
		t.Fatalf("ping: %+v %s", r, r.Result)
	}

	s.send(t, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	// The wire keys are MCP's, checked raw rather than through provider.Tool.
	var list struct {
		Tools []map[string]json.RawMessage `json:"tools"`
	}
	if err := json.Unmarshal(s.recv(t).Result, &list); err != nil {
		t.Fatal(err)
	}
	want := []map[string]string{
		{"name": `"schedule_list"`, "description": `"List the jobs."`, "inputSchema": string(tools[0].InputSchema)},
		{"name": `"schedule_run_now"`, "description": `"Run a job now."`, "inputSchema": string(tools[1].InputSchema)},
	}
	if len(list.Tools) != len(want) {
		t.Fatalf("tools/list has %d tools", len(list.Tools))
	}
	for i, w := range want {
		if len(list.Tools[i]) != len(w) {
			t.Errorf("tool %d has keys %v, want exactly %v", i, slices.Collect(maps.Keys(list.Tools[i])), slices.Collect(maps.Keys(w)))
		}
		for k, v := range w {
			if got := string(list.Tools[i][k]); got != v {
				t.Errorf("tool %d %s = %s, want %s", i, k, got, v)
			}
		}
	}
}

func TestServerUnknownProtocolVersionGetsOurs(t *testing.T) {
	s := serve(t, noExec)
	s.send(t, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`)
	var init struct{ ProtocolVersion string }
	_ = json.Unmarshal(s.recv(t).Result, &init)
	if init.ProtocolVersion != protocolVersions[0] {
		t.Fatalf("version %q, want %q", init.ProtocolVersion, protocolVersions[0])
	}
}

type recorder struct {
	mu    sync.Mutex
	calls []provider.ToolCall
}

func (r *recorder) exec(_ context.Context, c provider.ToolCall) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, c)
	if c.Name == "schedule.run_now" {
		return "", errors.New("policy: not allowed here")
	}
	return "2 jobs", nil
}

type wireResult struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

func TestServerCallMapsNames(t *testing.T) {
	rec := &recorder{}
	s := serve(t, rec.exec)
	s.send(t, `{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"schedule_list","arguments":{ "b" : 1, "a":"x"}}}`)
	var res wireResult
	r := s.recv(t)
	if err := json.Unmarshal(r.Result, &res); err != nil || string(r.ID) != "7" {
		t.Fatalf("call: %+v %v", r, err)
	}
	if res.IsError || len(res.Content) != 1 || res.Content[0].Type != "text" || res.Content[0].Text != "2 jobs" {
		t.Fatalf("result %+v", res)
	}
	// The arguments pass through untouched: the policy checks them (R18).
	if len(rec.calls) != 1 || rec.calls[0].Name != "schedule.list" || string(rec.calls[0].Args) != `{ "b" : 1, "a":"x"}` {
		t.Fatalf("exec got %+v", rec.calls)
	}

	// The name maps back by lookup, not by replacing "_" with ".".
	s.send(t, `{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"schedule_run_now"}}`)
	res = wireResult{}
	_ = json.Unmarshal(s.recv(t).Result, &res)
	if !res.IsError || len(res.Content) != 1 || !strings.Contains(res.Content[0].Text, "not allowed") {
		t.Fatalf("a tool error is a result with isError: %+v", res)
	}
	if c := rec.calls[1]; c.Name != "schedule.run_now" || string(c.Args) != "{}" {
		t.Fatalf("exec got %+v, want schedule.run_now with {}", c)
	}
}

// A name that is not offered is a protocol error, and nothing runs.
func TestServerRefusesNamesNotOffered(t *testing.T) {
	rec := &recorder{}
	s := serve(t, rec.exec)
	for _, name := range []string{"schedule.list", "memory_write", "schedule_run.now", ""} {
		s.send(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":{}}}`)
		r := s.recv(t)
		if r.Error == nil || r.Error.Code != codeInvalidParams {
			t.Errorf("%q: %+v, want error %d", name, r, codeInvalidParams)
		}
	}
	if len(rec.calls) != 0 {
		t.Fatalf("ran %+v", rec.calls)
	}
}

func TestServerUnknownMethod(t *testing.T) {
	s := serve(t, noExec)
	s.send(t, `{"jsonrpc":"2.0","id":3,"method":"resources/list"}`)
	if r := s.recv(t); r.Error == nil || r.Error.Code != -32601 || string(r.ID) != "3" {
		t.Fatalf("unknown method: %+v", r)
	}
	// An unknown notification is ignored; the ping is answered next.
	s.send(t, `{"jsonrpc":"2.0","method":"notifications/whatever"}`)
	s.send(t, `not json`)
	if r := s.recv(t); r.Error == nil || r.Error.Code != -32700 || string(r.ID) != "null" {
		t.Fatalf("parse error: %+v", r)
	}
	s.send(t, `{"jsonrpc":"2.0","id":4,"method":"ping"}`)
	if r := s.recv(t); string(r.ID) != "4" || r.Error != nil {
		t.Fatalf("ping after errors: %+v", r)
	}
}

func TestServerAnswersWhileACallRuns(t *testing.T) {
	release := make(chan struct{})
	s := serve(t, func(context.Context, provider.ToolCall) (string, error) {
		<-release
		return "done", nil
	})
	s.send(t, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"schedule_list","arguments":{}}}`)
	s.send(t, `{"jsonrpc":"2.0","id":2,"method":"ping"}`)
	if r := s.recv(t); string(r.ID) != "2" {
		t.Fatalf("first reply %+v, want the ping's", r)
	}
	close(release)
	if r := s.recv(t); string(r.ID) != "1" {
		t.Fatalf("second reply %+v, want the call's", r)
	}
}

func TestServerWaitsForCallsBeforeReturning(t *testing.T) {
	inR, inW := io.Pipe()
	var out strings.Builder
	var mu sync.Mutex
	started, finished := make(chan struct{}), make(chan struct{})
	srv := NewServer("ganglion", "1", tools, func(ctx context.Context, _ provider.ToolCall) (string, error) {
		close(started)
		<-ctx.Done() // the peer went away: the call is cancelled
		close(finished)
		return "", ctx.Err()
	})
	done := make(chan error, 1)
	go func() {
		done <- srv.Serve(context.Background(), struct {
			io.Reader
			io.Writer
		}{inR, writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return out.Write(p) })})
	}()
	_, _ = io.WriteString(inW, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"schedule_list"}}`+"\n")
	<-started
	_ = inW.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return")
	}
	select {
	case <-finished:
	default:
		t.Fatal("Serve returned before the call it started ended")
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestServerLineTooLong(t *testing.T) {
	long := `{"jsonrpc":"2.0","id":1,"method":"ping","params":{"x":"` + strings.Repeat("a", MaxLine) + `"}}` + "\n"
	err := NewServer("ganglion", "1", tools, noExec).Serve(context.Background(), struct {
		io.Reader
		io.Writer
	}{strings.NewReader(long), io.Discard})
	if !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("Serve: %v, want ErrLineTooLong", err)
	}
}

func TestLineReader(t *testing.T) {
	l := newLineReader(strings.NewReader("one\r\ntwo\n"+strings.Repeat("x", 20)+"\nlast"), 16)
	for _, want := range []string{"one", "two"} {
		if got, err := l.next(); err != nil || string(got) != want {
			t.Fatalf("next = %q, %v; want %q", got, err, want)
		}
	}
	if _, err := l.next(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("long line: %v", err)
	}
	l = newLineReader(strings.NewReader("last"), 16)
	if got, err := l.next(); err != nil || string(got) != "last" {
		t.Fatalf("last line = %q, %v", got, err)
	}
	if _, err := l.next(); !errors.Is(err, io.EOF) {
		t.Fatalf("after the last line: %v", err)
	}
}
