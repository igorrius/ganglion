package modhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/internal/backoff"
	"github.com/igorrius/ganglion/links"
	"github.com/igorrius/ganglion/pdk"
)

// This file is the delivery of events over links (R-BUS-6, ADR-0108): one
// durable consumer of the EVENTS stream for every link that works and whose
// receiver runs. The host reconciles them with the links and the modules:
// at start, after every reload and after a link is added or removed. A
// link whose receiver is off keeps its consumer on the server, unread, so
// its events wait; a link that is gone from the file loses it. The same
// reconcile runs the cell listeners' consumers (cellevents.go).

// Consumers is the bus as the link consumers need it; bus/natsbus
// implements it.
type Consumers interface {
	// RunConsumer reads until ctx ends and hands each message to h.
	RunConsumer(ctx context.Context, spec bus.ConsumerSpec, h bus.DeliveryHandler) error
	DeleteConsumer(ctx context.Context, stream, name string) error
	// ConsumerNames are the stream's durable consumers whose name starts
	// with prefix.
	ConsumerNames(ctx context.Context, stream, prefix string) ([]string, error)
}

// noConsumers is the bus of a host that has none: no link carries events.
type noConsumers struct{}

func (noConsumers) RunConsumer(ctx context.Context, _ bus.ConsumerSpec, _ bus.DeliveryHandler) error {
	<-ctx.Done()
	return nil
}
func (noConsumers) DeleteConsumer(context.Context, string, string) error            { return nil }
func (noConsumers) ConsumerNames(context.Context, string, string) ([]string, error) { return nil, nil }

// Settings of a link's consumer.
const (
	// linkPrefix starts the name of every link's consumer.
	linkPrefix = "link_"
	// linkAckSlack is added to the receiver's time limit for the consumer's
	// ack wait: the run, then its record and ack.
	linkAckSlack = 30 * time.Second
	// linkRestart is the wait before a consumer that could not be made is
	// tried again.
	linkRestart = 5 * time.Second
)

// linkRetry is the wait before an event whose run failed is delivered
// again: from 1 s to 5 min, doubling with the delivery count.
var linkRetry = backoff.Policy{Min: time.Second, Max: 5 * time.Minute}

// eventDeliveryOutcome is what became of an event a link's consumer
// handled, for the metrics.
type eventDeliveryOutcome string

// Event delivery outcomes.
const (
	eventDelivered eventDeliveryOutcome = "ok"     // the receiver ran and succeeded
	eventRetried   eventDeliveryOutcome = "failed" // the run failed, or the receiver was not running: it comes again
	eventBad       eventDeliveryOutcome = "bad"    // the payload does not fit the receiver's port; acked, nothing ran
	eventDeep      eventDeliveryOutcome = "deep"   // the chain is too deep; acked, nothing ran
)

// linkConsumerName is the durable consumer's name of a link: the first 20
// hex digits of the digest of its two ends.
func linkConsumerName(l links.Link) string {
	sum := sha256.Sum256([]byte(l.From + " " + l.To))
	return linkPrefix + hex.EncodeToString(sum[:])[:20]
}

// wantedLink is a consumer that should run.
type wantedLink struct {
	link links.Link
	ends links.Ends
	spec bus.ConsumerSpec
}

// runningLink is a consumer's goroutine.
type runningLink struct {
	spec   bus.ConsumerSpec
	ends   links.Ends
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{} // closed when the goroutine has returned
}

// linkSet is the running consumers: one goroutine each with its own cancel.
// Its mutex is held for short steps only, never while waiting for a
// goroutine.
type linkSet struct {
	mu      sync.Mutex
	running map[string]*runningLink
	cells   map[string]*runningCells // the listeners' consumers, by name
	ctx     context.Context          // the host's: Shutdown ends it, and with it every consumer
	end     context.CancelFunc
	wg      sync.WaitGroup
	recon   sync.Mutex // serialises the reconciles: one at a time
}

func newLinkSet() *linkSet {
	ctx, end := context.WithCancel(context.Background())
	return &linkSet{running: map[string]*runningLink{}, cells: map[string]*runningCells{}, ctx: ctx, end: end}
}

// SyncLinks brings the consumers in line with the links of the file and
// the modules that run: it stops those no longer wanted (waiting for them),
// starts the missing ones and deletes the consumers of links that are gone
// from the file. The link tools call it after they write the file, and the
// host after it loads or reloads modules. It never holds the host's lock
// while it waits.
func (h *Host) SyncLinks(ctx context.Context) {
	h.links.recon.Lock()
	defer h.links.recon.Unlock()
	defer h.syncCells(ctx)
	want, all, err := h.wantedLinks(ctx)
	if err != nil {
		h.d.Log.Warn("modhost: the links' consumers are not reconciled: the links or the modules cannot be read", "err", err)
		return
	}
	for _, done := range h.links.retire(want) {
		<-done
	}
	h.links.start(h, want)
	h.dropGone(ctx, all)
}

// wantedLinks are the consumers that should run, by name, and the names of
// every link in the file, whatever its state: those keep their consumer on
// the server.
func (h *Host) wantedLinks(ctx context.Context) (want map[string]wantedLink, all map[string]bool, err error) {
	ls, mods, problems, err := h.linkState(ctx)
	if err != nil {
		return nil, nil, err
	}
	broken := make(map[links.Link]bool, len(problems))
	for _, p := range problems {
		broken[p.Link] = true
	}
	timeouts := h.timeouts()
	want, all = map[string]wantedLink{}, map[string]bool{}
	for _, f := range ls {
		l := links.Link(f)
		name := linkConsumerName(l)
		all[name] = true
		ends, ok := links.EventEnds(mods, l)
		to, running := timeouts[ends.ToModule]
		if broken[l] || !ok || !running {
			continue
		}
		subject, err := bus.Event(h.d.Node, ends.FromModule, ends.FromPort)
		if err != nil {
			h.d.Log.Warn("modhost: a link has no subject; it gets no consumer", "link", l.String(), "err", err)
			continue
		}
		want[name] = wantedLink{link: l, ends: ends, spec: bus.ConsumerSpec{Stream: bus.StreamEvents, Name: name, Description: l.String(),
			Filter: subject, Deliver: bus.DeliverNew, AckWait: to + linkAckSlack, Retry: linkRetry}}
	}
	return want, all, nil
}

// timeouts are the time limits of the running modules by short name.
func (h *Host) timeouts() map[string]time.Duration {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make(map[string]time.Duration, len(h.running))
	for _, l := range h.running {
		out[l.short] = l.m.Limits.Timeout
	}
	return out
}

// retire cancels the consumers that are no longer wanted, whose ack wait
// changed, or whose context already ended, and returns what to wait for.
func (s *linkSet) retire(want map[string]wantedLink) []chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []chan struct{}
	for name, c := range s.running {
		w, ok := want[name]
		if !ok || w.spec.AckWait != c.spec.AckWait || c.ctx.Err() != nil {
			c.cancel()
			out = append(out, c.done)
		}
	}
	return out
}

// start runs the wanted consumers that do not run yet, unless the host was
// shut down.
func (s *linkSet) start(h *Host, want map[string]wantedLink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return
	}
	for name, w := range want {
		if _, ok := s.running[name]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(s.ctx)
		c := &runningLink{spec: w.spec, ends: w.ends, ctx: ctx, cancel: cancel, done: make(chan struct{})}
		s.running[name] = c
		s.wg.Add(1)
		go h.runLinkConsumer(name, w, c)
	}
}

// runLinkConsumer is one consumer's goroutine: it reads until its context
// ends, and starts again after a wait when the bus could not make it.
func (h *Host) runLinkConsumer(name string, w wantedLink, c *runningLink) {
	defer h.links.wg.Done()
	defer func() {
		h.links.mu.Lock()
		if h.links.running[name] == c {
			delete(h.links.running, name)
		}
		h.links.mu.Unlock()
		close(c.done)
	}()
	h.consume(c.ctx, c.spec, "the link "+w.link.String(), func(ctx context.Context, d bus.Delivery) error { return h.deliverEvent(ctx, name, w, d) })
}

// consume reads spec's consumer until ctx ends, and starts again after a
// wait when the bus could not make it; what names it in the log.
func (h *Host) consume(ctx context.Context, spec bus.ConsumerSpec, what string, handler bus.DeliveryHandler) {
	for ctx.Err() == nil {
		err := h.d.Consumers.RunConsumer(ctx, spec, handler)
		if err == nil || ctx.Err() != nil {
			return
		}
		h.d.Log.Warn("modhost: a consumer could not run; trying again", "for", what, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(linkRestart):
		}
	}
}

// stopReceiver cancels the consumers of the links into module (a short
// name) without waiting: it is called from a run's own consumer, which must
// not wait for itself. The consumer's goroutine removes itself; the next
// reconcile starts a consumer again when the module runs.
func (h *Host) stopReceiver(module string) {
	h.links.mu.Lock()
	defer h.links.mu.Unlock()
	for _, c := range h.links.running {
		if c.ends.ToModule == module {
			c.cancel()
		}
	}
}

// dropGone deletes the durable consumers of links that are no longer in
// the file.
func (h *Host) dropGone(ctx context.Context, all map[string]bool) {
	names, err := h.d.Consumers.ConsumerNames(ctx, bus.StreamEvents, linkPrefix)
	if err != nil {
		h.d.Log.Warn("modhost: the links' consumers on the bus cannot be listed", "err", err)
		return
	}
	for _, n := range names {
		if all[n] {
			continue
		}
		if err := h.d.Consumers.DeleteConsumer(ctx, bus.StreamEvents, n); err != nil {
			h.d.Log.Warn("modhost: the consumer of a removed link is not deleted", "consumer", n, "err", err)
		}
	}
}

// Shutdown removes the modules' served ports from the bus, stops every
// link's consumer and waits for their goroutines, or for ctx. A message in
// hand is neither acked nor refused: the server sends it again after the ack
// wait.
func (h *Host) Shutdown(ctx context.Context) error {
	h.unserveAll()
	h.links.end()
	done := make(chan struct{})
	go func() {
		h.links.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("modhost: the links' consumers did not stop: %w", ctx.Err())
	}
}

// eventInput is the payload of an event's run: where it came from and the
// event itself, which already fits the receiver's port.
type eventInput struct {
	From  string          `json:"from"`
	Event json.RawMessage `json:"event"`
}

// errReceiverGone is an event whose receiver is not running now.
var errReceiverGone = errors.New("the receiver is not running")

// errReceiverFailed is an event whose run failed.
var errReceiverFailed = errors.New("the receiver's run failed")

// deliverEvent is the handler of a link's consumer: it runs the receiver
// with the event, one level deeper in the chain than its producer's run, as
// an unattended run (counted, escalated, towards the crash loop). A nil
// return acks the event. An event past the depth limit and one that does
// not fit the receiver's port are acked and never run. A run that fails, or
// a receiver that is not running, is an error: the event is delivered
// again after the back-off, without a limit. Delivery is at least once.
func (h *Host) deliverEvent(ctx context.Context, name string, w wantedLink, d bus.Delivery) error {
	depth, _ := strconv.Atoi(d.Headers[bus.HeaderDepth])
	h.mu.RLock()
	l, ok := h.running[fullName(w.ends.ToModule)]
	h.mu.RUnlock()
	if !ok {
		h.in.eventDelivered(ctx, name, eventRetried)
		return errReceiverGone
	}
	if depth >= maxDepth {
		h.in.eventDelivered(ctx, name, eventDeep)
		h.d.Log.Warn("modhost: an event of a chain deeper than 4 is dropped", "link", w.link.String(), "module", l.name)
		h.chainLine(ctx, l, "a chain of modules deeper than 4 stopped; its events start no module")
		return nil
	}
	if err := l.m.Ports.Accepts[w.ends.ToPort].Check(d.Payload); err != nil {
		h.in.eventDelivered(ctx, name, eventBad)
		h.d.Log.Warn("modhost: an event does not fit its receiver's port; it is dropped", "link", w.link.String(), "module", l.name, "err", err)
		return nil
	}
	payload, err := json.Marshal(eventInput{From: w.link.From, Event: d.Payload})
	if err != nil {
		return err
	}
	return h.runEvent(ctx, name, w, d, depth, payload)
}

// runEvent runs the receiver of w with an event's payload and settles the
// run. The run does not end with ctx: stopping a consumer must not fail a
// run that is half done, and the module's own time limit bounds it.
func (h *Host) runEvent(ctx context.Context, name string, w wantedLink, d bus.Delivery, depth int, payload []byte) error {
	id := "event:" + name + ":" + strconv.FormatUint(d.Seq, 10)
	if d.Tries > 1 {
		id += ":" + strconv.FormatUint(d.Tries, 10) // a retry is its own row in the journal
	}
	producer := strings.TrimPrefix(d.Headers[bus.HeaderFrom], h.d.Node+"/")
	in := pdk.Input{Trigger: "event:" + w.ends.ToPort, Payload: payload}
	r, ok := h.call(context.WithoutCancel(ctx), fullName(w.ends.ToModule), in,
		newRun(id, triggerEvent, depth+1).byEvent(producer, d.Headers[bus.HeaderRun]).over(w.link.String()))
	if !ok {
		h.in.eventDelivered(ctx, name, eventRetried)
		return errReceiverGone
	}
	o := judge(r.out, r.err, r.l.short+" event")
	if _, err := h.finish(ctx, r, o); err != nil {
		h.d.Log.Error("modhost: disabling a module failed", "module", r.l.name, "err", err)
	}
	if o.failed() {
		h.in.eventDelivered(ctx, name, eventRetried)
		return errReceiverFailed
	}
	h.in.eventDelivered(ctx, name, eventDelivered)
	return nil
}
