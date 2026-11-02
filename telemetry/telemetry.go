// Package telemetry sets up OpenTelemetry for a node (docs/spec.md, 7.9;
// ADR-0026): metrics through the Prometheus exporter on /metrics with the
// promtop dashboard on /webmetrics/, and traces over OTLP/HTTP when the
// standard OTLP environment variables name a collector. Packages
// instrument themselves through the global otel providers.
package telemetry

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/igorrius/promtop/dash"
	"github.com/prometheus/client_golang/prometheus"
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
	mp  *sdkmetric.MeterProvider
	tp  *sdktrace.TracerProvider // nil when no OTLP endpoint is set
	srv *http.Server
	ln  net.Listener
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
	// HandlerFor without a Registry in the options does not count its own
	// calls, so the dashboard's polling stays out of what scrapers see.
	metrics := promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics)
	mux.Handle("/webmetrics/", dash.New(metrics, dash.Title(cfg.Service)))
	t.ln, t.srv = ln, &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { _ = t.srv.Serve(ln) }()
	return t, nil
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
	errs = append(errs, t.mp.Shutdown(ctx))
	return errors.Join(errs...)
}
