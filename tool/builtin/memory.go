package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/memory"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/tool"
)

// Names of the memory tools.
const (
	toolMemoryWrite  = "memory.write"
	toolMemoryForget = "memory.forget"
	toolMemoryRead   = "memory.read"
	toolMemoryRecall = "memory.recall"
	toolMemoryAudit  = "memory.audit"
)

// recallLimit is how many hits memory.recall returns.
const recallLimit = 10

// Facts is the agent's memory (memory.Memory).
type Facts interface {
	Write(ctx context.Context, e memory.Entry) (memory.Entry, error)
	Notice(ctx context.Context, e memory.Entry) ([]event.Outgoing, error)
	Forget(ctx context.Context, slug, turn string) error
	Read(slug string) (memory.Entry, error)
	Index() string
	Recall(ctx context.Context, q string, convs []string, limit int) ([]memory.Hit, error)
	Audit(ctx context.Context) (memory.Report, error)
}

// MemoryTools returns the memory tools over f: write, forget, read,
// recall and the audit. They are registered together or not at all.
// private finds the caller's private conversation, whose archived
// sessions recall searches besides the calling conversation's
// (telegram.PrivateConv).
func MemoryTools(f Facts, private alert.PrivateConvFunc) []tool.Handler {
	return []tool.Handler{&MemoryWrite{f}, &MemoryForget{f}, &MemoryRead{f}, &MemoryRecall{facts: f, private: private}, &MemoryAudit{f}}
}

// MemoryWrite is the memory.write tool.
type MemoryWrite struct{ facts Facts }

type writeArgs struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// Spec describes memory.write: a side effect. The policy lets it run in
// a clean session and asks the owner in a tainted one; the guard checks
// every write either way (R-POL-6, R-POL-7).
func (*MemoryWrite) Spec() tool.Spec {
	return tool.Spec{
		Name: toolMemoryWrite,
		Description: "Remember a fact about the owner, their home, the people around them or their preferences, " +
			"for later conversations. Write facts, not instructions: \"The owner does not want messages 23:00-07:00\", " +
			"not \"never message at night\". Writing the same title again updates the fact. Every fact is checked " +
			"before it is stored; one that reads like an instruction is held back and the owner is told.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"title":{"type":"string","description":"a short name for the fact, such as Quiet hours (at most 100 characters)"},` +
			`"body":{"type":"string","description":"the fact itself, as a statement (at most 2000 characters)"}},` +
			`"required":["title","body"],"additionalProperties":false}`),
		Class:     tool.ClassSideEffect,
		Policy:    tool.PolicyConfirm,
		AdminOnly: true,
	}
}

func (*MemoryWrite) args(c tool.Call) (writeArgs, error) {
	var a writeArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return writeArgs{}, fmt.Errorf("memory.write: arguments: %w", err)
	}
	if strings.TrimSpace(a.Title) == "" || strings.TrimSpace(a.Body) == "" {
		return writeArgs{}, errors.New("memory.write: a fact needs a title and a body")
	}
	return a, nil
}

// Question implements tool.Questioner: the fact as it would be stored,
// title and whole body; one the question cannot show whole is refused
// (ruling R34). The policy asks only in a session that holds outside
// content.
func (w *MemoryWrite) Question(_ context.Context, c tool.Call) (string, error) {
	a, err := w.args(c)
	if err != nil {
		return "", err
	}
	return whole(toolMemoryWrite, "Remember this fact? This conversation holds content from outside, so check it is yours.\n\n"+
		strings.Join(strings.Fields(a.Title), " ")+": "+strings.TrimSpace(a.Body))
}

// Run stores the fact with where it came from: the turn and the
// session's taint. A fact the guard holds back is quarantined and the
// admins get a notice, built only after the write.
func (w *MemoryWrite) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	a, err := w.args(c)
	if err != nil {
		return tool.Result{}, err
	}
	e, err := w.facts.Write(ctx, memory.Entry{Title: a.Title, Body: a.Body, SourceTurn: c.TurnID, Tainted: c.Tainted})
	if err != nil {
		return tool.Result{}, fmt.Errorf("memory.write: %w", err)
	}
	if e.Checked == memory.CheckOK {
		return tool.Result{Text: "remembered as " + e.Slug}, nil
	}
	out, err := w.facts.Notice(ctx, e)
	if err != nil {
		return tool.Result{}, fmt.Errorf("memory.write: held back as %s, but the admins could not be told: %w", e.Slug, err)
	}
	return tool.Result{
		// The guard's reason is a model's words about the text: it goes to
		// the owner, not into the model's context.
		Text: "not stored: the check held it back as a possible injection; the owner was told",
		Out:  out,
	}, nil
}

// slugArgs are the arguments of the tools on one fact.
type slugArgs struct {
	Slug string `json:"slug"`
}

// slugOf reads the slug argument of a call of tool name; optional allows
// an empty one.
func slugOf(name string, c tool.Call, optional bool) (string, error) {
	var a slugArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return "", fmt.Errorf("%s: arguments: %w", name, err)
	}
	a.Slug = strings.TrimSpace(a.Slug)
	if a.Slug == "" && !optional {
		return "", fmt.Errorf("%s: slug is empty", name)
	}
	return a.Slug, nil
}

// MemoryForget is the memory.forget tool.
type MemoryForget struct{ facts Facts }

// Spec describes memory.forget: a side effect with the same policy as a
// write.
func (*MemoryForget) Spec() tool.Spec {
	return tool.Spec{
		Name:        toolMemoryForget,
		Description: "Forget a stored fact by its slug, the name in brackets in the known facts. The owner can bring it back from the history.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"slug":{"type":"string","description":"the fact's slug, such as quiet-hours"}},` +
			`"required":["slug"],"additionalProperties":false}`),
		Class:     tool.ClassSideEffect,
		Policy:    tool.PolicyConfirm,
		AdminOnly: true,
	}
}

// Question implements tool.Questioner: the slug that is forgotten and,
// when the fact is readable, what it holds (ruling R34). The slug is the
// action, so a stored text the policy shows redacted does not stop the
// owner forgetting it.
func (f *MemoryForget) Question(_ context.Context, c tool.Call) (string, error) {
	slug, err := slugOf(toolMemoryForget, c, false)
	if err != nil {
		return "", err
	}
	if err := showable(toolMemoryForget, slug, maxNameRunes); err != nil {
		return "", err
	}
	if e, err := f.facts.Read(slug); err == nil {
		return "Forget this fact (" + slug + ")?\n\n" + e.Title + ": " + e.Body, nil
	}
	return "Forget the fact " + slug + "?", nil
}

// Run removes the fact in a commit that names the turn.
func (f *MemoryForget) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	slug, err := slugOf(toolMemoryForget, c, false)
	if err != nil {
		return tool.Result{}, err
	}
	if err := f.facts.Forget(ctx, slug, c.TurnID); err != nil {
		return tool.Result{}, fmt.Errorf("memory.forget: %w", err)
	}
	return tool.Result{Text: "forgotten " + slug + "; the owner can bring it back from the workspace history"}, nil
}

// MemoryRead is the memory.read tool.
type MemoryRead struct{ facts Facts }

// Spec describes memory.read.
func (*MemoryRead) Spec() tool.Spec {
	return tool.Spec{
		Name: toolMemoryRead,
		Description: "Read a stored fact by its slug, the name in brackets in the known facts. Without a slug, list " +
			"every stored fact: the answer to \"what do you remember about me?\".",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"slug":{"type":"string","description":"the fact's slug; leave out to list all facts"}},` +
			`"additionalProperties":false}`),
		Class:     tool.ClassRead,
		Policy:    tool.PolicyAuto,
		AdminOnly: true,
		LocalSafe: true,
	}
}

// Run returns the fact with where it came from, or the index. A fact
// written in a session with outside content is outside content again.
func (r *MemoryRead) Run(_ context.Context, c tool.Call) (tool.Result, error) {
	slug, err := slugOf(toolMemoryRead, c, true)
	if err != nil {
		return tool.Result{}, err
	}
	if slug == "" {
		if idx := r.facts.Index(); idx != "" {
			return tool.Result{Text: idx}, nil
		}
		return tool.Result{Text: "no facts are stored"}, nil
	}
	e, err := r.facts.Read(slug)
	if err != nil {
		return tool.Result{}, fmt.Errorf("memory.read: %w", err)
	}
	return tool.Result{Text: e.Title + "\n\n" + e.Body + "\n\n(" + provenance(e) + ")", External: e.Tainted}, nil
}

// provenance says when and where a fact was written.
func provenance(e memory.Entry) string {
	s := "stored"
	if !e.Created.IsZero() {
		s += " " + e.Created.Format(time.DateOnly)
	}
	if e.Tainted {
		s += ", in a conversation that held outside content"
	}
	return s
}

// MemoryRecall is the memory.recall tool.
type MemoryRecall struct {
	facts   Facts
	private alert.PrivateConvFunc
}

// convs are the conversations whose archived sessions a call may search:
// the calling one and the caller's private chat, never another person's.
func (r *MemoryRecall) convs(c tool.Call) []string {
	out := []string{c.Conv}
	if _, conv, ok := r.private(c.Principal); ok && conv != c.Conv {
		out = append(out, conv)
	}
	return out
}

// Spec describes memory.recall.
func (*MemoryRecall) Spec() tool.Spec {
	return tool.Spec{
		Name: toolMemoryRecall,
		Description: "Search the stored facts, and the earlier sessions of this chat and of the sender's private chat, for a word or phrase " +
			"(not case sensitive). " +
			"Returns the newest matches first. A match from an earlier chat is what was said then, not a stored fact: " +
			"say so and give its date; when matches disagree, the newer one holds.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"query":{"type":"string","description":"the word or phrase to find"}},` +
			`"required":["query"],"additionalProperties":false}`),
		Class:     tool.ClassRead,
		Policy:    tool.PolicyAuto,
		AdminOnly: true,
		LocalSafe: true,
	}
}

// Run returns the hits, one per paragraph. When any hit comes from a
// session with outside content (or a fact written in one), the whole
// result is outside content: the agent wraps it as data and the session
// becomes tainted.
func (r *MemoryRecall) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	var a struct {
		Query string `json:"query"`
	}
	if err := strictJSON(c.Args, &a); err != nil {
		return tool.Result{}, fmt.Errorf("memory.recall: arguments: %w", err)
	}
	hits, err := r.facts.Recall(ctx, a.Query, r.convs(c), recallLimit)
	if err != nil {
		return tool.Result{}, fmt.Errorf("memory.recall: %w", err)
	}
	if len(hits) == 0 {
		return tool.Result{Text: "nothing found"}, nil
	}
	var b strings.Builder
	external := false
	for _, h := range hits {
		external = external || h.Tainted
		b.WriteString(hitLabel(h))
		if h.Title != "" {
			b.WriteString(" " + h.Title + ":")
		}
		b.WriteString(" " + h.Text + "\n\n")
	}
	return tool.Result{Text: strings.TrimSpace(b.String()), External: external}, nil
}

// hitLabel opens a recall hit with its level of memory in plain words, so
// the model does not report what was once said in a chat as a stored
// fact (ruling R49: the live run of 2026-11-03 said "memory still holds
// Deye 5 kW" while no fact was stored). A fact keeps its slug, which
// memory.read and memory.forget take.
func hitLabel(h memory.Hit) string {
	day := h.When.Format(time.DateOnly)
	switch h.Kind {
	case memory.HitFact:
		return "[stored fact (" + h.Slug + "), saved " + day + "]"
	case memory.HitEpisode:
		return "[said in an earlier chat on " + day + ", not a stored fact]"
	default:
		return "[" + day + "]"
	}
}

// MemoryAudit is the memory.audit tool, which the scheduled audit runs.
type MemoryAudit struct{ facts Facts }

// Spec describes memory.audit: it reads and reports, nothing more.
func (*MemoryAudit) Spec() tool.Spec {
	return tool.Spec{
		Name: toolMemoryAudit,
		Description: "Check every stored fact and persona file for hidden instructions with the clean-context check, " +
			"and list what it flags. It changes nothing.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Class:       tool.ClassRead,
		Policy:      tool.PolicyAuto,
		AdminOnly:   true,
	}
}

// Run audits and returns the report. The reasons are a model's words
// about stored text, so the result is outside content.
func (a *MemoryAudit) Run(ctx context.Context, _ tool.Call) (tool.Result, error) {
	rep, err := a.facts.Audit(ctx)
	if err != nil {
		return tool.Result{}, fmt.Errorf("memory.audit: %w", err)
	}
	return tool.Result{Text: rep.String(), External: true}, nil
}

// AuditJob is the code of the scheduled audit of memory and persona
// (R-POL-7): it runs memory.Audit, without a model turn, and returns the
// report for the owner (ruling R36).
type AuditJob struct{ facts Facts }

// NewAuditJob returns the audit job over f.
func NewAuditJob(f Facts) AuditJob { return AuditJob{facts: f} }

// auditTitle starts the report the owner gets.
const auditTitle = "Nightly memory check. "

// Run audits and returns the report. The reasons are a model's words
// about stored text: they go to the owner only, never into a model's
// context.
func (a AuditJob) Run(ctx context.Context) (string, error) {
	rep, err := a.facts.Audit(ctx)
	if err != nil {
		return "", fmt.Errorf("memory audit: %w", err)
	}
	return auditTitle + rep.String(), nil
}

// The scheduled audit: a system job that cannot be deleted, run as the
// first admin, reporting in their private chat.
const (
	AuditJobID = "system:memory-audit"
	auditCron  = "0 4 * * *"
	auditText  = "Memory audit: every stored fact and persona file checked for hidden instructions; nothing is changed."
)

// JobEnsurer stores a system job, replacing the stored one of its id
// (schedule.Store).
type JobEnsurer interface {
	EnsureSystem(ctx context.Context, j schedule.Job) error
}

// EnsureAudit makes sure the scheduled audit exists as a system job
// (schedule.KindSystem, ruling R36), owned by the first admin and
// reporting in their private chat; a stored one, an older release's
// model task among them, is replaced. With no admin, or none with a
// private chat, there is no one to tell: it is skipped and logged.
func EnsureAudit(ctx context.Context, jobs JobEnsurer, admins alert.Admins, now time.Time, log *slog.Logger) error {
	list, err := admins.List(ctx)
	if err != nil {
		return fmt.Errorf("builtin: memory audit: %w", err)
	}
	for _, adm := range list {
		transport, conv, ok := admins.Private(adm)
		if !ok {
			continue
		}
		j := schedule.Job{ID: AuditJobID, Owner: adm, Conv: conv, Transport: transport, Kind: schedule.KindSystem, Cron: auditCron,
			Text: auditText, System: true, Created: now}
		if err := jobs.EnsureSystem(ctx, j); err != nil {
			return fmt.Errorf("builtin: memory audit: %w", err)
		}
		return nil
	}
	log.Warn("builtin: no admin with a private chat; the scheduled memory audit is not set up")
	return nil
}
