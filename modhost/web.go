package modhost

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/igorrius/ganglion/modhttp"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
)

// This file is the http trigger (R-HTTP-1…2, ADR-0110): the modules that
// answer a browser, and one request run as the module's. The listener that
// takes the browser's request, checks it and writes the answer's headers is
// package modhttp, whose refusals ServeHTTP returns; what is here is what
// only the host knows: which modules run, which routes their manifests
// declare and how a run is judged.

// Pages are the running modules that have an http trigger, by short name.
func (h *Host) Pages() []modhttp.Page {
	h.mu.RLock()
	defer h.mu.RUnlock()
	var out []modhttp.Page
	for _, l := range h.running {
		if l.web == nil {
			continue
		}
		out = append(out, modhttp.Page{Short: l.short, Routes: modstore.PagesOf(l.m)})
	}
	slices.SortFunc(out, func(a, b modhttp.Page) int { return strings.Compare(a.Short, b.Short) })
	return out
}

// ServeHTTP runs one request of the module with the short name short, as an
// http trigger, and judges and settles the run like any other (counted,
// crash-looping module turned off); a failure of the run is the node's
// words in the error (R-RUN-14), never the module's. req.Path must be the
// path inside the prefix, already cleaned by the listener; it is matched
// exactly against the manifest's routes, so a module is asked only for what
// it declared.
func (h *Host) ServeHTTP(ctx context.Context, short string, req pdk.HTTPRequest) (pdk.HTTPAnswer, error) {
	l := h.pageOf(short)
	if l == nil {
		return pdk.HTTPAnswer{}, modhttp.ErrNoPage
	}
	allow := routeMethods(l.m, req.Path)
	if len(allow) == 0 {
		return pdk.HTTPAnswer{}, modhttp.ErrNoPage
	}
	if !slices.Contains(allow, req.Method) {
		return pdk.HTTPAnswer{}, &modhttp.MethodError{Allow: allow}
	}
	select {
	case l.web <- struct{}{}:
		defer func() { <-l.web }()
	default:
		return pdk.HTTPAnswer{}, modhttp.ErrBusy
	}
	payload, err := json.Marshal(req)
	if err != nil {
		return pdk.HTTPAnswer{}, fmt.Errorf("modhost: %w", err)
	}
	r, ok := h.call(ctx, l.name, pdk.Input{Trigger: string(triggerHTTP), Payload: payload}, newRun(h.httpID(l.short), triggerHTTP, 0))
	if !ok {
		return pdk.HTTPAnswer{}, modhttp.ErrNoPage
	}
	o := judge(r.out, r.err, l.short+" http")
	var ans pdk.HTTPAnswer
	if !o.failed() {
		if ans, o = answerOf(r.out.Result); o.failed() {
			r.out = pdk.Output{Kind: pdk.KindFormat, Error: o.format}
		}
	}
	h.finishWeb(ctx, r, o)
	switch o.result {
	case ResultOK:
		return ans, nil
	case ResultTimeout:
		return pdk.HTTPAnswer{}, modhttp.ErrTimedOut
	default:
	}
	return pdk.HTTPAnswer{}, modhttp.ErrFailed
}

// answerOf reads a module's result as an answer: nothing unknown in it, a
// status of 200..599 and a body within MaxAnswerBody. A result that is not
// one is a break of the contract, in the node's words.
func answerOf(raw json.RawMessage) (pdk.HTTPAnswer, outcome) {
	var a pdk.HTTPAnswer
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil || dec.More() {
		return pdk.HTTPAnswer{}, formatBreak("the answer is not {status, headers, body}")
	}
	if a.Status < 200 || a.Status > 599 {
		return pdk.HTTPAnswer{}, formatBreak("the status is not within 200..599")
	}
	if len(a.Body) > modhttp.MaxAnswerBody {
		return pdk.HTTPAnswer{}, formatBreak("the body is longer than " + strconv.Itoa(modhttp.MaxAnswerBody) + " bytes")
	}
	return a, outcome{result: ResultOK}
}

// formatBreak is the verdict of an answer outside the http contract.
func formatBreak(why string) outcome {
	f := "http: " + why
	return outcome{result: ResultFormat, kind: KindFormat, detail: f, format: f}
}

// pageOf is the running module with the short name short that has an http
// trigger, or nil.
func (h *Host) pageOf(short string) *loaded {
	h.mu.RLock()
	defer h.mu.RUnlock()
	l := h.running[modstore.LocalPrefix+short]
	if l == nil || l.web == nil {
		return nil
	}
	return l
}

// routeMethods are the methods the manifest's routes declare for path.
func routeMethods(m runner.Manifest, path string) []string {
	var out []string
	for _, t := range m.Triggers {
		if t.HTTP == nil {
			continue
		}
		for _, r := range t.HTTP.Routes {
			if r.Path == path {
				out = append(out, r.Methods...)
			}
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// httpID is a run id no other run has: a restart restarts the counter, so
// the time goes in too.
func (h *Host) httpID(short string) string {
	return "http:" + short + ":" + strconv.FormatInt(h.d.Now().UnixNano(), 36) + "-" + strconv.FormatUint(h.webSeq.Add(1), 10)
}

// finishWeb settles a request's run. It writes the journal row of a failed
// run only: a page polls every few seconds, and a row for each would bury
// the runs the owner asks about. The cells the run wrote are published
// either way.
func (h *Host) finishWeb(ctx context.Context, r ran, o outcome) {
	if _, err := h.settle(ctx, r.l, r.run.Trigger, o); err != nil {
		h.d.Log.Error("modhost: disabling a module failed", "module", r.l.name, "err", err)
	}
	if o.failed() {
		h.record(ctx, r, o)
	}
	h.publishCells(ctx, r.l, r.run)
}
