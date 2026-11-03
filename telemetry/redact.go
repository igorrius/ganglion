package telemetry

import (
	"context"
	"fmt"
	"log/slog"
)

// RedactHandler wraps h so that the message and every string attribute,
// in groups too, pass through hide before h sees them (R-OBS-6). Errors
// and fmt.Stringer values are turned into their text first, since that
// is what a handler would print.
func RedactHandler(h slog.Handler, hide func(string) string) slog.Handler {
	return redactHandler{h: h, hide: hide}
}

type redactHandler struct {
	h    slog.Handler
	hide func(string) string
}

func (r redactHandler) Enabled(ctx context.Context, l slog.Level) bool { return r.h.Enabled(ctx, l) }

func (r redactHandler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, r.hide(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(r.attr(a))
		return true
	})
	return r.h.Handle(ctx, out)
}

func (r redactHandler) WithAttrs(as []slog.Attr) slog.Handler {
	clean := make([]slog.Attr, len(as))
	for i, a := range as {
		clean[i] = r.attr(a)
	}
	return redactHandler{h: r.h.WithAttrs(clean), hide: r.hide}
}

func (r redactHandler) WithGroup(name string) slog.Handler {
	return redactHandler{h: r.h.WithGroup(name), hide: r.hide}
}

// attr redacts one attribute, its group members included.
func (r redactHandler) attr(a slog.Attr) slog.Attr {
	v := a.Value.Resolve()
	switch v.Kind() {
	case slog.KindString:
		return slog.String(a.Key, r.hide(v.String()))
	case slog.KindGroup:
		members := v.Group()
		clean := make([]any, len(members))
		for i, m := range members {
			clean[i] = r.attr(m)
		}
		return slog.Group(a.Key, clean...)
	case slog.KindAny:
		switch x := v.Any().(type) {
		case error:
			return slog.String(a.Key, r.hide(x.Error()))
		case fmt.Stringer:
			return slog.String(a.Key, r.hide(x.String()))
		}
	case slog.KindBool, slog.KindDuration, slog.KindFloat64, slog.KindInt64, slog.KindTime, slog.KindUint64,
		slog.KindLogValuer: // numbers, times and flags hold no secret; Resolve leaves no valuer
	}
	return slog.Attr{Key: a.Key, Value: v}
}
