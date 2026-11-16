//go:build lab

package chatlab

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func startSession(t *testing.T, script []ScriptStep, idle time.Duration) (dir string, done chan error) {
	t.Helper()
	dir = sessionTempDir(t)
	done = make(chan error, 1)
	opts := SessionOptions{Dir: dir, Idle: idle, Scenarios: t.TempDir(),
		Node: NodeOptions{Tokens: &Counter{}, Scenario: Scenario{Name: "session", Model: ModelScripted, Persona: PersonaDefault, Script: script}}}
	go func() { done <- RunSession(context.Background(), opts) }()
	waitFor(t, func() bool { _, err := Call(SocketIn(dir), Request{Op: OpPing}); return err == nil })
	t.Cleanup(func() {
		_, _ = Call(SocketIn(dir), Request{Op: OpStop})
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(30 * time.Second):
			t.Error("the session did not stop")
		}
	})
	return dir, done
}

func call(t *testing.T, dir string, r Request) string {
	t.Helper()
	got, err := Call(SocketIn(dir), r)
	if err != nil {
		t.Fatal(err)
	}
	if !got.OK {
		t.Fatalf("%s: %s", r.Op, got.Error)
	}
	return got.Output
}

func TestSessionSayPrintsWhatAppeared(t *testing.T) {
	dir, _ := startSession(t, []ScriptStep{{Say: "Hello there."}, {Say: "Second."}}, time.Minute)
	if out := call(t, dir, Request{Op: OpSay, Text: "hi"}); !strings.Contains(out, "Hello there.") {
		t.Fatalf("output %q", out)
	}
	out := call(t, dir, Request{Op: OpSay, Text: "again"})
	if !strings.Contains(out, "Second.") || strings.Contains(out, "Hello there.") {
		t.Fatalf("output %q", out)
	}
}

func TestSessionPressMissingIsAnError(t *testing.T) {
	dir, _ := startSession(t, []ScriptStep{{Say: "ok"}}, time.Minute)
	got, err := Call(SocketIn(dir), Request{Op: OpPress, Text: "Approve"})
	if err != nil || got.OK || !strings.Contains(got.Error, "Approve") {
		t.Fatalf("response %+v, %v", got, err)
	}
}

func TestSessionAdvanceClosesAQuestion(t *testing.T) {
	dir, _ := startSession(t, []ScriptStep{{Tool: "schedule.create", Args: taskArgs(t, time.Now().Add(time.Hour))}, {Say: "I asked."}, {Say: "It expired."}}, time.Minute)
	if out := call(t, dir, Request{Op: OpSay, Text: "Set a reminder"}); !strings.Contains(out, "[Approve | Deny]") {
		t.Fatalf("output %q", out)
	}
	out := call(t, dir, Request{Op: OpAdvance, Clock: "confirmations", For: 16 * time.Minute})
	if !strings.Contains(out, "Expired, ") {
		t.Fatalf("output %q", out)
	}
}

func TestShowLogsAndJournal(t *testing.T) {
	dir, _ := startSession(t, []ScriptStep{{Tool: "memory.write", Args: `{"topic":"Boiler","summary":"The water boiler","fact":"80 litres."}`}, {Say: "Stored."}}, time.Minute)
	call(t, dir, Request{Op: OpSay, Text: "Remember my boiler"})
	for what, want := range map[string]string{"journal": "memory.write", "turns": "scripted", "history": "Remember my boiler", "memory": "80 litres", "logs": "outbox: sent"} {
		out := call(t, dir, Request{Op: OpShow, What: what})
		if !strings.Contains(out, want) {
			t.Errorf("show %s: no %q in %q", what, want, out)
		}
		if strings.Contains(out, labToken) {
			t.Errorf("show %s holds the bot's token", what)
		}
	}
	if got, err := Call(SocketIn(dir), Request{Op: OpShow, What: "secrets"}); err != nil || got.OK {
		t.Fatalf("an unknown view: %+v, %v", got, err)
	}
}

func TestSessionSaveKeepsItsSteps(t *testing.T) {
	dir := sessionTempDir(t)
	scenarios := t.TempDir()
	done := make(chan error, 1)
	go func() {
		done <- RunSession(context.Background(), SessionOptions{Dir: dir, Idle: time.Minute, Scenarios: scenarios,
			Node: NodeOptions{Tokens: &Counter{}, Scenario: Scenario{Name: "session", Model: ModelScripted, Persona: PersonaDefault, Script: []ScriptStep{{Say: "ok"}}}}})
	}()
	waitFor(t, func() bool { _, err := Call(SocketIn(dir), Request{Op: OpPing}); return err == nil })
	call(t, dir, Request{Op: OpSay, Text: "hi"})
	call(t, dir, Request{Op: OpSave, Name: "from-a-chat"})
	b, err := os.ReadFile(filepath.Join(scenarios, "from-a-chat.yaml"))
	if err != nil || !strings.Contains(string(b), "say: hi") {
		t.Fatalf("saved %q, %v", b, err)
	}
	call(t, dir, Request{Op: OpStop})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the directory stayed: %v", err)
	}
}

func TestIdleStops(t *testing.T) {
	dir, done := startSession(t, []ScriptStep{{Say: "ok"}}, 300*time.Millisecond)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
		done <- nil // for the cleanup
	case <-time.After(30 * time.Second):
		t.Fatal("the session did not stop when idle")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("the directory stayed: %v", err)
	}
}

func TestSessionOutputHidesKeys(t *testing.T) {
	const key = "sk-or-v1-1234567890abcdef"
	dir := sessionTempDir(t)
	done := make(chan error, 1)
	go func() {
		done <- RunSession(context.Background(), SessionOptions{Dir: dir, Idle: time.Minute, Scenarios: t.TempDir(),
			Node: NodeOptions{Tokens: &Counter{}, Keys: map[string]string{"openrouter": key},
				Scenario: Scenario{Name: "session", Model: ModelScripted, Persona: PersonaDefault, Script: []ScriptStep{{Say: "It said " + key}}}}})
	}()
	waitFor(t, func() bool { _, err := Call(SocketIn(dir), Request{Op: OpPing}); return err == nil })
	out := call(t, dir, Request{Op: OpSay, Text: "hi"})
	if !strings.Contains(out, "It said") || strings.Contains(out, key) {
		t.Fatalf("output %q", out)
	}
	if show := call(t, dir, Request{Op: OpShow, What: "history"}); strings.Contains(show, key) {
		t.Fatalf("show %q", show)
	}
	call(t, dir, Request{Op: OpStop})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
