package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/igorrius/ganglion/guard"
	"github.com/igorrius/ganglion/internal/redact"
	"github.com/igorrius/ganglion/memory"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
)

const toolPersonaPropose = "persona.propose"

// MaxPersonaRunes bounds the text of a proposed persona. The owner reads
// all of it in the question, which the policy cuts at 3000 characters, so
// the bound leaves room for the words around it.
const MaxPersonaRunes = 2000

// What the model reads when a proposal is refused. They are fixed: the
// guard's reason is a model's words about the text and stays out of the
// model's context (as in memory.write).
var (
	errPersonaTainted = errors.New("persona.propose: this conversation holds content from outside, so it cannot set the persona; " +
		"the owner can write the persona files by hand")
	errPersonaHeldBack = errors.New("persona.propose: the text did not pass the check for hidden instructions; " +
		"describe yourself in plain terms and propose again")
	errPersonaUnchecked = errors.New("persona.propose: the check for hidden instructions could not run; try again later")
	errPersonaSecret    = errors.New("persona.propose: the text holds something that looks like a secret; leave it out and propose again")
	errPersonaDone      = errors.New("persona.propose: you already have a persona; it changes only by hand")
)

// Persona is the persona files of the workspace (memory.Memory).
type Persona interface {
	PersonaEmpty() (bool, error)
	CheckPersona(ctx context.Context, text string) (guard.Verdict, error)
	WriteIdentity(ctx context.Context, text, turn string) error
}

// PersonaOption configures a PersonaPropose.
type PersonaOption func(*PersonaPropose)

// WithPersonaLogger sets where a check that could not run is reported.
// The default drops it.
func WithPersonaLogger(l *slog.Logger) PersonaOption { return func(p *PersonaPropose) { p.log = l } }

// PersonaPropose is the persona.propose tool of the first-run
// introduction.
type PersonaPropose struct {
	persona Persona
	log     *slog.Logger
}

// NewPersonaPropose returns the persona.propose tool over p.
func NewPersonaPropose(p Persona, opts ...PersonaOption) *PersonaPropose {
	t := &PersonaPropose{persona: p, log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(t)
	}
	return t
}

type proposeArgs struct {
	Text string `json:"text"`
}

// Spec describes persona.propose: a side effect that always asks the
// owner, in no session that holds outside content (R-POL-6).
func (*PersonaPropose) Spec() tool.Spec {
	return tool.Spec{
		Name: toolPersonaPropose,
		Description: "Propose your persona once you know the owner: who you are, your name, the tone, the language, " +
			"what to call the owner. Write it as a short plain description in the second person (\"You are ...\"), " +
			"without rules about tools or permissions. The text is checked, then the owner approves it with a button " +
			"and it is saved. Available only until you have a persona; afterwards the owner changes it by hand.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"text":{"type":"string","description":"the persona, at most 2000 characters"}},` +
			`"required":["text"],"additionalProperties":false}`),
		Class:     tool.ClassSideEffect,
		Policy:    tool.PolicyConfirm,
		AdminOnly: true,
	}
}

// Offered implements tool.Gated: only while persona/ has no *.md file. A
// directory that cannot be read is no reason to offer a write.
func (p *PersonaPropose) Offered() bool {
	empty, err := p.persona.PersonaEmpty()
	return err == nil && empty
}

// args reads and checks the text of a call.
func (*PersonaPropose) args(c tool.Call) (string, error) {
	var a proposeArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return "", fmt.Errorf("persona.propose: arguments: %w", err)
	}
	text := strings.TrimSpace(a.Text)
	switch {
	case text == "":
		return "", errors.New("persona.propose: the text is empty")
	case utf8.RuneCountInString(text) > MaxPersonaRunes:
		return "", fmt.Errorf("persona.propose: the text is longer than %d characters", MaxPersonaRunes)
	case redact.String(text) != text:
		// The question is redacted before the owner reads it: what is
		// shown must be what is written.
		return "", errPersonaSecret
	case c.Tainted:
		return "", errPersonaTainted
	}
	return text, nil
}

// Question implements tool.Questioner. The text passes the clean-context
// check first, so the owner is never asked to approve one it held back;
// the question shows the whole text.
func (p *PersonaPropose) Question(ctx context.Context, c tool.Call) (string, error) {
	text, err := p.args(c)
	if err != nil {
		return "", err
	}
	if empty, err := p.persona.PersonaEmpty(); err != nil {
		return "", fmt.Errorf("persona.propose: %w", err)
	} else if !empty {
		return "", errPersonaDone
	}
	v, err := p.persona.CheckPersona(ctx, text)
	switch {
	case err != nil:
		// Whatever the cause, an unusable check is not a pass. It is
		// logged here, without the text, and the model reads a fixed error.
		p.log.Warn("persona.propose: the check did not run", "err", err)
		return "", errPersonaUnchecked
	case v.Reason == guard.ReasonUnavailable && !v.OK:
		p.log.Warn("persona.propose: the check was unavailable")
		return "", errPersonaUnchecked
	case !v.OK:
		return "", errPersonaHeldBack
	}
	return c.Speak.Text(texts.AskPersona, texts.Vars{"file": memory.IdentityFile, varText: text}), nil
}

// Run writes the approved persona. The session's taint counts as at the
// call and as at the approval, whichever is tainted (the agent combines
// them in the call), so a proposal parked in a clean session is refused
// if the session took in outside content before the owner answered. The
// text is not checked again: it is the one the owner was shown.
func (p *PersonaPropose) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	text, err := p.args(c)
	if err != nil {
		return tool.Result{}, err
	}
	switch err := p.persona.WriteIdentity(ctx, text, c.TurnID); {
	case errors.Is(err, memory.ErrPersonaExists):
		return tool.Result{}, errPersonaDone
	case err != nil:
		return tool.Result{}, fmt.Errorf("persona.propose: %w", err)
	}
	return tool.Result{Text: "persona saved as " + memory.IdentityFile + "; from now on it describes you"}, nil
}
