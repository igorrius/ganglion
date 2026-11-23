package edge

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Caps on what the core keeps of its edges (ADR-0114).
const (
	// MaxEdges is how many edges the core keeps snapshots of.
	MaxEdges = 64
	// MaxSeries is how many series of one edge the core serves.
	MaxSeries = 4000
	// lateAfter is how many intervals of silence make an edge silent.
	lateAfter = 3
	// labelNode is the label the core gives an edge's series.
	labelNode = "node"
	// varName is the edge's name in a /status line.
	varName = "name"
	// maxModulesShown is how many module names one /status line lists.
	maxModulesShown = 5
)

// Metrics is where the core keeps the edges' series (telemetry.Network).
type Metrics interface {
	Set(node string, mfs []*dto.MetricFamily)
}

// Lag is how far the core's copy of an edge's events is behind.
type Lag struct {
	Edge string
	// Waiting is how many events the edge holds that the core has not.
	Waiting uint64
}

// Collector is the core's side of the edges' telemetry.
type Collector struct {
	// Watch delivers each snapshot with the edge's name from the subject.
	Watch func(fn func(edge string, data []byte)) (stop func() error, err error)
	// Resync starts the copy of an edge's events again at once, sourcing
	// an edge seen for the first time.
	Resync func(ctx context.Context, edge string) error
	// Lags reads how far the copy of each sourced edge is behind; edges
	// sourced in an earlier run show here before they are heard.
	Lags func(ctx context.Context) ([]Lag, error)
	// Metrics keeps the edges' series; nil keeps none.
	Metrics Metrics
	Now     func() time.Time
	Log     *slog.Logger

	once     sync.Once
	mu       sync.Mutex
	edges    map[string]*heard
	resyncs  chan string
	received metric.Int64Counter
}

// heard is the latest of one edge.
type heard struct {
	snap Snapshot
	at   time.Time
}

// Outcomes of a snapshot, the "outcome" attribute.
const (
	outcomeOK      = "ok"
	outcomeBad     = "bad"     // not a snapshot of this version, or too large
	outcomeMetrics = "metrics" // taken, but its metrics were refused
	outcomeFull    = "full"    // a new edge past MaxEdges
)

func (c *Collector) init() {
	c.once.Do(func() {
		c.edges = map[string]*heard{}
		c.resyncs = make(chan string, MaxEdges)
		c.received, _ = otel.Meter("ganglion/edge").Int64Counter("ganglion.edge.snapshots",
			metric.WithDescription("Telemetry snapshots the core got from its edges, by outcome (ok, bad: not a snapshot or too large, metrics: its metrics were refused, full: a new edge past the limit)."))
	})
}

// Name implements node.Component and status.Source.
func (c *Collector) Name() string { return "edges" }

// Run takes snapshots until ctx ends, and resyncs the edges heard after a
// silence, one at a time.
func (c *Collector) Run(ctx context.Context) error {
	c.init()
	stop, err := c.Watch(c.take)
	if err != nil {
		return err
	}
	defer func() { _ = stop() }()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-c.resyncs:
			if err := c.Resync(ctx, e); err != nil {
				c.Log.Warn("edge: events not resynced", "edge", e, "err", err)
			}
		}
	}
}

// take keeps one snapshot of edge.
func (c *Collector) take(edge string, data []byte) {
	c.init()
	ctx := context.Background()
	s, err := decode(data)
	if err != nil {
		c.count(ctx, outcomeBad)
		return
	}
	mfs, mErr := parseMetrics(edge, s.Metrics)
	s.Metrics = ""
	now := c.Now()
	c.mu.Lock()
	h, known := c.edges[edge]
	if !known && len(c.edges) >= MaxEdges {
		c.mu.Unlock()
		c.count(ctx, outcomeFull)
		return
	}
	resync := !known || now.Sub(h.at) > lateAfter*h.snap.Every
	c.edges[edge] = &heard{snap: s, at: now}
	c.mu.Unlock()
	if resync {
		select {
		case c.resyncs <- edge:
		default: // a resync is pending already
		}
	}
	if mErr != nil {
		c.count(ctx, outcomeMetrics)
		c.Log.Warn("edge: metrics refused", "edge", edge, "err", mErr)
		return
	}
	if c.Metrics != nil {
		c.Metrics.Set(edge, mfs)
	}
	c.count(ctx, outcomeOK)
}

func (c *Collector) count(ctx context.Context, outcome string) {
	c.received.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// parseMetrics reads an edge's text, caps it and labels every series with
// the edge's name, replacing a node label the edge set itself.
func parseMetrics(edge, text string) ([]*dto.MetricFamily, error) {
	if text == "" {
		return nil, nil
	}
	p := expfmt.NewTextParser(model.LegacyValidation)
	byName, err := p.TextToMetricFamilies(strings.NewReader(text))
	if err != nil {
		return nil, fmt.Errorf("edge: metrics: not the Prometheus text format")
	}
	series := 0
	out := make([]*dto.MetricFamily, 0, len(byName))
	for _, mf := range byName {
		series += len(mf.Metric)
		if series > MaxSeries {
			return nil, fmt.Errorf("edge: metrics: more than %d series", MaxSeries)
		}
		for _, m := range mf.Metric {
			m.Label = slices.DeleteFunc(m.Label, func(l *dto.LabelPair) bool { return l.GetName() == labelNode })
			name, value := labelNode, edge
			m.Label = append(m.Label, &dto.LabelPair{Name: &name, Value: &value})
		}
		out = append(out, mf)
	}
	return out, nil
}

// Status implements status.Source: a line per edge, in name order. An edge
// the core sources but has not heard since it started is silent too.
func (c *Collector) Status(ctx context.Context, _ status.Scope) ([]status.Check, error) {
	c.init()
	waiting := map[string]uint64{}
	lags, err := c.Lags(ctx)
	if err != nil {
		c.Log.Warn("edge: lag not read for /status", "err", err)
	}
	for _, l := range lags {
		waiting[l.Edge] = l.Waiting
	}
	now := c.Now()
	c.mu.Lock()
	names := make([]string, 0, len(c.edges)+len(lags))
	for n := range c.edges {
		names = append(names, n)
	}
	for _, l := range lags {
		if _, ok := c.edges[l.Edge]; !ok {
			names = append(names, l.Edge)
		}
	}
	slices.Sort(names)
	checks := make([]status.Check, 0, len(names))
	for _, n := range names {
		h, ok := c.edges[n]
		switch {
		case !ok:
			checks = append(checks, status.Check{Section: status.SectionProblems, State: status.Warn, Line: texts.StatusEdgeUnheard, Vars: texts.Vars{varName: n}})
		case now.Sub(h.at) > lateAfter*h.snap.Every:
			checks = append(checks, status.Check{Section: status.SectionProblems, State: status.Warn, Line: texts.StatusEdgeSilent, Since: h.at,
				Vars: texts.Vars{varName: n, "held": fmt.Sprint(h.snap.Events)}})
		default:
			checks = append(checks, status.Check{Section: status.SectionProblems, State: status.OK, Line: texts.StatusEdgeOK,
				Vars: texts.Vars{varName: n, "modules": modulesLine(h.snap.Modules), "waiting": fmt.Sprint(waiting[n])}})
		}
	}
	c.mu.Unlock()
	return checks, nil
}

// modulesLine lists the modules as "name version", the first few of them
// and how many more; "-" for none.
func modulesLine(mods []Module) string {
	if len(mods) == 0 {
		return "-"
	}
	parts := make([]string, 0, min(len(mods), maxModulesShown)+1)
	for i, m := range mods {
		if i == maxModulesShown {
			parts = append(parts, fmt.Sprintf("+%d", len(mods)-i))
			break
		}
		parts = append(parts, m.Name+" "+m.Version)
	}
	return strings.Join(parts, ", ")
}

var _ status.Source = (*Collector)(nil)
