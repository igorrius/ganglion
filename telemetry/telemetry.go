// Package telemetry sets up OpenTelemetry for a node (docs/spec.md, 7.9;
// ADR-0026): metrics, the Go runtime and process ones among them, through
// the Prometheus exporter on /metrics with the promtop dashboard on
// /webmetrics/, and traces over OTLP/HTTP when the standard OTLP
// environment variables name a collector. Packages instrument themselves
// through the global otel providers.
package telemetry

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/igorrius/promtop/dash"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// DefaultAddr is where /metrics and /webmetrics/ are served by default:
// loopback only.
const DefaultAddr = "127.0.0.1:9464"

// Config sets telemetry up.
type Config struct {
	Addr    string // host:port for /metrics and /webmetrics/; "off" serves nothing
	Service string // service.name
	Version string // service.version
	Commit  string // vcs.ref.head.revision; empty for a build without VCS stamps
	Getenv  func(string) string
}

// Telemetry owns the providers and the HTTP server.
type Telemetry struct {
	mp   *sdkmetric.MeterProvider
	tp   *sdktrace.TracerProvider // nil when no OTLP endpoint is set
	srv  *http.Server
	ln   net.Listener
	dash *dash.Dashboard
}

// Setup installs the global meter provider (and tracer provider, when
// OTLP is configured) and starts serving metrics.
func Setup(ctx context.Context, cfg Config) (*Telemetry, error) {
	getenv := cfg.Getenv
	if getenv == nil {
		getenv = os.Getenv
	}
	attrs := []attribute.KeyValue{attribute.String("service.name", cfg.Service), attribute.String("service.version", cfg.Version)}
	if cfg.Commit != "" {
		attrs = append(attrs, attribute.String("vcs.ref.head.revision", cfg.Commit))
	}
	res := resource.NewSchemaless(attrs...)
	reg := prometheus.NewRegistry()
	// A fresh registry has none of the default registry's collectors; the
	// runtime and process metrics are registered here by hand (R-OBS-1).
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	exp, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		return nil, err
	}
	t := &Telemetry{mp: sdkmetric.NewMeterProvider(sdkmetric.WithReader(exp), sdkmetric.WithResource(res))}
	otel.SetMeterProvider(t.mp)

	if getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "" || getenv("OTEL_EXPORTER_OTLP_TRACES_ENDPOINT") != "" {
		te, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, errors.Join(err, t.mp.Shutdown(ctx))
		}
		t.tp = sdktrace.NewTracerProvider(sdktrace.WithBatcher(te), sdktrace.WithResource(res))
		otel.SetTracerProvider(t.tp)
	}

	// HandlerFor without a Registry in the options does not count its own
	// calls, so the dashboard's polling stays out of what scrapers see.
	metrics := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	// The dashboard runs even with nothing listening: it reads the
	// handler in process, and the picture in the chat is drawn from it
	// (ADR-0045).
	t.dash = dash.New(metrics, dash.Title(cfg.Service))
	if cfg.Addr == "off" {
		return t, nil
	}
	addr := cfg.Addr
	if addr == "" {
		addr = DefaultAddr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, errors.Join(err, t.Shutdown(ctx))
	}
	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics)
	mux.Handle("/webmetrics/", t.dash)
	t.ln, t.srv = ln, &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = t.srv.Serve(ln) }()
	return t, nil
}

// Renderer draws the node's own metrics for the chat.
func (t *Telemetry) Renderer() *Renderer { return &Renderer{d: t.dash} }

// Renderer draws the dashboard's history: a PNG for the owner, the
// promtop summary for the model (ADR-0045). It reads the registry in
// process, never over the network.
type Renderer struct{ d *dash.Dashboard }

// Selection is what to draw: none of it set is the default dashboard.
type Selection struct {
	Globs    []string // metric name globs
	Sections []string // section keys, e.g. "go", "process", "ganglion"
	All      bool     // what the dashboard hides by default too
}

// Section is one of the dashboard's sections.
type Section struct{ Key, Title string }

func (s Selection) options(f dash.Format) dash.RenderOptions {
	return dash.RenderOptions{Format: f, Metrics: s.Globs, Sections: s.Sections, ShowAll: s.All}
}

// Picture writes the selection as a PNG.
func (r *Renderer) Picture(ctx context.Context, w io.Writer, sel Selection) error {
	return r.d.Render(ctx, w, sel.options(dash.FormatPNG))
}

// Summary writes the promtop.summary/v1 document of the selection;
// notable keeps only what stands out.
func (r *Renderer) Summary(ctx context.Context, w io.Writer, sel Selection, notable bool) error {
	o := sel.options(dash.FormatSummary)
	o.Notable = notable
	return r.d.Render(ctx, w, o)
}

// Sections lists the dashboard's sections.
func (r *Renderer) Sections(ctx context.Context) ([]Section, error) {
	secs, err := r.d.Sections(ctx)
	out := make([]Section, len(secs))
	for i, s := range secs {
		out[i] = Section{Key: s.Key, Title: s.Title}
	}
	return out, err
}

// Addr is the address metrics are served on, or "" when they are off.
func (t *Telemetry) Addr() string {
	if t.ln == nil {
		return ""
	}
	return t.ln.Addr().String()
}

// Shutdown stops the server and flushes the providers.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	if t.srv != nil {
		errs = append(errs, t.srv.Shutdown(ctx))
	}
	if t.tp != nil {
		errs = append(errs, t.tp.Shutdown(ctx))
	}
	if t.dash != nil {
		errs = append(errs, t.dash.Close())
	}
	errs = append(errs, t.mp.Shutdown(ctx))
	return errors.Join(errs...)
}
