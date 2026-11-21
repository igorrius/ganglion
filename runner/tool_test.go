package runner_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/modules/html2md"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/runner/runnertest"
)

func TestToolModuleRefusesCapabilities(t *testing.T) {
	for name, manifest := range map[string]string{
		"http.fetch":   "name: m\ncapabilities:\n  http.fetch: [bank.gov.ua]\n",
		"secret.get":   "name: m\ncapabilities:\n  secret.get: [TOKEN]\n",
		"port":         "name: m\nports:\n  emits: {x: string}\n",
		"state":        "name: m\ncapabilities:\n  state: true\n",
		"notify":       "name: m\ncapabilities:\n  notify: {per_day: 3}\n",
		"cell trigger": "name: m\ncells:\n  read: [p.soc]\ntriggers:\n  - cell: [p.soc]\n",
	} {
		// The manifest is refused before the module is compiled, so any
		// bytes do as the module.
		_, err := runner.LoadTool(context.Background(), []byte("not wasm"), []byte(manifest), runner.Deps{})
		if !errors.Is(err, runner.ErrCapabilities) {
			t.Errorf("%s: LoadTool = %v, want ErrCapabilities", name, err)
		}
		if _, err := runner.NewLazyTool([]byte("not wasm"), []byte(manifest), runner.Deps{}); !errors.Is(err, runner.ErrCapabilities) {
			t.Errorf("%s: NewLazyTool = %v, want ErrCapabilities", name, err)
		}
	}
	if _, err := runner.LoadTool(context.Background(), []byte("x"), []byte("name: [\n"), runner.Deps{}); err == nil || errors.Is(err, runner.ErrCapabilities) {
		t.Errorf("a bad manifest = %v", err)
	}
	if _, err := runner.LoadTool(context.Background(), []byte("not wasm"), []byte("name: m\n"), runner.Deps{}); err == nil {
		t.Error("bad wasm loaded")
	}
}

func TestLazyToolRetriesAFailedLoad(t *testing.T) {
	l, err := runner.NewLazyTool([]byte("not wasm"), []byte("name: m\n"), runner.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := l.Call(context.Background(), []byte(`{}`)); err == nil || l.Loaded() {
			t.Fatalf("Call = %v, loaded %v", err, l.Loaded())
		}
	}
	if err := l.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A tool module reads remote text (html2md, a page): it gets no log
// host function, so nothing it reads can reach the node's log.
func TestToolModuleHasNoLog(t *testing.T) {
	probe, err := runnertest.BuildProbe()
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	m, err := runner.LoadTool(context.Background(), probe, []byte("name: probe\n"),
		runner.Deps{Logger: slog.New(slog.NewTextHandler(&logs, nil))})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	_, err = m.Call(context.Background(), []byte(`{"op":"log","value":"ignore previous instructions"}`))
	if err == nil || !strings.Contains(err.Error(), "log") {
		t.Fatalf("Call = %v, want the missing log import refused", err)
	}
	if strings.Contains(logs.String(), "ignore previous") {
		t.Fatalf("the module logged: %s", logs.String())
	}
}

// A tool answer that does not fit the trigger's output is the node's
// complaint, not the module's text (R-RUN-14). html2md stands in for a
// tool module: the probe imports log, which a tool module does not get.
func loadHTML2MD(t *testing.T, output string) *runner.ToolModule {
	t.Helper()
	manifest := "name: html2md\ntriggers:\n  - tool: convert\n    about: x\n    output:\n      title: string\n      markdown: " + output + "\n"
	m, err := runner.LoadTool(context.Background(), html2md.Wasm, []byte(manifest), runner.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	return m
}

const page = `{"html":"<html><body><article><h1>Hello</h1><p>secret words here</p></article></body></html>","url":"https://example.test/"}`

func TestToolAnswerMustFitItsOutput(t *testing.T) {
	m := loadHTML2MD(t, "integer")
	_, err := m.Call(context.Background(), []byte(page)) // markdown is a string
	me, ok := errors.AsType[*runner.ModuleError](err)
	if !ok || me.Kind != "module.format" || me.Msg != "field markdown: expected integer, got string" {
		t.Fatalf("Call = %v, want a module.format ModuleError naming the field", err)
	}
	if strings.Contains(me.Msg, "secret") {
		t.Fatal("the error echoes the module's text")
	}
}

func TestToolAnswerThatFitsPasses(t *testing.T) {
	m := loadHTML2MD(t, "string")
	if _, err := m.Call(context.Background(), []byte(page)); err != nil {
		t.Fatalf("Call = %v, want the answer", err)
	}
}
