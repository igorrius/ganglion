package agent

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/memory"
	"github.com/igorrius/ganglion/texts"
)

// varTitle is the texts' variable that names a title.
const varTitle = "title"

// MemoryView is what /memory reads (memory.Memory).
type MemoryView interface {
	Topics() ([]memory.Topic, error)
	Topic(slug string) (memory.Topic, error)
}

// NoMemory is a MemoryView with nothing in it: a node without memory.
type NoMemory struct{}

// Topics implements MemoryView.
func (NoMemory) Topics() ([]memory.Topic, error) { return nil, nil }

// Topic implements MemoryView.
func (NoMemory) Topic(string) (memory.Topic, error) {
	return memory.Topic{}, memory.ErrNotFound
}

// showMemory shows an admin what memory holds, by topic, or one topic with
// each fact's id and source (R-MEM-9), with no model and no note in the
// history. It answers only in the admin's private chat: a group would see
// everything remembered.
func (a *Agent) showMemory(ctx context.Context, t *turn, arg string) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		t.reply(a.say(ctx, t, texts.ReplyNotAvailable, nil))
		return nil
	}
	if _, conv, ok := a.d.Private(t.msg.From); !ok || conv != t.event.Conv {
		t.reply(a.say(ctx, t, texts.MemoryPrivateOnly, nil))
		return nil
	}
	sp := a.speaker(ctx, t)
	if slug := strings.TrimSpace(arg); slug != "" {
		return a.showTopic(t, sp, slug)
	}
	ts, err := a.d.Memory.Topics()
	if err != nil {
		return err
	}
	if len(ts) == 0 {
		t.reply(sp.Text(texts.MemoryEmpty, nil))
		return nil
	}
	var b strings.Builder
	b.WriteString(sp.Text(texts.MemoryTopicsHead, nil))
	for _, tp := range ts {
		b.WriteString("\n- " + sp.Text(texts.MemoryTopicRow, texts.Vars{varTitle: texts.Escape(tp.Title), "slug": texts.Escape(tp.Slug),
			"count": strconv.Itoa(len(tp.Facts))}))
		if tp.Summary != "" {
			b.WriteString(" — " + texts.Escape(tp.Summary))
		}
	}
	t.reply(b.String())
	return nil
}

// showTopic replies with one topic's facts.
func (a *Agent) showTopic(t *turn, sp texts.Speaker, slug string) error {
	tp, err := a.d.Memory.Topic(slug)
	if errors.Is(err, memory.ErrNotFound) || errors.Is(err, memory.ErrInvalid) {
		t.reply(sp.Text(texts.MemoryNoTopic, texts.Vars{"topic": texts.Escape(slug)}))
		return nil
	}
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString(texts.Escape(tp.Title) + " (" + texts.Escape(tp.Slug) + ")")
	if tp.Summary != "" {
		b.WriteString(" — " + texts.Escape(tp.Summary))
	}
	for _, f := range tp.Facts {
		b.WriteString("\n- ")
		if f.ID != "" {
			b.WriteString("[" + f.ID + "] ")
		}
		b.WriteString(texts.Escape(f.Text) + " (" + factSource(sp, f) + ")")
	}
	t.reply(b.String())
	return nil
}

// factSource is when a fact was stored, and whether it came from a
// conversation with outside content, in the reader's language.
func factSource(sp texts.Speaker, f memory.Fact) string {
	s := sp.Text(texts.MemoryByHand, nil)
	if !f.Date.IsZero() {
		s = f.Date.Format(time.DateOnly)
	}
	if f.Tainted {
		s += ", " + sp.Text(texts.MemoryFromOutside, nil)
	}
	return s
}
