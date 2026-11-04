package html2md

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"path"
	"strings"
	"time"
)

// The ceiling a release of html2md must stay under; only a new core
// raises it (ADR-0041). html2md is a pure function: no capability at all.
const (
	ABI        = 1
	MaxMemory  = 64 << 20
	MaxTimeout = 10 * time.Second
)

// samples are pages every release must convert: each *.html with a
// *.want of lines its Markdown must contain (an empty one only asks that
// it converts).
//
//go:embed selftest
var samples embed.FS

// Caller runs the module once, as runner.ToolModule.Call does; an alias,
// so SelfTest fits modstore.SelfTest without an import.
type Caller = func(ctx context.Context, payload json.RawMessage) (json.RawMessage, error)

// SelfTest converts every sample with call and checks the result; a new
// release must pass it before it replaces the active one (R-RUN-8).
func SelfTest(ctx context.Context, call Caller) error {
	entries, err := samples.ReadDir("selftest")
	if err != nil {
		return err
	}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".html")
		if !ok {
			continue
		}
		if err := checkSample(ctx, call, name); err != nil {
			return fmt.Errorf("html2md self-test %s: %w", name, err)
		}
	}
	return nil
}

func checkSample(ctx context.Context, call Caller, name string) error {
	html, err := samples.ReadFile(path.Join("selftest", name+".html"))
	if err != nil {
		return err
	}
	want, err := samples.ReadFile(path.Join("selftest", name+".want"))
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"html": string(html), "url": "https://example.test/"})
	out, err := call(ctx, payload)
	if err != nil {
		return err
	}
	var res struct {
		Markdown string `json:"markdown"`
	}
	if err := json.Unmarshal(out, &res); err != nil {
		return fmt.Errorf("result: %w", err)
	}
	for line := range strings.Lines(string(want)) {
		if line = strings.TrimSpace(line); line != "" && !strings.Contains(res.Markdown, line) {
			return fmt.Errorf("the Markdown lacks %q", line)
		}
	}
	return nil
}
