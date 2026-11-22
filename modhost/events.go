package modhost

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/runner"
)

// This file is the publishing side of events (R-BUS-6, ADR-0108): a module's
// emit puts one message in the EVENTS stream, on the subject of its port;
// a notice that was sent is also an event on the module's notify output.
// The links' consumers read them (linkconsumers.go).

// eventNode is the node token of an event's subject until nodes have names
// (M10): the same word the Ganglion-From header says.
const eventNode = fromNode

// eventOutcome is what became of an emit call, for the metrics: a closed
// set.
type eventOutcome string

// Event outcomes.
const (
	eventSent    eventOutcome = "sent"
	eventDenied  eventOutcome = "denied"   // no such emits port
	eventFormat  eventOutcome = "format"   // the payload broke the port's shape
	eventNotHere eventOutcome = "not_here" // init, health or a check
	eventFailed  eventOutcome = "failed"   // over a limit, or the node could not publish
)

// eventOutcomes are the outcomes the runner's codes stand for.
var eventOutcomes = map[runner.EmitCode]eventOutcome{
	runner.EmitSent:    eventSent,
	runner.EmitDenied:  eventDenied,
	runner.EmitFormat:  eventFormat,
	runner.EmitNotHere: eventNotHere,
	runner.EmitFailed:  eventFailed,
}

// emitPort is the runner's EmitPort of one loaded module. The runner has
// checked the port, the size, the shape and the run's limit before the
// port.
type emitPort struct {
	h *Host
	l *loaded
}

// Emit implements runner.EmitPort. Only a run sends: init and health carry
// none and get EmitNotHere. An event's key is its run and its number in the
// run, so a run delivered again publishes nothing twice within the stream's
// window. A failure to publish is logged and answered EmitFailed.
func (p emitPort) Emit(ctx context.Context, port string, payload json.RawMessage) runner.EmitCode {
	r, ok := runOf(ctx)
	if !ok {
		p.h.in.emitted(ctx, p.l.name, eventNotHere)
		return runner.EmitNotHere
	}
	*r.emitted++
	id := "ev:" + p.l.name + ":" + r.ID + ":" + strconv.Itoa(*r.emitted)
	o := p.h.publishEvent(ctx, p.l, port, id, r, payload)
	p.h.in.emitted(ctx, p.l.name, o)
	if o == eventSent {
		return runner.EmitSent
	}
	return runner.EmitFailed
}

// Refused implements runner.EmitPort: counts a call the runner answered.
func (p emitPort) Refused(ctx context.Context, code runner.EmitCode) {
	p.h.in.emitted(ctx, p.l.name, eventOutcomes[code])
}

// publishEvent stores one event of l on its port's subject, under key id,
// with the run's identity and depth. The publish does not end with ctx: the
// run goes on, and an event stored once is stored.
func (h *Host) publishEvent(ctx context.Context, l *loaded, port, id string, r run, payload []byte) eventOutcome {
	subject, err := bus.Event(eventNode, l.short, port)
	if err == nil {
		headers := map[string]string{bus.HeaderFrom: fromNode + "/" + l.name, bus.HeaderRun: r.ID, bus.HeaderDepth: strconv.Itoa(r.Depth)}
		err = h.d.Publisher.Publish(context.WithoutCancel(ctx), subject, id, headers, payload)
	}
	if err != nil {
		h.d.Log.Error("modhost: a module's event was not published", "module", l.name, "port", port, "err", err)
		return eventFailed
	}
	return eventSent
}

// noticeEvent is the payload of the event a sent notice is on the module's
// notify output (the shape runner.Manifest.Emits gives it).
type noticeEvent struct {
	Text string `json:"text"`
	At   string `json:"at"` // RFC 3339, UTC
}

// publishNotice makes a notice that was sent an event on the module's
// notify output, so a link can carry it. Its failure is logged: the notice
// itself was sent. The key keeps the notice's number apart from the
// module's own events.
func (h *Host) publishNotice(ctx context.Context, l *loaded, r run, text string, n int) {
	payload, err := json.Marshal(noticeEvent{Text: text, At: h.d.Now().UTC().Format(time.RFC3339)})
	if err != nil {
		return
	}
	id := "ev:" + l.name + ":" + r.ID + ":notify:" + strconv.Itoa(n)
	h.publishEvent(ctx, l, runner.NotifyOutput, id, r, payload)
}
