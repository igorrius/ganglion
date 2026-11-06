package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
)

const toolLanguageSet = "language.set"

// LanguageChooser records the language a person asked for (language.Store).
type LanguageChooser interface {
	Choose(ctx context.Context, principal string, l texts.Lang) (changed bool, err error)
}

// LanguageSet is the language.set tool: a person's lasting choice of the language the node writes to
// them in (R-LANG-3). It takes no target, so it changes only the caller's own language.
type LanguageSet struct{ languages LanguageChooser }

// NewLanguageSet returns the language.set tool over c.
func NewLanguageSet(c LanguageChooser) *LanguageSet { return &LanguageSet{languages: c} }

// Spec describes language.set: admins only until permissions exist. The policy applies it at once in
// a clean session and asks in a tainted one (ruleLanguageSet).
func (*LanguageSet) Spec() tool.Spec {
	return tool.Spec{
		Name: toolLanguageSet,
		Description: "Use when the person asks you to always write to them in another language, or says they do not " +
			"understand yours. Not for translating one text. code is an ISO 639-1 language code.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"code":{"type":"string","description":"e.g. uk"}},"required":["code"],"additionalProperties":false}`),
		Class:     tool.ClassSideEffect,
		Policy:    tool.PolicyAuto,
		AdminOnly: true,
		LocalSafe: true,
	}
}

// args reads and checks the language of a call against the table of native names.
func (*LanguageSet) args(c tool.Call) (texts.Lang, error) {
	var a struct {
		Code string `json:"code"`
	}
	if err := strictJSON(c.Args, &a); err != nil {
		return "", fmt.Errorf("language.set: arguments: %w", err)
	}
	l, ok := texts.Normalize(a.Code)
	if !ok {
		return "", fmt.Errorf("language.set: %q is not a language this node can use", a.Code)
	}
	return l, nil
}

// Question implements tool.Questioner: in the requested language, with the same question in the
// current one under it, since a request from an injected page looks the same to code as a real one.
// The model supplies only the code; the text comes from the files.
func (t *LanguageSet) Question(_ context.Context, c tool.Call) (string, error) {
	l, err := t.args(c)
	if err != nil {
		return "", err
	}
	name := texts.Vars{"language": texts.Native(l)}
	var first string
	if target := c.Speak.In(l); target.Has() {
		first = target.Text(texts.AskLanguage, name)
	} else {
		first = c.Speak.In(texts.English).Text(texts.AskLanguageNoFile, name)
	}
	second := c.Speak.Text(texts.AskLanguage, name)
	if first == second {
		return first, nil
	}
	return first + "\n" + second, nil
}

// Run records the choice. A scheduled run has no person to choose. Run again under the same key it
// changes nothing and says the same.
func (t *LanguageSet) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	if c.Grant != nil {
		return tool.Result{}, errors.New("language.set: a scheduled run has no person to choose a language")
	}
	l, err := t.args(c)
	if err != nil {
		return tool.Result{}, err
	}
	if _, err := t.languages.Choose(ctx, c.Principal, l); err != nil {
		return tool.Result{}, fmt.Errorf("language.set: %w", err)
	}
	return tool.Result{Text: "language set to " + string(l) + "; write to this person in it from now on"}, nil
}
