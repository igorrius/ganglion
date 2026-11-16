package agent

import (
	"cmp"
	"context"
	"regexp"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/tool"
)

// What a reply brings into the history (R-DLV-13, R-DLV-14, design section 3
// of p1-reply-context, ADR-0055).
const (
	replyExcerptRunes = 150  // a message the history already holds: a pointer to it
	replyTextRunes    = 2000 // any other: its text
	sourceReplied     = "replied message"
	// notOfThisNode follows the time of a message the channel says the bot
	// wrote and this node's outbox does not hold (R-DLV-15).
	notOfThisNode = ", which this node has no record of: another node of this bot may have sent it, or it is over 90 days old"
	// moduleWrote follows the time of a module's message (R-DLV-15).
	moduleWrote   = "; a module writes its messages itself, with no model, and its text is not shown here: read the module's source to see what it writes"
	unknownTime   = "an unknown time"
	unknownSource = "an unknown source"
	thisSession   = "this session"
	withFile      = "a message with a file"
)

// replyKind is how a replied message reached the history; the metric's label.
type replyKind string

// Reply kinds.
const (
	replyCurrent replyKind = "current" // the bot's, in this session
	replyEarlier replyKind = "earlier" // the bot's, elsewhere, written clean
	replyTainted replyKind = "tainted" // the bot's, written tainted or of no known origin
	replyModule  replyKind = "module"  // a module's, named without its text
	replyOwn     replyKind = "own"     // the sender's own words
	replyOutside replyKind = "outside" // anything else, or not found
	replyFailed  replyKind = "failed"  // the lookup failed: as outside
)

// replyNote is what the history says about the message a reply answers.
type replyNote struct {
	kind    replyKind // "" when the message answers none
	line    string    // after the sender's words; "" for none
	outside bool      // it taints the session
}

// sources name where a message the node sent came from, for the note.
var sources = map[event.OriginKind]string{
	event.OriginTurn:     "an earlier session",
	event.OriginTask:     "a scheduled task's report",
	event.OriginReminder: "a reminder",
	event.OriginSystem:   "a system job's report",
}

// repliedRow is what the node's outbox holds for the bot's message a reply
// answers: the row, whether there is one, and whether reading it failed.
type repliedRow struct {
	row    event.Outgoing
	found  bool
	failed bool
	// run is the journal's row of the run that sent a module's message, when
	// the message names one the journal still has (R-RUN-20).
	run *modstore.Run
}

// lookupReplied reads from the outbox the message m answers when the channel
// says the bot wrote it and it is not a forward; for any other reply it reads
// nothing. The row is found by the ref in this transport and conversation, so a
// copy that matches no row is outside content (R-DLV-14). A failed read is
// logged without the text, its error naming the ref (a message id, not
// sensitive), and leaves failed set: it costs only the taint.
func (a *Agent) lookupReplied(ctx context.Context, transport, conv string, m event.Message) repliedRow {
	r := m.Reply
	if r == nil || !r.ByBot || r.Forwarded || r.Ref == "" {
		return repliedRow{}
	}
	row, found, err := a.d.Sent.SentByRef(ctx, transport, conv, r.Ref)
	if err != nil {
		if ctx.Err() == nil {
			a.log.Warn("agent: replied message not read", "conv", conv, "err", err)
		}
		return repliedRow{failed: true}
	}
	return repliedRow{row: row, found: found, run: a.runOf(ctx, row, found)}
}

// runOf is the journal's row of the run that sent row, a module's message;
// nil for any other message, one that names no run, one the journal no
// longer has, and when reading it fails: the note is then what it was
// before the journal (R-RUN-20).
func (a *Agent) runOf(ctx context.Context, row event.Outgoing, found bool) *modstore.Run {
	if !found || row.Origin == nil || row.Origin.Run == "" {
		return nil
	}
	if _, ok := moduleOf(row.Origin); !ok {
		return nil
	}
	run, ok, err := a.d.Local.Run(ctx, row.Origin.Module, row.Origin.Run)
	if err != nil {
		if ctx.Err() == nil {
			a.log.Warn("agent: the run of a replied module message not read", "err", err)
		}
		return nil
	}
	if !ok {
		return nil
	}
	return &run
}

// replyNoteFor sorts m's reply into a note, given what the outbox holds for it
// (got), the id of the session the message joins and the node's location. The
// bot's message is judged by its row: Telegram's copy decides nothing about it.
// A selected quote takes the place of the replied text, and taints as the
// message it was taken from (design section 3).
func replyNoteFor(m event.Message, got repliedRow, session string, loc *time.Location) replyNote {
	r := m.Reply
	switch {
	case r == nil && m.Quote == "":
		return replyNote{}
	case r == nil:
		return outsideNote(replyOutside, unknownTime, m.Quote, false)
	case r.Forwarded:
		return outsideNote(replyOutside, when(r.At, loc), cmp.Or(m.Quote, r.Text), r.HasFile)
	case r.ByBot:
		return botNote(m, got, session, when(r.At, loc), loc)
	case r.BySender:
		return ownNote(when(r.At, loc), cmp.Or(m.Quote, r.Text), r.HasFile)
	}
	return outsideNote(replyOutside, when(r.At, loc), cmp.Or(m.Quote, r.Text), r.HasFile)
}

// botNote is the note of a reply to the bot's message sent at, by its row.
// A module's message brings the module's name and nothing of its text, the
// channel's copy or a quote, so it does not taint (R-DLV-15, ADR-0073); when
// the journal has its run, the note also tells the chain of runs that led to
// it, in the node's words (R-RUN-20).
// The taint is checked before the session, so text written tainted is wrapped
// even in its own session: an address in it never becomes known. A quote is not
// checked against the row: a forged one gains nothing, since the sender's own
// words are plain anyway.
func botNote(m event.Message, got repliedRow, session, at string, loc *time.Location) replyNote {
	r := m.Reply
	switch {
	case got.failed:
		return outsideNote(replyFailed, at, cmp.Or(m.Quote, r.Text), r.HasFile)
	case !got.found:
		return outsideNote(replyOutside, at+notOfThisNode, cmp.Or(m.Quote, r.Text), r.HasFile)
	}
	o, body := got.row.Origin, cmp.Or(m.Quote, got.row.Text)
	if name, ok := moduleOf(o); ok {
		line := "[in reply to the message the module " + name + " sent at " + at
		if got.run != nil {
			line += "; how it came about: " + chainWords(*got.run, loc)
		}
		return replyNote{kind: replyModule, line: line + moduleWrote + "]"}
	}
	src, known := unknownSource, false
	if o != nil {
		src, known = sources[o.Kind]
	}
	head := "[in reply to your message of " + at
	current := known && o.Session != "" && o.Session == session
	switch {
	case !known || o.Tainted:
		return taintedNote(head, cmp.Or(src, unknownSource), body, current)
	case current:
		if job := jobOf(o); job != "" {
			head += ", " + src + job
		}
		return replyNote{kind: replyCurrent, line: head + quoted(body, replyExcerptRunes, r.HasFile) + "]"}
	}
	src += jobOf(o)
	if body == "" {
		return replyNote{kind: replyEarlier, line: head + ", from " + src + filed(r.HasFile) + "]"}
	}
	return replyNote{kind: replyEarlier, line: head + ", from " + src + ":]\n" + text.Truncate(body, replyTextRunes)}
}

// jobPattern is what a job id may look like in a note: the node's own ids
// (schedule.create's lower-case key and number, a system job's name).
var jobPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9:._-]{0,127}$`)

// jobOf names the job of a reminder's message for the note, ", job <id>", so
// the model can stop it without listing the jobs (ADR-0091); "" for any other
// message, or an id the node could not have written.
func jobOf(o *event.Origin) string {
	if o == nil || o.Kind != event.OriginReminder || !jobPattern.MatchString(o.Job) {
		return ""
	}
	return ", job " + o.Job
}

// moduleOf is the short name of the module that wrote a message of origin
// o, false when a module did not or the record names none. The name comes
// from the node's own record, so the note carries it as the node's words; one
// that could not be a module's (modstore.CheckShortName) is not trusted, and
// the message is then of an unknown source.
func moduleOf(o *event.Origin) (string, bool) {
	if o == nil || o.Kind != event.OriginModule {
		return "", false
	}
	name := moduleName(o.Module)
	if modstore.CheckShortName(name) != "" {
		return "", false
	}
	return name, true
}

// taintedNote is the note of the bot's message from src written tainted or of
// no known origin: its text wrapped as outside content that taints. One of the
// current session, whose history holds it already, brings only an excerpt.
func taintedNote(head, src, body string, current bool) replyNote {
	n := replyTextRunes
	if current {
		src, n = thisSession, replyExcerptRunes
	}
	return replyNote{kind: replyTainted, outside: true,
		line: head + ", from " + src + "; its text is outside content]" + wrapped(body, n)}
}

// ownNote is the note of a reply to the sender's own message, from the
// channel's copy: their words, not outside content.
func ownNote(at, body string, hasFile bool) replyNote {
	return replyNote{kind: replyOwn, line: "[in reply to their own message of " + at + quoted(body, replyTextRunes, hasFile) + "]"}
}

// outsideNote is the note of a message someone else wrote, or one not known
// to be the bot's: its text wrapped as outside content, tainting the session.
func outsideNote(kind replyKind, at, body string, hasFile bool) replyNote {
	line := "[in reply to a message of " + at
	if body == "" {
		line += filed(hasFile) + "]"
	} else {
		line += "]" + wrapped(body, replyTextRunes)
	}
	return replyNote{kind: kind, outside: true, line: line}
}

// quoted is `: "<body cut at n runes>"`, or the file mark when body is empty.
func quoted(body string, n int, hasFile bool) string {
	if body == "" {
		return filed(hasFile)
	}
	return `: "` + text.Truncate(body, n) + `"`
}

// filed is ": a message with a file" when a file came with the message
// (R-DLV-10), "" otherwise.
func filed(hasFile bool) string {
	if hasFile {
		return ": " + withFile
	}
	return ""
}

// wrapped is body cut at n runes on its own line, wrapped as outside content;
// "" for none.
func wrapped(body string, n int) string {
	if body == "" {
		return ""
	}
	return "\n" + tool.WrapExternal(sourceReplied, text.Truncate(body, n))
}

// when is at in loc as the turn's time line writes it; a zero time is an
// unknown one.
func when(at time.Time, loc *time.Location) string {
	if at.IsZero() {
		return unknownTime
	}
	return at.In(loc).Format(nowLayout)
}

// classifyReply sorts m's reply by got, the lookup's result, into the note for
// t's session, and counts it. It reads t.state, so on the additions path the
// caller holds t.mu; the lookup, which is I/O, runs before it is taken.
func (a *Agent) classifyReply(ctx context.Context, t *turn, m event.Message, got repliedRow) replyNote {
	note := replyNoteFor(m, got, t.state.Session, a.cfg.Location)
	a.observeReply(ctx, note.kind)
	return note
}
