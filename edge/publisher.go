package edge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"time"
)

// Health is an edge's own view of its link and store.
type Health struct {
	Linked bool
	Events uint64
}

// Publisher sends an edge's snapshot every Every and as soon as its link
// comes up, which tells the core to copy the kept events at once.
type Publisher struct {
	// Edge is the edge's name.
	Edge string
	// Every is edge.telemetry_every.
	Every time.Duration
	// Health reads the link and the stream.
	Health func(ctx context.Context) Health
	// Modules lists the modules the edge runs.
	Modules func(ctx context.Context) ([]Module, error)
	// Metrics writes the node's metrics; nil sends none.
	Metrics func(w io.Writer) error
	// Publish sends data on the edge's telemetry subject.
	Publish func(edge string, data []byte) error
	// Poll is how often the link is looked at; 0 is a second.
	Poll time.Duration
	Now  func() time.Time
	Log  *slog.Logger
}

// Name implements node.Component.
func (p *Publisher) Name() string { return "edge-telemetry" }

// Run sends snapshots until ctx ends.
func (p *Publisher) Run(ctx context.Context) error {
	poll := p.Poll
	if poll <= 0 {
		poll = time.Second
	}
	every := time.NewTicker(p.Every)
	defer every.Stop()
	look := time.NewTicker(poll)
	defer look.Stop()
	linked := false
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-every.C:
			h := p.Health(ctx)
			linked = h.Linked
			if h.Linked {
				p.send(ctx, h)
			}
		case <-look.C:
			h := p.Health(ctx)
			if h.Linked && !linked {
				p.Log.Info("edge: link up", "edge", p.Edge)
				p.send(ctx, h)
			} else if !h.Linked && linked {
				p.Log.Warn("edge: link down; events are kept", "edge", p.Edge, "events", h.Events)
			}
			linked = h.Linked
		}
	}
}

// send publishes one snapshot; a failure is logged, the next one comes.
func (p *Publisher) send(ctx context.Context, h Health) {
	data, err := p.snapshot(ctx, h)
	if err == nil {
		err = p.Publish(p.Edge, data)
	}
	if err != nil {
		p.Log.Warn("edge: telemetry not sent", "err", err)
	}
}

// snapshot is the encoded snapshot; metrics that would make it too large
// are left out.
func (p *Publisher) snapshot(ctx context.Context, h Health) ([]byte, error) {
	s := Snapshot{V: Version, At: p.Now().UTC(), Every: p.Every, Linked: h.Linked, Events: h.Events}
	mods, err := p.Modules(ctx)
	if err != nil {
		p.Log.Warn("edge: modules not read for telemetry", "err", err)
	}
	s.Modules = mods
	if p.Metrics != nil {
		var buf bytes.Buffer
		if err := p.Metrics(&buf); err != nil {
			p.Log.Warn("edge: metrics not read for telemetry", "err", err)
		}
		s.Metrics = buf.String()
	}
	data, err := json.Marshal(s)
	if err == nil && len(data) > MaxSnapshot {
		p.Log.Warn("edge: metrics too large for telemetry; sent without them", "bytes", len(data))
		s.Metrics = ""
		data, err = json.Marshal(s)
	}
	return data, err
}
