package telemetry

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// LogHandler wraps h so that a record logged with a context that holds a
// valid span carries trace_id and span_id (R-OBS-4).
func LogHandler(h slog.Handler) slog.Handler { return traceHandler{h} }

type traceHandler struct{ slog.Handler }

func (t traceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(slog.String("trace_id", sc.TraceID().String()), slog.String("span_id", sc.SpanID().String()))
	}
	return t.Handler.Handle(ctx, r)
}

func (t traceHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return traceHandler{t.Handler.WithAttrs(as)}
}

func (t traceHandler) WithGroup(name string) slog.Handler {
	return traceHandler{t.Handler.WithGroup(name)}
}
