package runner

import (
	"slices"
	"strings"
	"testing"
)

const portsManifest = `
name: p
capabilities:
  notify: {per_day: 5}
ports:
  emits:   {reading: {soc: number, load: number}}
  accepts: {command: [start, stop]}
  serves:  {history: {in: {hours: number}, out: {points: {list: number}}}}
  calls:   {price: {in: {at: string}, out: number}}
`

func TestParsePorts(t *testing.T) {
	m, err := ParseManifest([]byte(portsManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Ports.Emits["reading"].Fields["soc"].Type != "number" || m.Ports.Accepts["command"].Type != "enum" {
		t.Errorf("events = %+v %+v", m.Ports.Emits, m.Ports.Accepts)
	}
	if h := m.Ports.Serves["history"]; h.In.Fields["hours"].Type != "number" || h.Out.Fields["points"].Type != "list" {
		t.Errorf("serves = %+v", m.Ports.Serves)
	}
	if c := m.Ports.Calls["price"]; c.In.Type != "object" || c.Out.Type != "number" {
		t.Errorf("calls = %+v", m.Ports.Calls)
	}
	e, a, s, c := m.Ports.Names()
	if !slices.Equal(e, []string{"reading"}) || !slices.Equal(a, []string{"command"}) || !slices.Equal(s, []string{"history"}) || !slices.Equal(c, []string{"price"}) {
		t.Errorf("names = %v %v %v %v", e, a, s, c)
	}
}

func TestEmitsAddsTheNotifyOutput(t *testing.T) {
	m, err := ParseManifest([]byte(portsManifest))
	if err != nil {
		t.Fatal(err)
	}
	out := m.Emits()
	n, ok := out["notify"]
	if !ok || n.Type != "object" || n.Fields["text"].Type != "string" || n.Fields["at"].Type != "string" || len(n.Fields) != 2 {
		t.Fatalf("notify = %+v (%v)", n, ok)
	}
	if _, ok := out["reading"]; !ok || len(out) != 2 {
		t.Errorf("emits = %v", out)
	}
	plain, err := ParseManifest([]byte("name: q\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(plain.Emits()) != 0 || plain.Ports.Any() {
		t.Errorf("a module without ports emits %v", plain.Emits())
	}
}

func TestParsePortsRejects(t *testing.T) {
	cases := map[string]struct{ src, want string }{
		"unknown kind":     {"ports:\n  sends: {x: string}\n", "sends"},
		"unknown key":      {"ports:\n  serves: {h: {in: string, out: string, more: string}}\n", "more"},
		"upper case":       {"ports:\n  emits: {Reading: string}\n", "Reading"},
		"starts with _":    {"ports:\n  emits: {_x: string}\n", "_x"},
		"too long":         {"ports:\n  emits: {" + strings.Repeat("a", 33) + ": string}\n", "at most 32"},
		"reserved":         {"ports:\n  emits: {notify: string}\n", "reserved"},
		"reserved accepts": {"ports:\n  accepts: {notify: string}\n", "reserved"},
		"twice":            {"ports:\n  emits: {x: string}\n  accepts: {x: string}\n", "already a port in ports.emits"},
		"twice calls":      {"ports:\n  serves: {x: {in: string, out: string}}\n  calls: {x: {in: string, out: string}}\n", "already a port in ports.serves"},
		"no out":           {"ports:\n  serves: {h: {in: string}}\n", "in and out"},
		"no in":            {"ports:\n  calls: {h: {out: string}}\n", "in and out"},
		"bad shape":        {"ports:\n  emits: {x: text}\n", "unknown type"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManifest([]byte("name: x\n" + tc.src))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one naming %q", err, tc.want)
			}
		})
	}
}
