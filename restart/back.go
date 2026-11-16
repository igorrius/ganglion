package restart

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
)

// EnqueueFunc stores messages in the outbox (queue.Enqueue).
type EnqueueFunc func(ctx context.Context, msgs ...event.Outgoing) error

// LangFunc is a person's language (language.Store.Resolve).
type LangFunc func(ctx context.Context, principal string) (texts.Lang, error)

// AppliedFunc are the reasons of the changes from the chat recorded
// between since and until (confighist: config.propose and config.rollback).
type AppliedFunc func(ctx context.Context, since, until time.Time) ([]string, error)

// maxApplied bounds the changes named in the message.
const maxApplied = 5

// Back is the node component that says the node is back after a restart
// asked from the chat.
type Back struct {
	note    *Note
	enqueue EnqueueFunc
	lang    LangFunc
	applied AppliedFunc
	texts   *texts.Catalog
	now     func() time.Time
	log     *slog.Logger
}

// NewBack makes the component.
func NewBack(n *Note, enqueue EnqueueFunc, lang LangFunc, applied AppliedFunc, cat *texts.Catalog,
	now func() time.Time, log *slog.Logger) *Back {
	return &Back{note: n, enqueue: enqueue, lang: lang, applied: applied, texts: cat, now: now, log: log}
}

// Name implements node.Component.
func (b *Back) Name() string { return "restart" }

// Run takes the record and tells; it never stops the node: what goes
// wrong is logged.
func (b *Back) Run(ctx context.Context) error {
	r, ok, err := b.note.Take()
	if err != nil {
		b.log.Warn("restart: the record was dropped", "err", err)
		return nil
	}
	if !ok {
		return nil
	}
	msg := event.Outgoing{IdemKey: "restart:back:" + strconv.FormatInt(r.At.UnixNano(), 10),
		Transport: r.Transport, Conv: r.Conv, Text: b.text(ctx, r)}
	if err := b.enqueue(ctx, msg); err != nil {
		b.log.Warn("restart: the message that the node is back was not stored", "err", err)
		return nil
	}
	b.log.Info("restart: back", "took", b.now().Sub(r.At).Round(time.Second).String())
	return nil
}

// text is the message in the language of the person who asked.
func (b *Back) text(ctx context.Context, r Request) string {
	lang, err := b.lang(ctx, r.Principal)
	if err != nil {
		lang = texts.English
	}
	secs := max(0, int(b.now().Sub(r.At).Round(time.Second)/time.Second))
	text := b.texts.Text(lang, texts.ReplyBack, texts.Vars{"seconds": strconv.Itoa(secs)})
	reasons, err := b.applied(ctx, r.Since, r.At)
	if err != nil {
		b.log.Debug("restart: no changes to name", "err", err)
		return text
	}
	if len(reasons) == 0 {
		return text
	}
	if len(reasons) > maxApplied {
		reasons = append(reasons[:maxApplied], "…")
	}
	for i, s := range reasons {
		reasons[i] = "• " + texts.Escape(s)
	}
	return text + "\n" + b.texts.Text(lang, texts.ReplyBackApplied, texts.Vars{"changes": strings.Join(reasons, "\n")})
}
