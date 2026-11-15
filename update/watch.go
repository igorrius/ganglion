package update

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
)

// ConfirmAfter is how long a new version's components run before the
// node calls it good (R-UPD-4).
const ConfirmAfter = 2 * time.Minute

// AfterFunc waits for d: its channel fires after d (time.After).
type AfterFunc func(d time.Duration) <-chan time.Time

// EnqueueFunc stores messages in the outbox (queue.Enqueue).
type EnqueueFunc func(ctx context.Context, msgs ...event.Outgoing) error

// LangFunc is a person's language (language.Store.Resolve).
type LangFunc func(ctx context.Context, principal string) (texts.Lang, error)

// Watch is the node component that tells the admins what the launcher did
// and confirms a new version once it has run for ConfirmAfter
// (R-UPD-4, R-UPD-5, ADR-0081).
type Watch struct {
	store   *Store
	running string
	admins  alert.Admins
	enqueue EnqueueFunc
	lang    LangFunc
	texts   *texts.Catalog
	after   AfterFunc
	log     *slog.Logger
	tel     telemetry
}

// WatchOption configures a Watch.
type WatchOption func(*Watch)

// WithAfter sets how the confirmation time is waited for (tests).
func WithAfter(f AfterFunc) WatchOption { return func(w *Watch) { w.after = f } }

// NewWatch makes the component for the node that runs the core release
// running ("" or a version the state does not name watches nothing but a
// fall back).
func NewWatch(s *Store, running string, admins alert.Admins, enqueue EnqueueFunc, lang LangFunc, cat *texts.Catalog,
	log *slog.Logger, opts ...WatchOption) *Watch {
	w := &Watch{store: s, running: running, admins: admins, enqueue: enqueue, lang: lang, texts: cat,
		after: time.After, log: log, tel: newTelemetry()}
	for _, o := range opts {
		o(w)
	}
	return w
}

// Name implements node.Component.
func (w *Watch) Name() string { return "update" }

// Run tells about a fall back, then waits to confirm a pending version
// that is this one. A state it cannot read or write is logged: watching
// never stops the node.
func (w *Watch) Run(ctx context.Context) error {
	st, err := w.store.State()
	if err != nil {
		w.log.Warn("update: the state is not readable; nothing to watch", "err", err)
		return nil
	}
	if st.Fallback != nil {
		w.tellFallback(ctx, *st.Fallback)
	}
	if st.Pending != nil && st.Pending.Version == w.running {
		w.confirm(ctx, *st.Pending)
	}
	return nil
}

// tellFallback tells every admin once and clears the record. A record that
// could not be sent stays for the next start.
func (w *Watch) tellFallback(ctx context.Context, fb Fallback) {
	key := "update:fallback:" + fb.From + ":" + strconv.FormatInt(fb.At.Unix(), 10)
	if !w.tell(ctx, key, func(ctx context.Context, p string) string { return w.fallbackText(ctx, p, fb) }) {
		return
	}
	w.tel.fallback(ctx, fb.Reason)
	w.log.Warn("update: fell back", "from", fb.From, "to", fb.To, "reason", string(fb.Reason), "restored_db", fb.RestoredDB)
	_, err := w.store.mutate(func(st *State) bool {
		if st.Fallback == nil || !sameFallback(*st.Fallback, fb) {
			return false // a newer one, or already cleared: not ours to clear
		}
		st.Fallback = nil
		return true
	})
	if err != nil {
		w.log.Warn("update: the fall back record was not cleared", "err", err)
	}
}

// confirm waits for the confirmation time, then clears pending, records
// the version as confirmed and tells the admins once. The context ending
// first confirms nothing: the version has not run long enough.
func (w *Watch) confirm(ctx context.Context, p Pending) {
	select {
	case <-ctx.Done():
		return
	case <-w.after(ConfirmAfter):
	}
	cleared := false
	_, err := w.store.mutate(func(st *State) bool {
		if st.Pending == nil || st.Pending.Version != p.Version || !st.Pending.Since.Equal(p.Since) {
			return false
		}
		st.Pending, st.Confirmed, cleared = nil, p.Version, true
		return true
	})
	if err != nil {
		w.log.Warn("update: the version was not confirmed", "release", p.Version, "err", err)
		return
	}
	if !cleared {
		return
	}
	w.tel.confirmed(ctx)
	w.log.Info("update: confirmed", "release", p.Version)
	key := "update:confirmed:" + p.Version + ":" + strconv.FormatInt(p.Since.Unix(), 10)
	w.tell(ctx, key, func(ctx context.Context, principal string) string {
		return w.texts.Text(w.langOf(ctx, principal), texts.UpdateConfirmed, texts.Vars{"version": texts.Escape(p.Version)})
	})
}

// tell stores a text for every admin, each in their language. It reports
// whether it was stored; a failure is logged.
func (w *Watch) tell(ctx context.Context, key string, text alert.TextFunc) bool {
	msgs, err := w.admins.Each(ctx, key, text)
	if err == nil && len(msgs) > 0 {
		err = w.enqueue(ctx, msgs...)
	}
	if err != nil {
		if ctx.Err() == nil {
			w.log.Error("update: the admins were not told", "key", key, "err", err)
		}
		return false
	}
	return true
}

// langOf is the admin's language, English when it cannot be told.
func (w *Watch) langOf(ctx context.Context, principal string) texts.Lang {
	l, err := w.lang(ctx, principal)
	if err != nil {
		return texts.English
	}
	return l
}

// fallbackKeys is the alert for each reason; a closed set.
var fallbackKeys = map[Reason]texts.Key{
	ReasonFailed:     texts.UpdateAlertFailed,
	ReasonUnverified: texts.UpdateAlertUnverified,
	ReasonNoExec:     texts.UpdateAlertNoExec,
	ReasonExec:       texts.UpdateAlertExec,
}

func (w *Watch) fallbackText(ctx context.Context, principal string, fb Fallback) string {
	l := w.langOf(ctx, principal)
	key, ok := fallbackKeys[fb.Reason]
	if !ok {
		key = texts.UpdateAlertFailed // a record from before the reason was kept
	}
	to := texts.Escape(fb.To)
	if fb.To == "" {
		to = w.texts.Text(l, texts.UpdateImageVersion, nil)
	}
	db := texts.UpdateDBKept
	if fb.RestoredDB {
		db = texts.UpdateDBRestored
	}
	return w.texts.Text(l, key, texts.Vars{"from": texts.Escape(fb.From), "to": to, "db": w.texts.Text(l, db, nil)})
}

func sameFallback(a, b Fallback) bool { return a.From == b.From && a.To == b.To && a.At.Equal(b.At) }
