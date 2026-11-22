package modtrial

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/igorrius/ganglion/runner"
)

// clock is the virtual time of one example: it only moves when a step says.
type clock struct{ now time.Time }

func (c *clock) Now() time.Time { return c.now }

// trialCells is the example's cells: ids to values and the virtual time of
// their last change. The runner checks the type and the ownership of a
// report before it reaches the port.
type trialCells struct {
	clock    *clock
	values   map[string]trialCell
	reported []Reported // what the module reported in the current step
}

type trialCell struct {
	value json.RawMessage
	at    time.Time
}

func (c *trialCells) set(id string, v json.RawMessage, at time.Time) {
	c.values[id] = trialCell{value: slices.Clone(v), at: at}
}

// Report implements runner.CellPort.
func (c *trialCells) Report(_ context.Context, id string, v json.RawMessage, _ string) error {
	c.set(id, v, c.clock.now)
	c.reported = append(c.reported, Reported{Cell: id, Value: slices.Clone(v)})
	return nil
}

// Get implements runner.CellPort: an unknown cell is an error, as a missing
// cell is on a node.
func (c *trialCells) Get(_ context.Context, id string) (json.RawMessage, time.Time, error) {
	v, ok := c.values[id]
	if !ok {
		return nil, time.Time{}, fmt.Errorf("modtrial: no cell %s", id)
	}
	return v.value, v.at, nil
}

// trialNotify counts a module's messages per virtual day against its
// per_day grant. It keeps the texts of the current step.
type trialNotify struct {
	clock  *clock
	perDay int
	days   map[string]int
	sent   []string // accepted texts of the current step
}

// Notify implements runner.NotifyPort.
func (n *trialNotify) Notify(_ context.Context, text string) runner.NotifyCode {
	day := n.clock.now.Format(time.DateOnly)
	if n.days[day] >= n.perDay {
		return runner.NotifyLimit
	}
	n.days[day]++
	n.sent = append(n.sent, text)
	return runner.NotifySent
}

// cannedTransport answers the example's exact URLs and nothing else; it
// never dials.
type cannedTransport struct{ answers map[string]Canned }

func newCannedTransport(cs []Canned) cannedTransport {
	t := cannedTransport{answers: map[string]Canned{}}
	for _, c := range cs {
		t.answers[c.URL] = c
	}
	return t
}

// RoundTrip implements http.RoundTripper.
func (t cannedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	c, ok := t.answers[r.URL.String()]
	if !ok {
		return nil, fmt.Errorf("modtrial: %s is not in the examples", r.URL.Host)
	}
	h := http.Header{}
	for k, v := range c.Headers {
		h.Set(k, v)
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", c.Status, http.StatusText(c.Status)),
		StatusCode:    c.Status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        h,
		Body:          io.NopCloser(bytes.NewReader([]byte(c.Body))),
		ContentLength: int64(len(c.Body)),
		Request:       r,
	}, nil
}

// trialEmit is the emit port of a trial: the runner has checked the port
// and the shape like a node's, and nothing is sent. It keeps what the
// runner let through, so a step can count and show the events.
type trialEmit struct {
	sent []Emitted // accepted events of the current step
}

// Emit implements runner.EmitPort.
func (e *trialEmit) Emit(_ context.Context, port string, payload json.RawMessage) runner.EmitCode {
	e.sent = append(e.sent, Emitted{Port: port, Payload: slices.Clone(payload)})
	return runner.EmitSent
}

// Refused implements runner.EmitPort.
func (*trialEmit) Refused(context.Context, runner.EmitCode) {}

// trialCall is the call port of a trial: the runner has checked the port and
// the request's shape like a node's, and nobody is called. A trial has no
// links to carry a call, so the answer is the example's canned one for the
// port, or "not here" when it has none.
type trialCall struct {
	answers map[string]CannedCall
}

func newTrialCall(cs []CannedCall) trialCall {
	t := trialCall{answers: make(map[string]CannedCall, len(cs))}
	for _, c := range cs {
		t.answers[c.Port] = c
	}
	return t
}

// Call implements runner.CallPort.
func (t trialCall) Call(_ context.Context, port string, _ json.RawMessage) (json.RawMessage, runner.CallCode) {
	c, ok := t.answers[port]
	switch {
	case !ok:
		return nil, runner.CallNotHere
	case c.Code != runner.CallAnswered:
		return nil, c.Code
	}
	return slices.Clone(c.Answer), runner.CallAnswered
}

// Settled implements runner.CallPort.
func (trialCall) Settled(context.Context, runner.CallCode) {}
