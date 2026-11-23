package natsbus

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// edgeStreamMaxBytes caps each stream an edge keeps while its link is down;
// the oldest messages go first. The core's stream keeps no more than the
// age limit either way (ADR-0115).
const edgeStreamMaxBytes = 256 << 20

// IsEdge reports whether this bus is an edge's leaf node.
func (b *Bus) IsEdge() bool { return b.cfg.Leaf != nil }

// edgeAPI is the JetStream API prefix of an edge's domain, as the core
// reaches it across the leaf link.
func edgeAPI(edge string) string { return "$JS." + edge + ".API" }

// ensureStream makes one stream as cfg says. The stream of an edge is
// capped in size; the core's EVENTS stream keeps the edges it sources,
// which Ensure must not drop (ADR-0115).
func (b *Bus) ensureStream(ctx context.Context, cfg jetstream.StreamConfig) error {
	if b.IsEdge() {
		cfg.MaxBytes = edgeStreamMaxBytes
		cfg.Discard = jetstream.DiscardOld
	} else if cfg.Name == bus.StreamEvents {
		if s, err := b.js.Stream(ctx, cfg.Name); err == nil {
			cfg.Sources = s.CachedInfo().Config.Sources
		} else if !errors.Is(err, jetstream.ErrStreamNotFound) {
			return err
		}
	}
	_, err := b.js.CreateOrUpdateStream(ctx, cfg)
	return err
}

// SourceEdge makes the core's EVENTS stream source the EVENTS stream in
// the JetStream domain of edge (R-EDGE-7): what the edge kept while its
// link was down arrives when the link is up, in order, and the stream's
// duplicate window drops what arrived twice. Sourcing an edge twice does
// nothing the second time; an edge that is not reachable yet is caught up
// when it connects.
func (b *Bus) SourceEdge(ctx context.Context, edge string) error {
	return b.changeSources(ctx, edge, true)
}

// UnsourceEdge stops sourcing edge's events, for a revoked edge. What was
// already copied stays.
func (b *Bus) UnsourceEdge(ctx context.Context, edge string) error {
	return b.changeSources(ctx, edge, false)
}

func (b *Bus) changeSources(ctx context.Context, edge string, add bool) error {
	if b.nc == nil {
		return bus.ErrNotConnected
	}
	if b.IsEdge() {
		return errors.New("natsbus: an edge does not source other edges")
	}
	if err := bus.CheckEdgeName(edge); err != nil {
		return err
	}
	s, err := b.js.Stream(ctx, bus.StreamEvents)
	if err != nil {
		return fmt.Errorf("natsbus: stream %s: %w", bus.StreamEvents, err)
	}
	cfg := s.CachedInfo().Config
	api := edgeAPI(edge)
	has := slices.ContainsFunc(cfg.Sources, func(x *jetstream.StreamSource) bool { return x.External != nil && x.External.APIPrefix == api })
	if has == add {
		return nil
	}
	if add {
		cfg.Sources = append(cfg.Sources, &jetstream.StreamSource{Name: bus.StreamEvents, External: &jetstream.ExternalStream{APIPrefix: api}})
	} else {
		cfg.Sources = slices.DeleteFunc(cfg.Sources, func(x *jetstream.StreamSource) bool { return x.External != nil && x.External.APIPrefix == api })
	}
	if _, err := b.js.UpdateStream(ctx, cfg); err != nil {
		return fmt.Errorf("natsbus: source edge %s: %w", edge, err)
	}
	return nil
}

// ResyncEdge starts the copy of edge's events again at once, for an edge
// whose link has just come back: the server retries a source that failed
// on its own, but waits up to two minutes between tries (ADR-0115). The
// copy goes on from the last event the core holds, so nothing is copied
// twice. An edge the core does not source is sourced.
func (b *Bus) ResyncEdge(ctx context.Context, edge string) error {
	if err := b.changeSources(ctx, edge, false); err != nil {
		return err
	}
	return b.changeSources(ctx, edge, true)
}

// EdgeSource is how the core's copy of one edge's events is doing.
type EdgeSource struct {
	// Edge is the edge's name.
	Edge string
	// Lag is how many events the edge holds that the core has not copied
	// yet; read while the link is up.
	Lag uint64
	// Idle is how long the source has been quiet: large while the link is
	// down.
	Idle time.Duration
}

// EdgeSources reports each sourced edge, in the order they were added.
func (b *Bus) EdgeSources(ctx context.Context) ([]EdgeSource, error) {
	if b.nc == nil {
		return nil, bus.ErrNotConnected
	}
	s, err := b.js.Stream(ctx, bus.StreamEvents)
	if err != nil {
		return nil, fmt.Errorf("natsbus: stream %s: %w", bus.StreamEvents, err)
	}
	info, err := s.Info(ctx)
	if err != nil {
		return nil, fmt.Errorf("natsbus: stream %s: %w", bus.StreamEvents, err)
	}
	var out []EdgeSource
	for _, si := range info.Sources {
		if si.External == nil {
			continue
		}
		edge := trimAPI(si.External.APIPrefix)
		if edge == "" {
			continue
		}
		out = append(out, EdgeSource{Edge: edge, Lag: si.Lag, Idle: si.Active})
	}
	return out, nil
}

// trimAPI is the edge's name in a domain API prefix, or "".
func trimAPI(prefix string) string {
	const head, tail = "$JS.", ".API"
	if len(prefix) <= len(head)+len(tail) || prefix[:len(head)] != head || prefix[len(prefix)-len(tail):] != tail {
		return ""
	}
	return prefix[len(head) : len(prefix)-len(tail)]
}

// EdgeHealth is what an edge says of its own link and store.
type EdgeHealth struct {
	// Linked is true while the leaf connection to the core is up.
	Linked bool
	// Events is how many events the edge's stream holds.
	Events uint64
}

// EdgeHealth reads the edge's link and its EVENTS stream. A reading that
// fails leaves its part zero.
func (b *Bus) EdgeHealth(ctx context.Context) EdgeHealth {
	var h EdgeHealth
	if b.srv != nil {
		if lz, err := b.srv.Leafz(&server.LeafzOptions{}); err == nil {
			h.Linked = lz.NumLeafs > 0
		}
	}
	if b.js != nil {
		if s, err := b.js.Stream(ctx, bus.StreamEvents); err == nil {
			if info, err := s.Info(ctx); err == nil {
				h.Events = info.State.Msgs
			}
		}
	}
	return h
}

// PublishTelemetry sends an edge's snapshot to g.tel.<edge> over plain
// NATS (R-OBS-9): nothing keeps it, and none goes out while the link is
// down.
func (b *Bus) PublishTelemetry(edge string, data []byte) error {
	if b.nc == nil {
		return bus.ErrNotConnected
	}
	subj, err := bus.Telemetry(edge)
	if err != nil {
		return err
	}
	return b.nc.Publish(subj, data)
}

// WatchTelemetry calls fn with every snapshot an edge sends, and the edge's
// name from the subject, never from the payload, until stop is called.
// fn runs on one goroutine, one snapshot at a time.
func (b *Bus) WatchTelemetry(fn func(edge string, data []byte)) (stop func() error, err error) {
	if b.nc == nil {
		return nil, bus.ErrNotConnected
	}
	sub, err := b.nc.Subscribe(bus.SubjectsTelemetry, func(m *nats.Msg) {
		edge := strings.TrimPrefix(m.Subject, bus.TelemetryPrefix)
		if bus.CheckEdgeName(edge) != nil {
			return
		}
		fn(edge, m.Data)
	})
	if err != nil {
		return nil, fmt.Errorf("natsbus: telemetry: %w", err)
	}
	return sub.Unsubscribe, nil
}
