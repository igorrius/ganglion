package telemetry

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/internal/buckets"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func setup(t *testing.T, env map[string]string) *Telemetry {
	t.Helper()
	tel, err := Setup(context.Background(), Config{
		Addr: "127.0.0.1:0", Service: "ganglion-test", Version: "v0",
		Getenv: func(k string) string { return env[k] },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	return tel
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestMetricsEndpoint(t *testing.T) {
	tel := setup(t, nil)
	c, err := otel.Meter("ganglion/test").Int64Counter("ganglion.test.events")
	if err != nil {
		t.Fatal(err)
	}
	c.Add(context.Background(), 3)
	code, body := get(t, "http://"+tel.Addr()+"/metrics")
	if code != 200 || !strings.Contains(body, "ganglion_test_events_total") || !strings.Contains(body, `service_name="ganglion-test"`) {
		t.Fatalf("/metrics %d:\n%s", code, body)
	}
}

// The Go runtime and the process are on /metrics under their standard
// names, as with the default registry (R-OBS-1).
func TestRuntimeAndProcessMetrics(t *testing.T) {
	tel := setup(t, nil)
	_, body := get(t, "http://"+tel.Addr()+"/metrics")
	for _, name := range []string{"go_goroutines", "go_memstats_heap_alloc_bytes", "go_gc_duration_seconds", "go_info",
		"process_resident_memory_bytes", "process_cpu_seconds_total", "process_open_fds", "process_start_time_seconds"} {
		if !strings.Contains(body, "\n"+name) {
			t.Errorf("%s is missing", name)
		}
	}
}

func TestDashboardServedLocally(t *testing.T) {
	tel := setup(t, nil)
	code, body := get(t, "http://"+tel.Addr()+"/webmetrics/")
	if code != 200 || !strings.Contains(strings.ToLower(body), "<html") {
		t.Fatalf("/webmetrics/ %d: %.200s", code, body)
	}
}

func TestNoOTLPNoTracer(t *testing.T) {
	setup(t, nil)
	_, span := otel.Tracer("ganglion/test").Start(context.Background(), "x")
	defer span.End()
	if span.IsRecording() {
		t.Fatal("spans are recorded with no OTLP endpoint")
	}
}

func TestOTLPEndpointTurnsTracingOn(t *testing.T) {
	setup(t, map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://127.0.0.1:1"})
	_, span := otel.Tracer("ganglion/test").Start(context.Background(), "x")
	defer span.End()
	if !span.IsRecording() {
		t.Fatal("spans are not recorded with an OTLP endpoint set")
	}
}

func TestMetricsOff(t *testing.T) {
	tel, err := Setup(context.Background(), Config{Addr: "off", Service: "s", Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tel.Shutdown(context.Background()) }()
	if tel.Addr() != "" {
		t.Fatalf("listening on %q with metrics off", tel.Addr())
	}
}

func TestLogHandlerAddsIDs(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(LogHandler(slog.NewTextHandler(&buf, nil)))
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	ctx, span := tp.Tracer("t").Start(context.Background(), "turn")
	log.InfoContext(ctx, "inside")
	span.End()
	log.Info("outside")
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	sc := span.SpanContext()
	if !strings.Contains(lines[0], "trace_id="+sc.TraceID().String()) || !strings.Contains(lines[0], "span_id="+sc.SpanID().String()) {
		t.Fatalf("inside a span: %s", lines[0])
	}
	if strings.Contains(lines[1], "trace_id") {
		t.Fatalf("outside a span: %s", lines[1])
	}
}

// target_info says which build runs: the version and the commit.
func TestResourceNamesTheCommit(t *testing.T) {
	tel, err := Setup(context.Background(), Config{Addr: "127.0.0.1:0", Service: "ganglion-test", Version: "v0.1.1",
		Commit: "86cda06f1d2e", Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	c, _ := otel.Meter("ganglion/test").Int64Counter("ganglion.test.build")
	c.Add(context.Background(), 1)
	_, body := get(t, "http://"+tel.Addr()+"/metrics")
	if !strings.Contains(body, `vcs_ref_head_revision="86cda06f1d2e"`) || !strings.Contains(body, `service_version="v0.1.1"`) {
		t.Fatalf("target_info lacks the build:\n%s", body)
	}
}

// A histogram in seconds with buckets.Seconds splits sub-second
// times on /metrics: the SDK's default buckets start at 0, 5, 10.
func TestSecondsBucketsOnMetrics(t *testing.T) {
	tel := setup(t, nil)
	h, err := otel.Meter("ganglion/test").Float64Histogram("ganglion.test.wait", metric.WithUnit("s"), buckets.Seconds)
	if err != nil {
		t.Fatal(err)
	}
	h.Record(context.Background(), 0.03)
	_, body := get(t, "http://"+tel.Addr()+"/metrics")
	for _, le := range []string{`le="0.05"`, `le="0.5"`, `le="60"`} {
		if !strings.Contains(body, "ganglion_test_wait_seconds_bucket{") || !strings.Contains(body, le) {
			t.Fatalf("/metrics lacks the %s bucket of ganglion_test_wait_seconds:\n%s", le, body)
		}
	}
}

// The picture is drawn with /metrics off: the dashboard reads the
// in-process handler and nothing listens (ADR-0045).
func TestRendererWithMetricsAddrOff(t *testing.T) {
	tel, err := Setup(context.Background(), Config{Addr: "off", Service: "ganglion", Getenv: func(string) string { return "" }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	var png, sum bytes.Buffer
	if err := tel.Renderer().Picture(context.Background(), &png, Selection{Sections: []string{"go"}}); err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(png.Bytes(), []byte("\x89PNG")) {
		t.Fatal("not a PNG")
	}
	if err := tel.Renderer().Summary(context.Background(), &sum, Selection{Globs: []string{"go_*"}, All: true}, true); err != nil || !bytes.Contains(sum.Bytes(), []byte("promtop.summary/v1")) {
		t.Fatalf("summary %s, %v", sum.String(), err)
	}
	if secs, err := tel.Renderer().Sections(context.Background()); err != nil || len(secs) == 0 {
		t.Fatalf("sections %v, %v", secs, err)
	}
}
