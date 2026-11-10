package modhost

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
)

// Kind is how a module's run went wrong, in the node's words: the kinds a
// module may report, and those only the node raises. Anything else a module
// claims is KindException, so no module text becomes a kind.
type Kind string

// Kinds.
const (
	KindException  Kind = pdk.KindException  // a trap, a timeout, an unreadable answer, or the module's own failure
	KindAuthFailed Kind = pdk.KindAuthFailed // the module's credentials were refused
	KindStale      Kind = "stale"            // the module's source answers with old data
	KindFormat     Kind = pdk.KindFormat     // the node found an answer outside its contract
	KindCrashloop  Kind = "module.crashloop" // the node disabled the module
)

// Failures within crashWindow that disable a module (design section 8.3).
const (
	crashFailures = 5
	crashWindow   = time.Hour
)

// Result is how one run ended, for the metrics: a closed set.
type Result string

// Results.
const (
	ResultOK        Result = "ok"        // the answer fits its contract
	ResultFailed    Result = "failed"    // the module reported a failure
	ResultFormat    Result = "format"    // the answer or a cell value broke the contract
	ResultTimeout   Result = "timeout"   // stopped at the time limit
	ResultException Result = "exception" // a trap or an unreadable answer
)

// maxLogged caps the module's own text in a log line.
const maxLogged = 500

// maxFormat caps the node's description of a contract break.
const maxFormat = 300

// outcome is a run judged by the node: the module's text goes only to the
// log; kind and detail are the node's words.
type outcome struct {
	result Result
	kind   Kind
	detail string // the node's words, for the owner and the agent
	format string // the node's description of a contract break
	logged string // the module's own text, for the log only
}

func (o outcome) failed() bool { return o.result != ResultOK }

// callResult is the outcome as the journal counts it.
func (o outcome) callResult() modstore.CallResult {
	return modstore.CallResult{Failed: o.failed(), Exception: o.result == ResultTimeout || o.result == ResultException, Kind: string(o.kind)}
}

// judge words a call's answer; where names the run in a format detail
// ("probe.status", "probe cron").
func judge(out pdk.Output, err error, where string) outcome {
	switch {
	case errors.Is(err, runner.ErrTimeout):
		return outcome{result: ResultTimeout, kind: KindException, detail: "the module ran past its time limit", logged: err.Error()}
	case err != nil:
		return outcome{result: ResultException, kind: KindException,
			detail: "the module stopped with an error (a trap, or an answer the node cannot read)", logged: err.Error()}
	case out.OK:
		return outcome{result: ResultOK}
	case out.Kind == pdk.KindFormat: // the runner's verdict: never the module's claim
		f := where + ": " + text.Truncate(out.Error, maxFormat)
		return outcome{result: ResultFormat, kind: KindFormat, detail: f, format: f, logged: out.ModuleError}
	}
	k := moduleKind(out.Kind)
	return outcome{result: ResultFailed, kind: k, detail: "the module reported a failure of kind " + string(k), logged: out.Error}
}

// initOutcome words a failed init: the runner's error, or a *ModuleError
// whose kind is the node's already.
func initOutcome(err error) outcome {
	if me, ok := errors.AsType[*runner.ModuleError](err); ok {
		k := moduleKind(me.Kind)
		if k == KindFormat {
			return outcome{result: ResultFormat, kind: k, detail: "init: " + text.Truncate(me.Msg, maxFormat)}
		}
		return outcome{result: ResultFailed, kind: k, detail: "its init failed with kind " + string(k)}
	}
	return judge(pdk.Output{}, err, "init")
}

// moduleKind is the kind a module reported, if it is one a module may
// report; otherwise KindException.
func moduleKind(k string) Kind {
	switch Kind(k) {
	case KindAuthFailed, KindStale, KindFormat:
		return Kind(k)
	case KindException, KindCrashloop:
	}
	return KindException
}

// settle does what follows a run: it counts it, logs a failure with the
// module's own text (whatever the verdict, so the operator sees the cause),
// escalates it when its kind does and nobody saw it fail, and disables the
// module on its fifth failure within an hour. A tool call's failure is not
// escalated: the agent has the node's words for it in the turn that made
// the call, and a second turn would only repeat them (owner, 2026-11-10).
// disabled reports the last; an error is the node's failure to disable it.
func (h *Host) settle(ctx context.Context, l *loaded, trig trigger, o outcome) (disabled bool, err error) {
	h.d.Store.Count(ctx, l.name, l.sha, o.callResult())
	h.in.run(ctx, l, trig, o.result)
	if !o.failed() {
		h.recovered(ctx, l.name)
		return false, nil
	}
	l.log.Warn("modhost: a module run failed", "module", l.name, "version", l.version, "trigger", string(trig),
		"result", string(o.result), "kind", string(o.kind), "detail", o.detail, "text", text.Truncate(o.logged, maxLogged))
	if trig.unattended() {
		h.escalate(ctx, l, o.kind, withHint(o.detail, o, l.short))
	}
	if h.failure(l.name) < crashFailures {
		return false, nil
	}
	return true, h.disable(ctx, l)
}

// failure records a failure of module now and returns how many it had
// within the window.
func (h *Host) failure(module string) int {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	now := h.d.Now()
	kept := h.fails[module][:0]
	for _, at := range h.fails[module] {
		if now.Sub(at) < crashWindow {
			kept = append(kept, at)
		}
	}
	h.fails[module] = append(kept, now)
	return len(h.fails[module])
}

// resetFailures forgets module's failures: it starts afresh.
func (h *Host) resetFailures(module string) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	delete(h.fails, module)
}

// crashloopDetail is the node's words for a disabled module.
const crashloopDetail = "it failed 5 times within an hour, so the node turned it off; the owner turns it on with /modules on, or a new version does"

// disable turns l off for good (ruling 8 of the M4b plan): off: true in the
// settings, no instance, no jobs, one module.crashloop event. A module
// already replaced or stopped is left as it is.
func (h *Host) disable(ctx context.Context, l *loaded) error {
	h.mu.Lock()
	if h.running[l.name] != l {
		h.mu.Unlock()
		return nil
	}
	h.unload(ctx, l.name)
	h.mu.Unlock()
	h.setStopped(l.name, stop{reason: ReasonCrashloop, since: h.d.Now()}, true)
	h.in.disabled(ctx, l.name, ReasonCrashloop)
	h.d.Log.Warn("modhost: module disabled after a crash loop", "module", l.name, "version", l.version)
	err := errors.Join(h.d.Settings.SetOff(l.name, true, string(ReasonCrashloop)), h.d.Jobs.DeleteModule(ctx, l.name))
	h.escalate(ctx, l, KindCrashloop, crashloopDetail)
	if err != nil {
		return fmt.Errorf("modhost: disable %s: %w", l.name, err)
	}
	return nil
}

// setStopped records how module stands when it is not running: st's reason,
// or off in the settings (a crash loop stays one while it is off), or
// neither: running, or no longer installed.
func (h *Host) setStopped(module string, st stop, off bool) {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	cur, had := h.stopped[module]
	switch {
	case st.reason != "":
		h.stopped[module] = st
	case off && had && cur.reason == ReasonCrashloop:
	default:
		delete(h.stopped, module)
	}
	if off {
		h.off[module] = true
	} else {
		delete(h.off, module)
	}
}
