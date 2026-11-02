package telemetry

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
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
