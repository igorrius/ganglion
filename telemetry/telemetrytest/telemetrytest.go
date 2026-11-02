// Package telemetrytest installs in-memory OpenTelemetry providers for
// tests and reads what the code recorded.
package telemetrytest

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// Recorder holds the in-memory providers.
type Recorder struct {
	reader *sdkmetric.ManualReader
	Spans  *tracetest.SpanRecorder
}

// Install sets global providers backed by memory until the test ends.
// Components must be built after it: they take their instruments from the
// global providers when constructed.
func Install(t testing.TB) *Recorder {
	t.Helper()
	prevM, prevT := otel.GetMeterProvider(), otel.GetTracerProvider()
	r := &Recorder{reader: sdkmetric.NewManualReader(), Spans: tracetest.NewSpanRecorder()}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(r.reader))
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(r.Spans))
	otel.SetMeterProvider(mp)
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetMeterProvider(prevM)
		otel.SetTracerProvider(prevT)
		_ = mp.Shutdown(context.Background())
		_ = tp.Shutdown(context.Background())
	})
	return r
}

func matches(set attribute.Set, want map[string]string) bool {
	for k, v := range want {
		got, ok := set.Value(attribute.Key(k))
		if !ok || got.String() != v {
			return false
		}
	}
	return true
}

// Value sums the data points of the metric named name (OpenTelemetry name,
// e.g. "ganglion.turns") whose attributes include want: counters give
// their sum, histograms their count.
func (r *Recorder) Value(t testing.TB, name string, want map[string]string) float64 {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.reader.Collect(context.Background(), &rm); err != nil {
		t.Fatal(err)
	}
	total := 0.0
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				for _, p := range d.DataPoints {
					if matches(p.Attributes, want) {
						total += float64(p.Value)
					}
				}
			case metricdata.Sum[float64]:
				for _, p := range d.DataPoints {
					if matches(p.Attributes, want) {
						total += p.Value
					}
				}
			case metricdata.Histogram[float64]:
				for _, p := range d.DataPoints {
					if matches(p.Attributes, want) {
						total += float64(p.Count)
					}
				}
			}
		}
	}
	return total
}
