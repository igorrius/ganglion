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

// collect returns the metrics recorded so far. A gauge callback that
// fails is logged, not fatal: the first provider a test binary installs
// also gets the instruments that earlier tests made through the global
// delegate, and their callbacks read databases those tests closed. The
// SDK still returns every other metric, and a wrong value fails the
// test's own check.
func (r *Recorder) collect(t testing.TB) []metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := r.reader.Collect(context.Background(), &rm); err != nil {
		t.Logf("telemetrytest: collect: %v", err)
	}
	var out []metricdata.Metrics
	for _, sm := range rm.ScopeMetrics {
		out = append(out, sm.Metrics...)
	}
	return out
}

// Value sums the data points of the metric named name (OpenTelemetry name,
// e.g. "ganglion.turns") whose attributes include want: counters give
// their sum, histograms their count, gauges their value.
func (r *Recorder) Value(t testing.TB, name string, want map[string]string) float64 {
	t.Helper()
	total := 0.0
	for _, m := range r.collect(t) {
		if m.Name != name {
			continue
		}
		switch d := m.Data.(type) {
		case metricdata.Sum[int64]:
			total += sum(d.DataPoints, want, func(p metricdata.DataPoint[int64]) float64 { return float64(p.Value) })
		case metricdata.Sum[float64]:
			total += sum(d.DataPoints, want, func(p metricdata.DataPoint[float64]) float64 { return p.Value })
		case metricdata.Gauge[int64]:
			total += sum(d.DataPoints, want, func(p metricdata.DataPoint[int64]) float64 { return float64(p.Value) })
		case metricdata.Gauge[float64]:
			total += sum(d.DataPoints, want, func(p metricdata.DataPoint[float64]) float64 { return p.Value })
		case metricdata.Histogram[int64]:
			total += sum(d.DataPoints, want, func(p metricdata.HistogramDataPoint[int64]) float64 { return float64(p.Count) })
		case metricdata.Histogram[float64]:
			total += sum(d.DataPoints, want, func(p metricdata.HistogramDataPoint[float64]) float64 { return float64(p.Count) })
		}
	}
	return total
}

// Sum is the sum of the values a histogram recorded, over the data points
// whose attributes include want.
func (r *Recorder) Sum(t testing.TB, name string, want map[string]string) float64 {
	t.Helper()
	total := 0.0
	for _, m := range r.collect(t) {
		if m.Name != name {
			continue
		}
		switch d := m.Data.(type) {
		case metricdata.Histogram[int64]:
			total += sum(d.DataPoints, want, func(p metricdata.HistogramDataPoint[int64]) float64 { return float64(p.Sum) })
		case metricdata.Histogram[float64]:
			total += sum(d.DataPoints, want, func(p metricdata.HistogramDataPoint[float64]) float64 { return p.Sum })
		}
	}
	return total
}

// Bounds are the bucket boundaries of the histogram named name, from its
// first data point; nil when it has none.
func (r *Recorder) Bounds(t testing.TB, name string) []float64 {
	t.Helper()
	for _, m := range r.collect(t) {
		if m.Name != name {
			continue
		}
		switch d := m.Data.(type) {
		case metricdata.Histogram[int64]:
			if len(d.DataPoints) > 0 {
				return d.DataPoints[0].Bounds
			}
		case metricdata.Histogram[float64]:
			if len(d.DataPoints) > 0 {
				return d.DataPoints[0].Bounds
			}
		}
	}
	return nil
}

// attributed is a data point of any kind.
type attributed interface {
	metricdata.DataPoint[int64] | metricdata.DataPoint[float64] |
		metricdata.HistogramDataPoint[int64] | metricdata.HistogramDataPoint[float64]
}

// sum adds value(p) over the points whose attributes include want.
func sum[P attributed](points []P, want map[string]string, value func(P) float64) float64 {
	total := 0.0
	for _, p := range points {
		if matches(attributesOf(p), want) {
			total += value(p)
		}
	}
	return total
}

func attributesOf[P attributed](p P) attribute.Set {
	switch p := any(p).(type) {
	case metricdata.DataPoint[int64]:
		return p.Attributes
	case metricdata.DataPoint[float64]:
		return p.Attributes
	case metricdata.HistogramDataPoint[int64]:
		return p.Attributes
	case metricdata.HistogramDataPoint[float64]:
		return p.Attributes
	}
	return attribute.Set{}
}
