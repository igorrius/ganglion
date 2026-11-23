package edge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
	dto "github.com/prometheus/client_model/go"
)

func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

type sink struct {
	mu  sync.Mutex
	got map[string][]*dto.MetricFamily
}

func (s *sink) Set(node string, mfs []*dto.MetricFamily) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.got == nil {
		s.got = map[string][]*dto.MetricFamily{}
	}
	s.got[node] = mfs
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func snapshot(t *testing.T, s Snapshot) []byte {
	t.Helper()
	s.V = Version
	if s.Every == 0 {
		s.Every = 30 * time.Second
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func newCollector(c *clock, m Metrics, resynced *[]string) *Collector {
	return &Collector{
		Watch:  func(func(string, []byte)) (func() error, error) { return func() error { return nil }, nil },
		Resync: func(_ context.Context, e string) error { *resynced = append(*resynced, e); return nil },
		Lags: func(context.Context) ([]Lag, error) {
			return []Lag{{Edge: "garage", Waiting: 2}, {Edge: "old", Waiting: 0}}, nil
		},
		Metrics: m, Now: c.now, Log: quiet(),
	}
}

// resyncsQueued drains the queued resyncs as Run would.
func resyncsQueued(c *Collector) []string {
	var out []string
	for {
		select {
		case e := <-c.resyncs:
			out = append(out, e)
		default:
			return out
		}
	}
}

func lines(t *testing.T, c *Collector) map[string]status.Check {
	t.Helper()
	checks, err := c.Status(context.Background(), status.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]status.Check{}
	for _, ch := range checks {
		out[ch.Vars["name"]] = ch
	}
	return out
}

// An edge heard for the first time is resynced and shown with its modules
// and the events on the way; a late one is silent; one heard again after
// the silence is resynced again; a sourced edge never heard is unheard.
func TestCollectorLinesAndResyncs(t *testing.T) {
	clk := &clock{t: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)}
	var resynced []string
	c := newCollector(clk, nil, &resynced)
	c.init()
	c.take("garage", snapshot(t, Snapshot{Linked: true, Events: 7, Modules: []Module{{Name: "meter", Version: "1.2.0"}}}))
	if got := resyncsQueued(c); len(got) != 1 || got[0] != "garage" {
		t.Fatalf("resyncs = %v, want garage", got)
	}
	l := lines(t, c)
	if g := l["garage"]; g.State != status.OK || g.Line != texts.StatusEdgeOK || g.Vars["modules"] != "meter 1.2.0" || g.Vars["waiting"] != "2" {
		t.Fatalf("garage = %+v", g)
	}
	if o := l["old"]; o.State != status.Warn || o.Line != texts.StatusEdgeUnheard {
		t.Fatalf("old = %+v", o)
	}

	clk.t = clk.t.Add(30 * time.Second)
	c.take("garage", snapshot(t, Snapshot{Linked: true}))
	if got := resyncsQueued(c); len(got) != 0 {
		t.Fatalf("an edge on time was resynced: %v", got)
	}
	// A short outage: one snapshot missed, the next one late; the link
	// came back, so the kept events are copied at once.
	clk.t = clk.t.Add(60 * time.Second)
	c.take("garage", snapshot(t, Snapshot{Linked: true}))
	if got := resyncsQueued(c); len(got) != 1 {
		t.Fatalf("an edge back after a short gap was not resynced: %v", got)
	}
	// The edge says its link just came up, on time or not.
	clk.t = clk.t.Add(10 * time.Second)
	c.take("garage", snapshot(t, Snapshot{Linked: true, Relinked: true}))
	if got := resyncsQueued(c); len(got) != 1 {
		t.Fatalf("a relinked edge was not resynced: %v", got)
	}
	heardAt := clk.t
	clk.t = clk.t.Add(2 * time.Minute)
	if g := lines(t, c)["garage"]; g.State != status.Warn || g.Line != texts.StatusEdgeSilent || !g.Since.Equal(heardAt) {
		t.Fatalf("silent garage = %+v", g)
	}
	c.take("garage", snapshot(t, Snapshot{Linked: true}))
	if got := resyncsQueued(c); len(got) != 1 {
		t.Fatalf("an edge back after a silence was not resynced: %v", got)
	}
}

// An edge cannot claim an interval that hides its silence.
func TestDecodeClampsTheInterval(t *testing.T) {
	for every, want := range map[time.Duration]time.Duration{time.Hour * 24 * 365: maxEvery, time.Millisecond: minEvery, time.Minute: time.Minute} {
		s, err := decode(snapshot(t, Snapshot{Every: every}))
		if err != nil || s.Every != want {
			t.Errorf("every %s: got %s, %v; want %s", every, s.Every, err, want)
		}
	}
	clk := &clock{t: time.Now()}
	var resynced []string
	c := newCollector(clk, nil, &resynced)
	c.init()
	c.take("garage", snapshot(t, Snapshot{Every: 100 * time.Hour}))
	clk.t = clk.t.Add(lateAfter*maxEvery + time.Second)
	if g := lines(t, c)["garage"]; g.Line != texts.StatusEdgeSilent {
		t.Fatalf("an edge claiming a long interval is not silent: %+v", g)
	}
}

// What is not a snapshot is dropped; a new edge past the limit is not kept.
func TestCollectorRefuses(t *testing.T) {
	var resynced []string
	c := newCollector(&clock{t: time.Now()}, nil, &resynced)
	c.init()
	for _, bad := range [][]byte{[]byte("{"), []byte(`{"v":2,"every":1}`), []byte(`{"v":1}`), []byte(strings.Repeat(" ", MaxSnapshot+1))} {
		c.take("garage", bad)
	}
	if len(c.edges) != 0 {
		t.Fatalf("a bad snapshot was kept: %v", c.edges)
	}
	for i := range MaxEdges + 1 {
		c.take(fmt.Sprintf("e%d", i), snapshot(t, Snapshot{}))
	}
	if len(c.edges) != MaxEdges {
		t.Fatalf("kept %d edges, want %d", len(c.edges), MaxEdges)
	}
}

// The edge's series get the edge's name as node, replacing a node label of
// its own; too many series are refused, the snapshot itself is kept.
func TestCollectorLabelsMetrics(t *testing.T) {
	var resynced []string
	m := &sink{}
	c := newCollector(&clock{t: time.Now()}, m, &resynced)
	c.init()
	text := "# TYPE up gauge\nup{node=\"core\",job=\"x\"} 1\n"
	c.take("garage", snapshot(t, Snapshot{Metrics: text}))
	mfs := m.got["garage"]
	if len(mfs) != 1 {
		t.Fatalf("families = %v", mfs)
	}
	var nodes []string
	for _, l := range mfs[0].Metric[0].Label {
		if l.GetName() == "node" {
			nodes = append(nodes, l.GetValue())
		}
	}
	if len(nodes) != 1 || nodes[0] != "garage" {
		t.Fatalf("node labels = %v", nodes)
	}

	var big strings.Builder
	big.WriteString("# TYPE many counter\n")
	for i := range MaxSeries + 1 {
		fmt.Fprintf(&big, "many{i=\"%d\"} 1\n", i)
	}
	c.take("shed", snapshot(t, Snapshot{Metrics: big.String()}))
	if _, ok := m.got["shed"]; ok {
		t.Fatal("too many series were kept")
	}
	if _, ok := c.edges["shed"]; !ok {
		t.Fatal("the snapshot was dropped with its metrics")
	}
	c.take("junk", snapshot(t, Snapshot{Metrics: "not { metrics"}))
	if _, ok := m.got["junk"]; ok {
		t.Fatal("unparsable metrics were kept")
	}
}

// The publisher sends at once when the link comes up, and on its interval
// only while linked; metrics too large for a snapshot are left out.
func TestPublisherSendsOnLinkUp(t *testing.T) {
	var (
		mu     sync.Mutex
		linked bool
		sent   [][]byte
	)
	p := &Publisher{
		Edge: "garage", Every: time.Hour, Poll: 10 * time.Millisecond,
		Health: func(context.Context) Health {
			mu.Lock()
			defer mu.Unlock()
			return Health{Linked: linked, Events: 3}
		},
		Modules: func(context.Context) ([]Module, error) { return []Module{{Name: "meter", Version: "1"}}, nil },
		Metrics: func(w io.Writer) error { _, err := io.WriteString(w, strings.Repeat("x", MaxSnapshot)); return err },
		Publish: func(edge string, data []byte) error {
			mu.Lock()
			defer mu.Unlock()
			if edge != "garage" {
				return errors.New("wrong edge")
			}
			sent = append(sent, data)
			return nil
		},
		Now: time.Now, Log: quiet(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error)
	go func() { done <- p.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	if len(sent) != 0 {
		t.Fatal("sent while unlinked")
	}
	linked = true
	mu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(sent)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 {
		t.Fatalf("sent %d snapshots, want 1", len(sent))
	}
	s, err := decode(sent[0])
	if err != nil {
		t.Fatal(err)
	}
	if !s.Linked || !s.Relinked || s.Events != 3 || s.Metrics != "" || len(s.Modules) != 1 || s.Every != maxEvery {
		t.Fatalf("snapshot = %+v", s)
	}
}

type jobs map[string]schedule.Job

func (j jobs) Get(_ context.Context, id string) (schedule.Job, error) {
	if x, ok := j[id]; ok {
		return x, nil
	}
	return schedule.Job{}, schedule.ErrNotFound
}

// An edge runs a module's cron job and drops every other event.
func TestCronHandler(t *testing.T) {
	var ran []string
	h := CronHandler(jobs{
		"j1": {ID: "j1", Kind: schedule.KindModule, Text: "meter"},
		"j2": {ID: "j2", Kind: schedule.KindReminder, Text: "hi"},
	}, func(_ context.Context, module, id string) error { ran = append(ran, module+"/"+id); return nil }, quiet())
	ev := func(job string) event.Event {
		p, _ := json.Marshal(event.Schedule{Job: job})
		return event.Event{Kind: event.KindSchedule, Source: schedule.Source, Payload: p, IdemKey: "run-" + job}
	}
	for _, e := range []event.Event{ev("j1"), ev("j2"), ev("gone"), {Kind: event.KindMessage}} {
		out, _, err := h(context.Background(), e)
		if err != nil || len(out) != 0 {
			t.Fatalf("event %+v: out %v, err %v", e, out, err)
		}
	}
	if len(ran) != 1 || ran[0] != "meter/run-j1" {
		t.Fatalf("ran = %v", ran)
	}
}
