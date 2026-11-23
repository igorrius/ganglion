package modhost

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/links"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
)

// This file is calls over links (R-BUS-6, ADR-0108): a module's call host
// function sends a request to the endpoint its link names and waits for the
// answer; every running module with served ports registers them with NATS
// micro and runs for each request. Calls are not stored and not retried.

// Calls is the bus as calls need it; bus/natsbus implements it.
type Calls interface {
	// Serve registers a service with one endpoint per entry; stop removes it
	// and returns at once.
	Serve(service, version string, endpoints []bus.Endpoint) (stop func() error, err error)
	// Request sends payload to subject and waits for the answer until ctx
	// ends: bus.ErrNoResponder when nobody serves it, a *bus.CallError for an
	// error answer.
	Request(ctx context.Context, subject string, headers map[string]string, payload []byte) ([]byte, error)
}

// noCalls is the bus of a host that has none: nothing is served and every
// call finds nobody.
type noCalls struct{}

func (noCalls) Serve(string, string, []bus.Endpoint) (func() error, error) {
	return func() error { return nil }, nil
}
func (noCalls) Request(context.Context, string, map[string]string, []byte) ([]byte, error) {
	return nil, bus.ErrNoResponder
}

// callSettle is what a caller keeps of its run after it gave up waiting: the
// time to take a failed answer and finish.
const callSettle = time.Second

// callOutcome is what became of a call, for the caller's metrics: a closed
// set.
type callOutcome string

// Call outcomes.
const (
	callAnswered    callOutcome = "answered"
	callDenied      callOutcome = "denied"      // no such calls port, or no working link from it
	callFormat      callOutcome = "format"      // the request broke the port's in shape
	callNotHere     callOutcome = "not_here"    // init, health, a check or a trial run
	callFailed      callOutcome = "failed"      // the serving run failed, a limit, or the node could not send it
	callUnavailable callOutcome = "unavailable" // nobody serves the port
)

// callOutcomes are the outcomes the runner's codes stand for.
var callOutcomes = map[runner.CallCode]callOutcome{
	runner.CallAnswered:    callAnswered,
	runner.CallDenied:      callDenied,
	runner.CallFormat:      callFormat,
	runner.CallNotHere:     callNotHere,
	runner.CallFailed:      callFailed,
	runner.CallUnavailable: callUnavailable,
}

// servedOutcome is what became of a request a serving module got, for the
// metrics: a closed set.
type servedOutcome string

// Served outcomes.
const (
	servedOK     servedOutcome = "ok"
	servedFailed servedOutcome = "failed" // the run failed, or the module was not running
	servedBad    servedOutcome = "bad"    // the request does not fit the port's in shape; nothing ran
	servedDeep   servedOutcome = "deep"   // the chain is too deep; nothing ran
	servedLate   servedOutcome = "late"   // the caller had given up; nothing ran
)

// callPort is the runner's CallPort of one loaded module. The runner has
// checked the port, the size, the shape and the run's limit before the
// port.
type callPort struct {
	h *Host
	l *loaded
}

// Call implements runner.CallPort. Only a run calls: init and health carry
// none and get CallNotHere. The port needs a link that works; the request
// goes to the endpoint the link names with the run's identity, its depth and
// the time the caller gives up, and the call waits until then: the run's own
// deadline less a second to settle.
func (p callPort) Call(ctx context.Context, port string, request json.RawMessage) (json.RawMessage, runner.CallCode) {
	r, ok := runOf(ctx)
	if !ok {
		return nil, runner.CallNotHere
	}
	ends, linked, err := p.h.callLink(ctx, p.l.short, port)
	if err != nil {
		p.h.d.Log.Warn("modhost: the links cannot be read; a call is not sent", "module", p.l.name, "err", err)
		return nil, runner.CallFailed
	}
	if !linked {
		return nil, runner.CallDenied
	}
	subject, err := bus.Call(p.h.d.Node, ends.ToModule, ends.ToPort)
	if err != nil {
		p.h.d.Log.Warn("modhost: a link has no subject; a call is not sent", "module", p.l.name, "port", port, "err", err)
		return nil, runner.CallFailed
	}
	wctx, cancel := callWindow(ctx, p.l.m.Limits.Timeout)
	defer cancel()
	if wctx.Err() != nil {
		return nil, runner.CallFailed // no time is left to wait
	}
	until, _ := wctx.Deadline()
	headers := map[string]string{bus.HeaderFrom: p.h.d.Node + "/" + p.l.name, bus.HeaderPort: port, bus.HeaderRun: r.ID,
		bus.HeaderDepth: strconv.Itoa(r.Depth), bus.HeaderDeadline: strconv.FormatInt(until.UnixMilli(), 10)}
	answer, err := p.h.d.Calls.Request(wctx, subject, headers, request)
	if err != nil {
		return nil, p.h.callRefused(p.l, port, err)
	}
	return answer, runner.CallAnswered
}

// Settled implements runner.CallPort: counts a call by the code the module
// got.
func (p callPort) Settled(ctx context.Context, code runner.CallCode) {
	p.h.in.called(ctx, p.l.name, callOutcomes[code])
}

// callWindow is ctx cut at the run's deadline less callSettle: how long a
// call may wait. A ctx with no deadline gets the module's time limit.
func callWindow(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	if until, ok := ctx.Deadline(); ok {
		return context.WithDeadline(ctx, until.Add(-callSettle))
	}
	return context.WithTimeout(ctx, max(limit-callSettle, 0))
}

// callRefused is the code of a request that got no answer. Nobody serving the port
// and an answer of "unavailable" are the module not running; a timeout and
// any other failure are a call that did not get its answer, and only the
// unexpected ones are logged.
func (h *Host) callRefused(l *loaded, port string, err error) runner.CallCode {
	var ce *bus.CallError
	switch {
	case errors.Is(err, bus.ErrNoResponder):
		return runner.CallUnavailable
	case errors.As(err, &ce):
		if ce.Code == bus.CodeUnavailable {
			return runner.CallUnavailable
		}
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
	default:
		h.d.Log.Warn("modhost: a call was not sent", "module", l.name, "port", port, "err", err)
	}
	return runner.CallFailed
}

// callLink is the end of the link that starts at the calling port of the
// module (short name) and works: the first line of the file from the port
// that is not broken. false when there is none.
func (h *Host) callLink(ctx context.Context, short, port string) (links.Ends, bool, error) {
	ls, mods, problems, err := h.linkState(ctx)
	if err != nil {
		return links.Ends{}, false, err
	}
	broken := make(map[links.Link]bool, len(problems))
	for _, p := range problems {
		broken[p.Link] = true
	}
	for _, f := range ls {
		l := links.Link(f)
		if l.From != short+"."+port || broken[l] {
			continue
		}
		if ends, ok := links.CallEnds(mods, l); ok {
			return ends, true, nil
		}
	}
	return links.Ends{}, false, nil
}

// service is a module's registration with the bus: stopped once.
type service struct {
	once sync.Once
	stop func() error
}

// serve registers l's served ports as the endpoints of a micro service
// named after the module, at the manifest's version; a module with none
// registers nothing. A failure is logged: the module runs, and its callers
// find nobody. It holds h.mu.
func (h *Host) serve(l *loaded) {
	if len(l.m.Ports.Serves) == 0 {
		return
	}
	var eps []bus.Endpoint
	for _, port := range slices.Sorted(maps.Keys(l.m.Ports.Serves)) {
		subject, err := bus.Call(h.d.Node, l.short, port)
		if err != nil {
			h.d.Log.Warn("modhost: a served port has no subject", "module", l.name, "port", port, "err", err)
			continue
		}
		shapes := l.m.Ports.Serves[port]
		eps = append(eps, bus.Endpoint{Name: port, Subject: subject, Metadata: map[string]string{"in": shapes.In.String(), "out": shapes.Out.String()},
			Handler: func(ctx context.Context, r bus.Request) ([]byte, error) { return h.answerCall(ctx, l.name, port, r) }})
	}
	stop, err := h.d.Calls.Serve(l.short, strconv.Itoa(l.m.Version)+".0.0", eps)
	if err != nil {
		h.d.Log.Warn("modhost: a module's served ports are not registered", "module", l.name, "err", err)
		return
	}
	l.service = &service{stop: stop}
}

// unserve removes l's registration. A request in hand is still answered: it
// finds the module gone, or the module that replaced it. It never waits, so
// it may hold h.mu.
func (h *Host) unserve(l *loaded) {
	if l.service == nil {
		return
	}
	l.service.once.Do(func() {
		if err := l.service.stop(); err != nil {
			h.d.Log.Warn("modhost: a module's served ports are not removed", "module", l.name, "err", err)
		}
	})
}

// unserveAll removes every running module's registration, for the node's
// shutdown.
func (h *Host) unserveAll() {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, l := range h.running {
		h.unserve(l)
	}
}

// callInput is the payload of a call's run: the calling port and the
// request, which already fits the port's in shape.
type callInput struct {
	From    string          `json:"from"`
	Request json.RawMessage `json:"request"`
}

// callError is an error answer in the node's fixed words.
func callError(code, why string) error { return &bus.CallError{Code: code, Description: why} }

// answerCall is the handler of a served port of module (full name): it
// answers a request that arrives on the bus. A request past its caller's
// deadline, one of a chain that is too deep and one outside the port's in
// shape start no run and get an error answer. Otherwise the module runs
// with Trigger "call:<port>" one level deeper than its caller, counted and
// toward the crash loop but not escalated: the caller sees the failure.
// The run's result, which the runner has checked against the out shape, is
// the answer. Errors carry the node's words, never the module's text.
func (h *Host) answerCall(ctx context.Context, module, port string, req bus.Request) ([]byte, error) {
	h.mu.RLock()
	l, ok := h.running[module]
	h.mu.RUnlock()
	// A waiting Reload holds the read lock back, so the deadline is read
	// after it: a caller that gave up meanwhile starts nothing.
	if late(req, h.d.Now()) {
		h.in.served(ctx, module, port, servedLate)
		return nil, callError(bus.CodeLate, "the caller had given up")
	}
	shapes, serves := l.servedPort(ok, port)
	if !serves {
		h.in.served(ctx, module, port, servedFailed)
		return nil, callError(bus.CodeUnavailable, "the module is not running")
	}
	depth, _ := strconv.Atoi(req.Headers[bus.HeaderDepth])
	if depth >= maxDepth {
		h.in.served(ctx, module, port, servedDeep)
		h.d.Log.Warn("modhost: a call of a chain deeper than 4 is refused", "module", l.name, "port", port)
		h.chainLine(ctx, l, "a chain of modules deeper than 4 stopped; its calls start no module")
		return nil, callError(bus.CodeDeep, "the chain of modules is too deep")
	}
	if err := shapes.In.Check(req.Payload); err != nil {
		h.in.served(ctx, module, port, servedBad)
		h.d.Log.Warn("modhost: a request does not fit its port; it is refused", "module", l.name, "port", port, "err", err)
		return nil, callError(bus.CodeFormat, "the request does not fit the port")
	}
	return h.runCall(ctx, module, port, depth, req)
}

// late reports whether the request's caller gave up waiting by now. A
// request that names no deadline is never late.
func late(req bus.Request, now time.Time) bool {
	until, err := strconv.ParseInt(req.Headers[bus.HeaderDeadline], 10, 64)
	return err == nil && !now.Before(time.UnixMilli(until))
}

// servedPort is the shapes of the port when the module runs and serves it.
func (l *loaded) servedPort(running bool, port string) (runner.PortCall, bool) {
	if !running {
		return runner.PortCall{}, false
	}
	shapes, ok := l.m.Ports.Serves[port]
	return shapes, ok
}

// runCall runs module for a request that was let through and settles the run
// as the run of the call; the answer is its result.
func (h *Host) runCall(ctx context.Context, module, port string, depth int, req bus.Request) ([]byte, error) {
	caller := strings.TrimPrefix(req.Headers[bus.HeaderFrom], h.d.Node+"/")
	payload, err := json.Marshal(callInput{From: callerEnd(caller, req.Headers[bus.HeaderPort]), Request: req.Payload})
	if err != nil {
		return nil, callError(bus.CodeFailed, "the call failed")
	}
	id := "call:" + event.NewID(h.d.Now())
	r, ok := h.callIf(context.WithoutCancel(ctx), module, pdk.Input{Trigger: "call:" + port, Payload: payload},
		newRun(id, triggerCall, depth+1).byEvent(caller, req.Headers[bus.HeaderRun]).over(callerEnd(caller, req.Headers[bus.HeaderPort])+" → "+modstore.ShortName(module)+"."+port), func() bool { return !late(req, h.d.Now()) })
	if !ok {
		if late(req, h.d.Now()) {
			h.in.served(ctx, module, port, servedLate)
			return nil, callError(bus.CodeLate, "the caller had given up")
		}
		h.in.served(ctx, module, port, servedFailed)
		return nil, callError(bus.CodeUnavailable, "the module is not running")
	}
	o := judge(r.out, r.err, r.l.short+" call")
	if _, err := h.finish(ctx, r, o); err != nil {
		h.d.Log.Error("modhost: disabling a module failed", "module", r.l.name, "err", err)
	}
	if o.failed() || len(r.out.Result) > runner.MaxCallPayload {
		h.in.served(ctx, module, port, servedFailed)
		return nil, callError(bus.CodeFailed, "the serving run failed")
	}
	h.in.served(ctx, module, port, servedOK)
	if len(r.out.Result) == 0 {
		return []byte("null"), nil
	}
	return r.out.Result, nil
}

// callerEnd is the calling port as the module sees it, `<module>.<port>`
// with the module's short name; "?" when a name does not pass the node's
// name pattern. The names come from the bus's headers, which are data.
func callerEnd(caller, port string) string {
	short := modstore.ShortName(caller)
	if !modstore.PlainName(short) || !modstore.PlainName(port) {
		return "?"
	}
	return short + "." + port
}
