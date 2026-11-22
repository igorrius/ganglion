package modtrial

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"slices"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/igorrius/ganglion/runner"
)

// FileName is the examples file beside a module's manifest.
const FileName = "examples.yaml"

// Limits of an examples file.
const (
	MaxFileBytes = 32 << 10
	MaxExamples  = 10
	MaxSteps     = 20
	MaxBodyBytes = 64 << 10
)

// ErrExamples wraps every reason an examples file is refused; the text
// after it is the node's words and may quote names from the file.
var ErrExamples = errors.New("examples.yaml")

// Examples is a parsed and checked examples file.
type Examples struct {
	Examples []Example `yaml:"examples"`
}

// Example is one run from an empty state: its settings, the canned answers
// of the network and the steps in order.
type Example struct {
	Name    string            `yaml:"name"`
	Start   time.Time         `yaml:"-"`       // parsed from "start" (RFC 3339); zero = the trial's start
	Config  map[string]string `yaml:"config"`  // over the manifest's defaults; keys the manifest declares
	Secrets map[string]string `yaml:"secrets"` // fake values; names in secret.get
	HTTP    []Canned          `yaml:"http"`
	Calls   []CannedCall      `yaml:"-"` // parsed from "calls"
	Steps   []Step            `yaml:"steps"`
}

// CannedCall is the answer to a call the module makes on one of its calls
// ports: an answer that fits the port's out shape, or a refusal. A trial has
// no links, so these are the only answers a call gets; a calls port with no
// entry answers "not here".
type CannedCall struct {
	Port   string
	Answer json.RawMessage // the answer, when Code is runner.CallAnswered
	Code   runner.CallCode // runner.CallAnswered, or the refusal the module gets
}

// Canned is the answer to one exact URL.
type Canned struct {
	URL     string            `yaml:"url"` // exact; its host is in http.fetch
	Status  int               `yaml:"status"`
	Headers map[string]string `yaml:"headers"`
	Body    string            `yaml:"body"`
}

// Step moves time on, sets cells, pulls a trigger and states what must
// follow.
type Step struct {
	After   time.Duration              `yaml:"-"` // parsed from "after" (a Go duration, ≥ 0)
	Cells   map[string]json.RawMessage `yaml:"-"` // parsed from "cells": id → JSON value
	Trigger Trigger                    `yaml:"-"`
	Expect  Expect                     `yaml:"expect"`
}

// Trigger is "cell", "cron", a tool call, an event, a call, or none ("" Kind).
type Trigger struct {
	Kind string          // "cell" | "cron" | "tool" | "event" | "call" | ""
	Tool string          // the manifest's tool trigger name, for Kind "tool"
	Args json.RawMessage // an object, for Kind "tool" ({} when none)
	// Port is the accepts port an event arrives on (Kind "event") or the
	// served port a call is made on (Kind "call").
	Port string
	// Payload is the event or the request, already checked with the port's
	// shape (Kinds "event" and "call").
	Payload json.RawMessage
	// From is the sending end, `<module>.<port>`, as the module sees it
	// (Kinds "event" and "call").
	From string
}

// Expect is what a step must have done; every part is optional.
type Expect struct {
	Messages *int     `yaml:"messages"` // exact count of accepted notify calls
	Reports  []string `yaml:"reports"`  // exact set of cell ids reported; nil = not checked
	Fails    bool     `yaml:"fails"`    // the step must end in an error
	// Emits is the exact count of events the step emitted on each named
	// port; a port not named is not checked, and an empty map checks
	// nothing. The port is one of the manifest's emits ports.
	Emits map[string]int `yaml:"emits"`
}

type rawFile struct {
	Examples []rawExample `yaml:"examples"`
}

type rawExample struct {
	Name    string            `yaml:"name"`
	Start   string            `yaml:"start"`
	Config  map[string]string `yaml:"config"`
	Secrets map[string]string `yaml:"secrets"`
	HTTP    []Canned          `yaml:"http"`
	Calls   []rawCall         `yaml:"calls"`
	Steps   []rawStep         `yaml:"steps"`
}

type rawCall struct {
	Port   string    `yaml:"port"`
	Answer yaml.Node `yaml:"answer"`
	Fails  string    `yaml:"fails"`
}

type rawStep struct {
	After   yaml.Node `yaml:"after"`
	Cells   yaml.Node `yaml:"cells"`
	Trigger yaml.Node `yaml:"trigger"`
	Expect  Expect    `yaml:"expect"`
}

func refuse(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrExamples, fmt.Sprintf(format, a...))
}

// Parse reads and checks an examples file against the module's manifest.
// prefix is the module's short name: its own cells are <prefix>.<name>.
func Parse(data []byte, m runner.Manifest, prefix string) (Examples, error) {
	if len(data) > MaxFileBytes {
		return Examples{}, refuse("the file is %d bytes, at most %d", len(data), MaxFileBytes)
	}
	var raw rawFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return Examples{}, refuse("the file is empty")
		}
		return Examples{}, refuse("%v", err)
	}
	switch {
	case len(raw.Examples) == 0:
		return Examples{}, refuse("no examples")
	case len(raw.Examples) > MaxExamples:
		return Examples{}, refuse("%d examples, at most %d", len(raw.Examples), MaxExamples)
	}
	c := checker{m: m, prefix: prefix}
	var out Examples
	seen := map[string]bool{}
	for i, re := range raw.Examples {
		if re.Name == "" {
			return Examples{}, refuse("example %d has no name", i+1)
		}
		if seen[re.Name] {
			return Examples{}, refuse("duplicate example name %q", re.Name)
		}
		seen[re.Name] = true
		e, err := c.example(re)
		if err != nil {
			return Examples{}, refuse("example %q %v", re.Name, err)
		}
		out.Examples = append(out.Examples, e)
	}
	return out, nil
}

type checker struct {
	m      runner.Manifest
	prefix string
}

// own reports whether id is a cell the module reports.
func (c checker) own(id string) bool {
	for name := range c.m.Cells.Report {
		if id == c.prefix+"."+name {
			return true
		}
	}
	return false
}

func (c checker) example(re rawExample) (Example, error) {
	e := Example{Name: re.Name, Config: re.Config, Secrets: re.Secrets, HTTP: re.HTTP}
	if re.Start != "" {
		t, err := time.Parse(time.RFC3339, re.Start)
		if err != nil {
			return e, fmt.Errorf("start %q is not an RFC 3339 time (2026-01-02T15:04:05Z)", re.Start)
		}
		e.Start = t
	}
	for k := range re.Config {
		if _, ok := c.m.Config[k]; !ok {
			return e, fmt.Errorf("config key %q is not declared by the manifest", k)
		}
	}
	for k := range re.Secrets {
		if !slices.Contains(c.m.Capabilities.SecretGet, k) {
			return e, fmt.Errorf("secret %q is not in secret.get", k)
		}
	}
	for _, h := range re.HTTP {
		if err := c.canned(h); err != nil {
			return e, err
		}
	}
	var err error
	if e.Calls, err = c.calls(re.Calls); err != nil {
		return e, err
	}
	if len(re.Steps) == 0 {
		return e, errors.New("has no steps")
	}
	if len(re.Steps) > MaxSteps {
		return e, fmt.Errorf("has %d steps, at most %d", len(re.Steps), MaxSteps)
	}
	for i, rs := range re.Steps {
		s, err := c.step(rs)
		if err != nil {
			return e, fmt.Errorf("step %d: %w", i+1, err)
		}
		e.Steps = append(e.Steps, s)
	}
	return e, nil
}

// canned applies the rule the runner's fetch applies: an http or https URL
// whose host the manifest lists.
func (c checker) canned(h Canned) error {
	u, err := url.Parse(h.URL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("http %q: the scheme must be https or http", h.URL)
	}
	if !slices.Contains(c.m.Capabilities.HTTPFetch, u.Hostname()) {
		return fmt.Errorf("http %q: host %s is not in http.fetch", h.URL, u.Hostname())
	}
	if h.Status < 100 || h.Status > 599 {
		return fmt.Errorf("http %q: status %d is outside 100..599", h.URL, h.Status)
	}
	if len(h.Body) > MaxBodyBytes {
		return fmt.Errorf("http %q: the body is %d bytes, at most %d", h.URL, len(h.Body), MaxBodyBytes)
	}
	return nil
}

func (c checker) step(rs rawStep) (Step, error) {
	s := Step{Expect: rs.Expect}
	var err error
	if s.After, err = parseAfter(rs.After); err != nil {
		return s, err
	}
	if s.Cells, err = c.cells(rs.Cells); err != nil {
		return s, err
	}
	if !rs.Trigger.IsZero() {
		if s.Trigger, err = c.trigger(rs.Trigger); err != nil {
			return s, err
		}
	}
	for _, id := range rs.Expect.Reports {
		if !c.own(id) {
			return s, fmt.Errorf("expect.reports names %s, which the module does not report", id)
		}
	}
	return s, c.emits(rs.Expect.Emits)
}

// emits checks the ports expect.emits counts: the manifest's emits ports.
// The notify output is counted by expect.messages.
func (c checker) emits(counts map[string]int) error {
	for _, port := range slices.Sorted(maps.Keys(counts)) {
		if port == runner.NotifyOutput {
			return errors.New("expect.emits cannot count notify: count messages with expect.messages")
		}
		if _, ok := c.m.Ports.Emits[port]; !ok {
			return fmt.Errorf("expect.emits names %s, which is not an emits port of the manifest", port)
		}
		if counts[port] < 0 {
			return fmt.Errorf("expect.emits %s is negative", port)
		}
	}
	return nil
}

func parseAfter(n yaml.Node) (time.Duration, error) {
	if n.IsZero() {
		return 0, nil
	}
	var txt string
	if err := n.Decode(&txt); err != nil {
		return 0, fmt.Errorf("after: %w", err)
	}
	d, err := time.ParseDuration(txt)
	if err != nil {
		return 0, fmt.Errorf("after %q is not a Go duration", txt)
	}
	if d < 0 {
		return 0, fmt.Errorf("after %q is negative", txt)
	}
	return d, nil
}

func (c checker) cells(n yaml.Node) (map[string]json.RawMessage, error) {
	if n.IsZero() {
		return nil, nil
	}
	var vals map[string]any
	if err := n.Decode(&vals); err != nil {
		return nil, fmt.Errorf("cells: %w", err)
	}
	out := make(map[string]json.RawMessage, len(vals))
	for _, id := range slices.Sorted(maps.Keys(vals)) {
		v := vals[id]
		if !slices.Contains(c.m.Cells.Read, id) && !c.own(id) {
			return nil, fmt.Errorf("cell %s is not in cells.read", id)
		}
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("cell %s: the value is not JSON", id)
		}
		out[id] = b
	}
	return out, nil
}

func (c checker) trigger(n yaml.Node) (Trigger, error) {
	if n.Kind == yaml.ScalarNode {
		return c.kindTrigger(n.Value)
	}
	if n.Kind != yaml.MappingNode {
		return Trigger{}, errors.New("trigger must be cell, cron, {tool, args}, {event, payload} or {call, request}")
	}
	kind, err := triggerKind(n)
	if err != nil {
		return Trigger{}, err
	}
	switch kind {
	case kindEvent:
		return c.eventTrigger(n)
	case kindCall:
		return c.callTrigger(n)
	}
	return c.toolTrigger(n)
}

func (c checker) toolTrigger(n yaml.Node) (Trigger, error) {
	var tr struct {
		Tool string    `yaml:"tool"`
		Args yaml.Node `yaml:"args"`
	}
	if err := n.Decode(&tr); err != nil {
		return Trigger{}, fmt.Errorf("trigger: %w", err)
	}
	if !slices.ContainsFunc(c.m.Triggers, func(t runner.Trigger) bool { return t.Tool != "" && t.Tool == tr.Tool }) {
		return Trigger{}, fmt.Errorf("trigger tool %q is not a tool trigger of the manifest", tr.Tool)
	}
	args, err := toolArgs(tr.Args)
	if err != nil {
		return Trigger{}, fmt.Errorf("trigger tool %q: %w", tr.Tool, err)
	}
	return Trigger{Kind: kindTool, Tool: tr.Tool, Args: args}, nil
}

func (c checker) kindTrigger(kind string) (Trigger, error) {
	if kind == kindCell {
		if len(c.m.Listens()) == 0 {
			return Trigger{}, errors.New("trigger cell: the manifest has no cell trigger")
		}
		return Trigger{Kind: kindCell}, nil
	}
	if kind == kindCron {
		if !slices.ContainsFunc(c.m.Triggers, func(t runner.Trigger) bool { return t.Cron != "" }) {
			return Trigger{}, errors.New("trigger cron: the manifest has no cron trigger")
		}
		return Trigger{Kind: kindCron}, nil
	}
	return Trigger{}, fmt.Errorf("trigger %q is not cell, cron or a tool call", kind)
}

func toolArgs(n yaml.Node) (json.RawMessage, error) {
	if n.IsZero() {
		return json.RawMessage("{}"), nil
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return nil, fmt.Errorf("args: %w", err)
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("args must be an object")
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return nil, errors.New("args are not JSON")
	}
	return b, nil
}
