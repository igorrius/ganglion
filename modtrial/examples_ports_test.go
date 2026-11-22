package modtrial

import (
	"errors"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/runner"
)

const portsManifestYAML = `
name: local/watcher
version: 1
triggers:
  - cron: "*/10 * * * *"
capabilities:
  notify: {per_day: 5}
ports:
  accepts: {quake: {mag: number}}
  emits: {alarm: {mag: number}}
  serves: {price: {in: {sym: string}, out: {value: number}}}
  calls: {rate: {in: integer, out: {value: number}}, other: {in: integer, out: {value: number}}}
`

func portsManifest(t *testing.T) runner.Manifest {
	t.Helper()
	m, err := runner.ParseManifest([]byte(portsManifestYAML))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	return m
}

func TestParsePortSteps(t *testing.T) {
	file := ex(`    calls:
      - port: rate
        answer: {value: 3}
      - port: other
        fails: unavailable
    steps:
      - trigger: {event: quake, payload: {mag: 5.4}, from: feed.quake}
        expect: {emits: {alarm: 1}, messages: 0}
      - trigger: {event: quake, payload: {mag: 1}}
      - trigger: {call: price, request: {sym: abc}}
`)
	got, err := Parse([]byte(file), portsManifest(t), "watcher")
	if err != nil {
		t.Fatal(err)
	}
	e := got.Examples[0]
	if len(e.Calls) != 2 || string(e.Calls[0].Answer) != `{"value":3}` || e.Calls[0].Code != runner.CallAnswered || e.Calls[1].Code != runner.CallUnavailable {
		t.Errorf("calls = %+v", e.Calls)
	}
	ev := e.Steps[0].Trigger
	if ev.Kind != "event" || ev.Port != "quake" || string(ev.Payload) != `{"mag":5.4}` || ev.From != "feed.quake" {
		t.Errorf("event = %+v", ev)
	}
	if e.Steps[0].Expect.Emits["alarm"] != 1 {
		t.Errorf("emits = %v", e.Steps[0].Expect.Emits)
	}
	if got := e.Steps[1].Trigger.From; got != "trial.quake" {
		t.Errorf("default from = %q", got)
	}
	if c := e.Steps[2].Trigger; c.Kind != "call" || c.Port != "price" || string(c.Payload) != `{"sym":"abc"}` || c.From != "trial.price" {
		t.Errorf("call = %+v", c)
	}
}

func TestParsePortStepsRefused(t *testing.T) {
	step := func(trigger string) string { return ex("    steps:\n      - trigger: " + trigger + "\n") }
	cases := []struct{ name, file, want string }{
		{"event on an unknown port", step("{event: nope, payload: {mag: 1}}"), "not an accepts port"},
		{"event on an emits port", step("{event: alarm, payload: {mag: 1}}"), "not an accepts port"},
		{"event without payload", step("{event: quake}"), "payload is required"},
		{"event payload outside the shape", step("{event: quake, payload: {mag: high}}"), "does not fit the port's shape"},
		{"event from is not an end", step("{event: quake, payload: {mag: 1}, from: Feed}"), "from"},
		{"call on an unknown port", step("{call: nope, request: {sym: a}}"), "not a serves port"},
		{"call without request", step("{call: price}"), "request is required"},
		{"call request outside the shape", step("{call: price, request: 5}"), "does not fit the port's shape"},
		{"payload on a call", step("{call: price, request: {sym: a}, payload: 1}"), `"payload" does not go with call`},
		{"args on an event", step("{event: quake, payload: {mag: 1}, args: {}}"), `"args" does not go with event`},
		{"event and call", step("{event: quake, call: price, payload: {mag: 1}}"), "more than one"},
		{"answer and fails", ex("    calls:\n      - {port: rate, answer: {value: 1}, fails: failed}\n    steps: [{after: 1s}]\n"), "both answer and fails"},
		{"neither answer nor fails", ex("    calls:\n      - {port: rate}\n    steps: [{after: 1s}]\n"), "answer or fails"},
		{"unknown fails", ex("    calls:\n      - {port: rate, fails: gone}\n    steps: [{after: 1s}]\n"), "fails"},
		{"answer outside the shape", ex("    calls:\n      - {port: rate, answer: {value: x}}\n    steps: [{after: 1s}]\n"), "does not fit the port's shape"},
		{"unknown calls port", ex("    calls:\n      - {port: price, answer: {value: 1}}\n    steps: [{after: 1s}]\n"), "not a calls port"},
		{"calls port twice", ex("    calls:\n      - {port: rate, fails: failed}\n      - {port: rate, fails: denied}\n    steps: [{after: 1s}]\n"), "more than once"},
		{"emits on an accepts port", ex("    steps:\n      - expect: {emits: {quake: 1}}\n"), "not an emits port"},
		{"emits on notify", ex("    steps:\n      - expect: {emits: {notify: 1}}\n"), "expect.messages"},
		{"negative emits", ex("    steps:\n      - expect: {emits: {alarm: -1}}\n"), "negative"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse([]byte(c.file), portsManifest(t), "watcher")
			if !errors.Is(err, ErrExamples) || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}
