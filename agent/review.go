package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/memory"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/turns"
)

// reviewInputRunes caps the conversation a review reads: the newest
// unread lines that fit; older ones are skipped and counted.
const reviewInputRunes = 12000

// memoryNotePrefix starts the node's note of what a review stored, for the
// model (and for the next review, which leaves it out).
const memoryNotePrefix = "[memory] "

// noReviewTopics stands for the topics when none is stored.
const noReviewTopics = "(no topics are stored yet)"

// nodeNotes start the user-role lines the node writes into the history:
// they are not the owner's words, so the review leaves them out.
var nodeNotes = []string{commandPrefix, buttonPrefix, confirmationPrefix, reminderPrefix, commandFinishedPrefix,
	moduleNotePrefix, buildFinishedPrefix, memoryNotePrefix}

// Review is what the memory review needs from memory and its notes
// (memory.Memory and builtin.MemoryNotes, joined in config).
type Review interface {
	Apply(ctx context.Context, ops []memory.Op, src memory.Source) (memory.Applied, error)
	Notice(ctx context.Context, c memory.Change) ([]event.Outgoing, error)
	Note(ctx context.Context, sp texts.Speaker, key, transport, conv, principal string, changes []memory.Change,
		commits []string) (event.Outgoing, bool, error)
	Proposal(ctx context.Context, sp texts.Speaker, key, transport, conv, principal string, ops []memory.Op) (event.Outgoing, error)
}

// NoReview is the Review of a node without memory: it stores nothing.
type NoReview struct{}

// Apply implements Review.
func (NoReview) Apply(context.Context, []memory.Op, memory.Source) (memory.Applied, error) {
	return memory.Applied{}, nil
}

// Notice implements Review.
func (NoReview) Notice(context.Context, memory.Change) ([]event.Outgoing, error) { return nil, nil }

// Note implements Review.
func (NoReview) Note(context.Context, texts.Speaker, string, string, string, string, []memory.Change, []string) (event.Outgoing, bool, error) {
	return event.Outgoing{}, false, nil
}

// Proposal implements Review.
func (NoReview) Proposal(context.Context, texts.Speaker, string, string, string, string, []memory.Op) (event.Outgoing, error) {
	return event.Outgoing{}, nil
}

// armAfterTurn moves the live session's review to a quiet period after an
// admin's turn that answered a person's message (R-MEM-8). A task's run, a
// module's event, a command and a turn that failed arm nothing.
func (a *Agent) armAfterTurn(ctx context.Context, t *turn) {
	if t.event.Kind != event.KindMessage || t.made != "" || t.grant != nil || !t.admin || t.record.Outcome != turns.OutcomeOK {
		return
	}
	a.arm(ctx, t.event.Conv, "", t.transport, t.msg.From, t.state, t.now.Add(a.cfg.ReviewQuiet))
}

// armArchived makes an ended session's unread messages due at once, so a
// new session starts with what the old one taught.
func (a *Agent) armArchived(ctx context.Context, t *turn, st State) {
	admin, err := a.d.IsAdmin(ctx, t.msg.From)
	if err != nil || !admin {
		return
	}
	a.arm(ctx, t.event.Conv, st.Session, t.transport, t.msg.From, st, t.now)
}

// arm sets when conv's session is due, when the review is on, the session
// has unread messages and is not local-only, and conv is principal's
// private chat: only there are the owner's words only the owner's (ruling
// 3 of the M5 plan). A failure is logged: the turn stays done.
func (a *Agent) arm(ctx context.Context, conv, session, transport, principal string, st State, due time.Time) {
	if !a.cfg.Review || st.LocalOnly || st.Reviewed >= len(st.Messages) || !a.private(principal, conv) {
		return
	}
	if err := a.d.Reviews.Arm(ctx, conv, session, transport, principal, due); err != nil {
		a.log.Warn("agent: memory review not armed", "conv", conv, "err", err)
	}
}

// private reports whether conv is principal's private chat.
func (a *Agent) private(principal, conv string) bool {
	_, own, ok := a.d.Private(principal)
	return ok && own == conv
}

// memoryReview reads a quiet conversation's unread owner lines once and
// stores what the turns missed (R-MEM-8, ADR-0072). It sends no reply of
// its own: a note with Undo when it stored something, a question when the
// session held outside content. Every step that finds nothing to do
// stops, and one with no owner line makes no model call.
func (a *Agent) memoryReview(ctx context.Context, t *turn) error {
	t.record.Outcome = turns.OutcomeReview
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !a.cfg.Review || !t.admin || !a.private(t.msg.From, t.event.Conv) {
		a.observeReview(ctx, t.event.Conv, reviewSkipped)
		return nil
	}
	st, live, err := a.reviewedState(ctx, t)
	if err != nil || st == nil {
		return err
	}
	if st.LocalOnly {
		a.markReviewed(ctx, t, st, live)
		a.observeReview(ctx, t.event.Conv, reviewSkipped)
		return nil
	}
	in := reviewInput(st.Messages, st.Reviewed, reviewInputRunes)
	a.observeReviewUnread(ctx, in.skipped)
	if in.owner == 0 {
		a.markReviewed(ctx, t, st, live)
		a.observeReview(ctx, t.event.Conv, reviewNothing)
		return nil
	}
	ops, ok := a.askReview(ctx, t, *st, in.text)
	if !ok {
		return nil
	}
	if err := a.storeReview(ctx, t, *st, ops, live); err != nil {
		return err
	}
	a.markReviewed(ctx, t, st, live)
	return nil
}

// reviewedState is the session the event names: the live one, or an
// archived one read from the archive; nil when that is gone.
func (a *Agent) reviewedState(ctx context.Context, t *turn) (*State, bool, error) {
	if t.review.Session == "" || t.review.Session == t.state.Session {
		// The live session, also an ended one the owner resumed since.
		return &t.state, true, nil
	}
	st, err := a.d.Archive.Get(ctx, t.event.Conv, t.review.Session)
	if err != nil {
		a.log.Info("agent: memory review of a session not in the archive", "conv", t.event.Conv, "session", t.review.Session, "err", err)
		a.observeReview(ctx, t.event.Conv, reviewSkipped)
		return nil, false, nil
	}
	return &st, false, nil
}

// markReviewed marks the session read up to its end: the live one in the
// state the turn saves, an archived one in the archive, so it is not read
// again when the owner resumes it. A failure there is logged: the review
// is done.
func (a *Agent) markReviewed(ctx context.Context, t *turn, st *State, live bool) {
	if live {
		t.state.Reviewed = len(t.state.Messages)
		return
	}
	if err := a.d.Archive.MarkReviewed(ctx, t.event.Conv, t.review.Session, len(st.Messages)); err != nil {
		a.log.Warn("agent: memory review mark not stored", "conv", t.event.Conv, "session", t.review.Session, "err", err)
	}
}

// askReview makes the review's one model call: the fixed prompt, the
// topics and the conversation as data, no tools. ok is false when no model
// answered (the review is armed again a quiet period later, the mark
// stays) or the answer could not be read (counted; read anyway, so a bad
// answer does not loop).
func (a *Agent) askReview(ctx context.Context, t *turn, st State, conversation string) ([]memory.Op, bool) {
	topics := a.d.Facts()
	if topics == "" {
		topics = noReviewTopics
	}
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: memory.ReviewPrompt},
		{Role: provider.RoleUser, Content: tool.WrapExternal("memory", topics) + "\n\n" + tool.WrapExternal("conversation", conversation)},
	}
	resp, end := a.call(ctx, router.TaskReq{Purpose: router.PurposeMemory, Privacy: st.privacy()}, provider.ChatRequest{Messages: msgs},
		&t.record, noCalls)
	t.record.Outcome = turns.OutcomeReview
	if end != turns.OutcomeOK {
		a.arm(ctx, t.event.Conv, t.review.Session, t.transport, t.msg.From, st, t.now.Add(a.cfg.ReviewQuiet))
		a.observeReview(ctx, t.event.Conv, reviewNoModel)
		return nil, false
	}
	ops, err := memory.ParseOps(resp.Message.Content)
	if err != nil {
		a.log.Warn("agent: the memory review's answer is unreadable", "conv", t.event.Conv)
		a.observeReview(ctx, t.event.Conv, reviewUnreadable)
		a.markReviewed(ctx, t, &st, t.review.Session == "" || t.review.Session == t.state.Session)
		return nil, false
	}
	if len(ops) == 0 {
		a.observeReview(ctx, t.event.Conv, reviewNothing)
	}
	return ops, true
}

// storeReview stores ops one by one, each under its own key so a rerun
// finds its commit; an op that does not fit memory (a topic or fact that
// is not there, a full topic) is dropped and counted. A session that held
// outside content stores nothing: the owner gets the facts with Remember
// and Skip (R-POL-6).
func (a *Agent) storeReview(ctx context.Context, t *turn, st State, ops []memory.Op, live bool) error {
	if len(ops) == 0 {
		return nil
	}
	sp := a.speaker(ctx, t)
	key := t.event.IdemKey + ":memory"
	if st.Tainted {
		o, err := a.d.Review.Proposal(ctx, sp, key, t.transport, t.event.Conv, t.msg.From, ops)
		if err != nil {
			return fmt.Errorf("agent: memory review question: %w", err)
		}
		t.out = append(t.out, o)
		a.observeReview(ctx, t.event.Conv, reviewAsked)
		return nil
	}
	var changes []memory.Change
	var commits []string
	for i, op := range ops {
		applied, err := a.d.Review.Apply(ctx, []memory.Op{op}, memory.Source{Turn: t.event.ID, Key: t.event.ID + ":" + strconv.Itoa(i)})
		if errors.Is(err, memory.ErrInvalid) || errors.Is(err, memory.ErrNotFound) {
			a.observeReviewOp(ctx, reviewOpDropped)
			continue
		}
		if err != nil {
			return fmt.Errorf("agent: memory review: %w", err)
		}
		changes, commits = append(changes, applied.Changes...), append(commits, applied.Commit)
	}
	return a.tellReview(ctx, t, sp, key, changes, commits, live)
}

// tellReview sends what the review stored: an alert for each held-back
// text, the note with Undo, and in the live session a note for the model.
func (a *Agent) tellReview(ctx context.Context, t *turn, sp texts.Speaker, key string, changes []memory.Change, commits []string, live bool) error {
	var stored []string
	for _, c := range changes {
		if c.Held {
			a.observeReviewOp(ctx, reviewOpHeld)
			out, err := a.d.Review.Notice(ctx, c)
			if err != nil {
				return fmt.Errorf("agent: memory review alert: %w", err)
			}
			t.out = append(t.out, out...)
			continue
		}
		a.observeReviewOp(ctx, reviewOpApplied)
		stored = append(stored, strings.TrimSpace(c.Slug+" "+c.Fact))
	}
	o, ok, err := a.d.Review.Note(ctx, sp, key, t.transport, t.event.Conv, t.msg.From, changes, commits)
	if err != nil {
		return fmt.Errorf("agent: memory review note: %w", err)
	}
	if !ok {
		a.observeReview(ctx, t.event.Conv, reviewNothing)
		return nil
	}
	t.out = append(t.out, o)
	a.observeReview(ctx, t.event.Conv, reviewStored)
	if live {
		t.noteQuiet(memoryNotePrefix + "the memory review stored: " + strings.Join(stored, ", "))
	}
	return nil
}

// reviewText is what a review reads of a conversation.
type reviewText struct {
	text    string
	owner   int // owner lines in text
	skipped int // unread lines left out by the cap
}

// reviewInput is the unread part of msgs, from from on, as the review
// reads it (R-MEM-8): the owner's lines and the assistant's replies as
// plain text, oldest first. A tool's call and result, a node's note and
// any text inside an <external> wrapper never reach it: outside text has
// no path into memory but the owner's press (ADR-0072). Only the newest
// lines within limit runes are kept.
func reviewInput(msgs []provider.Message, from, limit int) reviewText {
	var lines []string
	var owner []bool
	for _, m := range msgs[min(max(from, 0), len(msgs)):] {
		line, isOwner, ok := reviewLine(m)
		if ok {
			lines, owner = append(lines, line), append(owner, isOwner)
		}
	}
	start, size := len(lines), 0
	for start > 0 {
		n := utf8.RuneCountInString(lines[start-1]) + 1
		if size+n > limit {
			break
		}
		size += n
		start--
	}
	out := reviewText{text: strings.Join(lines[start:], "\n"), skipped: start}
	for _, o := range owner[start:] {
		if o {
			out.owner++
		}
	}
	return out
}

// reviewLine is one message as the review reads it; ok is false for a
// message it leaves out.
func reviewLine(m provider.Message) (line string, owner, ok bool) {
	text := strings.Join(strings.Fields(tool.StripExternal(m.Content)), " ")
	switch {
	case text == "":
		return "", false, false
	case m.Role == provider.RoleAssistant:
		return "[assistant] " + text, false, true
	case m.Role != provider.RoleUser:
		return "", false, false
	}
	for _, p := range nodeNotes {
		if strings.HasPrefix(m.Content, p) {
			return "", false, false
		}
	}
	return "[owner] " + text, true, true
}
