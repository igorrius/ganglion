package web

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// ToolCaller runs a pure tool module once (runner.ToolModule).
type ToolCaller interface {
	Call(ctx context.Context, payload json.RawMessage) (json.RawMessage, error)
}

// ModuleConverter is the Converter over the html2md module: the page
// goes into the sandbox, Markdown comes out (ADR-0035).
type ModuleConverter struct {
	module ToolCaller
}

// NewModuleConverter converts with module.
func NewModuleConverter(module ToolCaller) ModuleConverter {
	return ModuleConverter{module: module}
}

// Convert implements Converter.
func (c ModuleConverter) Convert(ctx context.Context, url, html string) (string, string, error) {
	var payload bytes.Buffer
	enc := json.NewEncoder(&payload)
	enc.SetEscapeHTML(false) // a page is mostly '<' and '>': escaped, each would take six bytes
	if err := enc.Encode(struct {
		HTML string `json:"html"`
		URL  string `json:"url"`
	}{html, url}); err != nil {
		return "", "", fmt.Errorf("web: html2md input: %w", err)
	}
	raw, err := c.module.Call(ctx, bytes.TrimSuffix(payload.Bytes(), []byte("\n")))
	if err != nil {
		return "", "", fmt.Errorf("web: html2md: %w", err)
	}
	var page struct {
		Title    string `json:"title"`
		Markdown string `json:"markdown"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return "", "", fmt.Errorf("web: html2md result: %w", err)
	}
	return page.Title, page.Markdown, nil
}
