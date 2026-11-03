package runner_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/runner/runnertest"
)

func TestToolModuleRefusesCapabilities(t *testing.T) {
	for name, manifest := range map[string]string{
		"http.fetch": "name: m\ncapabilities:\n  http.fetch: [bank.gov.ua]\n",
		"secret.get": "name: m\ncapabilities:\n  secret.get: [TOKEN]\n",
		"event.emit": "name: m\ncapabilities:\n  event.emit: [x]\n",
		"state":      "name: m\ncapabilities:\n  state: true\n",
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
