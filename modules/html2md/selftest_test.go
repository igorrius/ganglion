package html2md_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/modules/html2md"
	"github.com/igorrius/ganglion/runner"
)

// The embedded build passes its own self-test (ADR-0041).
func TestSelfTestPassesOnTheBuild(t *testing.T) {
	m, err := runner.LoadTool(context.Background(), html2md.Wasm, html2md.Manifest, runner.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	if err := html2md.SelfTest(context.Background(), m.Call); err != nil {
		t.Fatal(err)
	}
}

// A converter that loses the content, or fails, does not pass.
func TestSelfTestCatchesABrokenConverter(t *testing.T) {
	empty := func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"title":"","markdown":""}`), nil
	}
	if err := html2md.SelfTest(context.Background(), empty); err == nil || !strings.Contains(err.Error(), "article") {
		t.Fatalf("an empty converter passed: %v", err)
	}
	failing := func(context.Context, json.RawMessage) (json.RawMessage, error) { return nil, errors.New("trap") }
	if err := html2md.SelfTest(context.Background(), failing); err == nil {
		t.Fatal("a failing converter passed")
	}
}
