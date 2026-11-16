package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/igorrius/ganglion/confighist"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/restart"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/pending"
)

// This file is the agent's tools for the node's file (R-CFG-14 – R-CFG-16,
// ADR-0093): config.propose changes the allowlisted fields with the
// owner's press, config.history lists the recorded changes, and
// config.rollback proposes a change's earlier values. A change is written
// at once and takes effect at the next start (R-CFG-6); the message that
// says so carries a Restart now button.

// Names of the config tools and the restart button's action.
const (
	ConfigProposeTool   = "config.propose"
	ConfigHistoryTool   = "config.history"
	ConfigRollbackTool  = "config.rollback"
	ActionConfigRestart = "config.restart"
)

// Bounds of a proposal.
const (
	maxConfigChanges = 10
	maxReasonRunes   = 300
	// configButtonsTTL is how long Restart now can be pressed.
	configButtonsTTL = 24 * time.Hour
)

// varReason is the reason's placeholder in a question.
const varReason = "reason"

// markRestart is the symbol on the restart button, outside the translated
// label (R-LANG-6).
const markRestart = "🔄 "

// RestartFunc ends the node for its supervisor to start again (the
// /restart path, agent.RestartFunc).
type RestartFunc func()

// Configuring is the config tools over the node's file.
type Configuring struct {
	// File is the node's file.
	File string
	// History records every change of it.
	History confighist.History
	// Buttons keeps the Restart now button.
	Buttons ButtonStore
	// Restart ends the node for its supervisor.
	Restart RestartFunc
	// Note records who pressed Restart now, so the node says it is back.
	Note restart.Asker
	// Now is the clock.
	Now func() time.Time
}

// Tools are the config tools.
func (g Configuring) Tools() []tool.Handler {
	return []tool.Handler{
		&configTool{spec: tool.Spec{Name: ConfigProposeTool,
			Description: "Change settings of the node's file (ganglion.yaml) at the owner's request. Only these fields: " +
				strings.Join(settings.AllowedPaths, ", ") + ". Any other field is changed on the host or by its own tool; " +
				"the refusal says which. value is the new value as the file holds it (a duration like \"30m\", a number, " +
				"true or false, a string), or null for the default. The owner confirms; the change takes effect after a restart.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` +
				`"changes":{"type":"array","minItems":1,"maxItems":10,"items":{"type":"object","properties":{` +
				`"path":{"type":"string","description":"a dotted field, such as agent.limits.daily_turns"},` +
				`"value":{"description":"the new value, or null for the default"}},` +
				`"required":["path","value"],"additionalProperties":false}},` +
				`"reason":{"type":"string","description":"why, in the owner's words, one line"}},` +
				`"required":["changes","reason"],"additionalProperties":false}`),
			Class: tool.ClassSideEffect, Policy: tool.PolicyConfirm, AdminOnly: true},
			question: g.proposeQuestion, run: g.propose},
		&configTool{spec: tool.Spec{Name: ConfigHistoryTool,
			Description: "List the latest recorded changes of the node's file: id, date, who changed it (propose, rollback, " +
				"module, destination, group, host), the reason and the diff.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` +
				`"limit":{"type":"integer","minimum":1,"maximum":20,"description":"how many, 10 by default"}},` +
				`"additionalProperties":false}`),
			Class: tool.ClassRead, Policy: tool.PolicyAuto, AdminOnly: true}, run: g.history},
		&configTool{spec: tool.Spec{Name: ConfigRollbackTool,
			Description: "Roll back a recorded change of the node's file: the allowlisted fields it changed go back to their " +
				"earlier values. change is an id from config.history; without it, the latest change of an allowlisted field. " +
				"Modules, destinations, groups and host edits are not rolled back here; the question names them. The owner confirms.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{` +
				`"change":{"type":"string","description":"a change's id from config.history"}},` +
				`"additionalProperties":false}`),
			Class: tool.ClassSideEffect, Policy: tool.PolicyConfirm, AdminOnly: true},
			question: g.rollbackQuestion, run: g.rollback},
	}
}

// QuickActions are the config buttons by action name.
func (g Configuring) QuickActions() map[string]pending.QuickAction {
	return map[string]pending.QuickAction{ActionConfigRestart: g.restartByButton}
}

// restartByButton ends the node, as /restart does, and records who
// pressed so the node says it is back.
func (g Configuring) restartByButton(_ context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	if err := g.Note.Ask(p.Button.Principal, p.Transport, p.Button.Conv); err != nil {
		return pending.QuickEdit{}, err
	}
	g.Restart()
	return pending.QuickEdit{Text: p.Speak.Text(texts.ReplyRestarting, nil), Reviewed: true}, nil
}

// configFunc is the question or the run of a config tool; the question's
// text is in Result.Text.
type configFunc func(ctx context.Context, c tool.Call) (tool.Result, error)

// configTool is one config tool.
type configTool struct {
	spec     tool.Spec
	question configFunc // nil for a tool that asks nothing
	run      configFunc
}

// Spec implements tool.Handler.
func (t *configTool) Spec() tool.Spec { return t.spec }

// Question implements tool.Questioner; a refusal comes without a question.
func (t *configTool) Question(ctx context.Context, c tool.Call) (string, error) {
	if t.question == nil {
		return "", nil
	}
	if err := t.allowed(c); err != nil {
		return "", err
	}
	q, err := t.question(ctx, c)
	if err != nil {
		return "", fmt.Errorf("%s: %w", t.spec.Name, err)
	}
	return whole(t.spec.Name, q.Text)
}

// Run implements tool.Handler.
func (t *configTool) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	if err := t.allowed(c); err != nil {
		return tool.Result{}, err
	}
	res, err := t.run(ctx, c)
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", t.spec.Name, err)
	}
	return res, nil
}

// allowed refuses a scheduled run: the node's settings are the admin's
// word in a chat, never a task's.
func (t *configTool) allowed(c tool.Call) error {
	if c.Grant != nil {
		return fmt.Errorf("%s: a scheduled run does not change the node's settings", t.spec.Name)
	}
	return nil
}

// proposal is a checked config.propose call.
type proposal struct {
	changes []settings.Change
	reason  string
}

// configProposeArgs are config.propose's arguments.
type configProposeArgs struct {
	Changes []struct {
		Path  string          `json:"path"`
		Value json.RawMessage `json:"value"`
	} `json:"changes"`
	Reason string `json:"reason"`
}

// errNoChanges is a proposal with nothing to change.
var errNoChanges = errors.New("no changes")

// proposalOf reads and bounds config.propose's arguments.
func proposalOf(c tool.Call) (proposal, error) {
	var a configProposeArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return proposal{}, fmt.Errorf("arguments: %w", err)
	}
	reason := strings.Join(strings.Fields(a.Reason), " ")
	switch {
	case len(a.Changes) == 0:
		return proposal{}, errNoChanges
	case len(a.Changes) > maxConfigChanges:
		return proposal{}, fmt.Errorf("at most %d changes at once", maxConfigChanges)
	case reason == "" || utf8.RuneCountInString(reason) > maxReasonRunes:
		return proposal{}, fmt.Errorf("reason: one line of 1 to %d characters", maxReasonRunes)
	}
	p := proposal{reason: reason}
	for _, ch := range a.Changes {
		if slices.ContainsFunc(p.changes, func(x settings.Change) bool { return x.Path == ch.Path }) {
			return proposal{}, fmt.Errorf("%s: named twice", ch.Path)
		}
		var v any
		if err := json.Unmarshal(ch.Value, &v); err != nil {
			return proposal{}, fmt.Errorf("%s: value: %w", ch.Path, err)
		}
		p.changes = append(p.changes, settings.Change{Path: ch.Path, Value: v})
	}
	return p, nil
}

// checked are the values of the changed fields before a change, read
// from the file as it is now; a field missing from the file has none.
type checked map[string]any

// check refuses a change of a field off the allowlist, naming where it is
// changed, and one the start would refuse; it returns the fields' current
// values (R-CFG-14, R-CFG-15).
func (g Configuring) check(cs []settings.Change) (checked, error) {
	paths := make([]string, 0, len(cs))
	for _, ch := range cs {
		if !settings.Allowed(ch.Path) {
			return nil, fmt.Errorf("%w: %s is changed %s", settings.ErrNotAllowed, ch.Path, placeWords(settings.Where(ch.Path)))
		}
		paths = append(paths, ch.Path)
	}
	current, err := readConfig(g.File)
	if err != nil {
		return nil, err
	}
	if _, err := settings.Apply(current, cs); err != nil {
		return nil, err
	}
	old, err := settings.Values(current, paths)
	if err != nil {
		return nil, err
	}
	return old, nil
}

// placeWords says where a field off the allowlist is changed, for the
// model to tell the owner.
func placeWords(p settings.Place) string {
	switch p {
	case settings.PlaceOwnTool:
		return "by its own tool (modules, destinations, groups, the update mode), not here"
	case settings.PlaceChat:
		return "here"
	case settings.PlaceHost:
		return "on the host, in the file, by the owner: it widens the node, moves its data or names a secret"
	default:
		return "on the host"
	}
}

// readConfig reads the node's file; a missing file is an empty one.
func readConfig(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("the node's file: %w", err)
	}
	return b, nil
}

// proposeQuestion is the card of config.propose.
func (g Configuring) proposeQuestion(_ context.Context, c tool.Call) (tool.Result, error) {
	p, err := proposalOf(c)
	if err != nil {
		return tool.Result{}, err
	}
	old, err := g.check(p.changes)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: changeQuestion(c, p, old, "")}, nil
}

// changeQuestion is the card of a change: a line per field, old → new,
// the reason, a note when the session holds outside content, and an
// extra line (what a rollback leaves).
func changeQuestion(c tool.Call, p proposal, old checked, extra string) string {
	q := c.Speak.Text(texts.AskConfigPropose, texts.Vars{"lines": changeLines(c.Speak, p.changes, old),
		varReason: texts.Escape(p.reason)})
	if extra != "" {
		q += "\n\n" + extra
	}
	if c.Tainted {
		q += "\n\n" + c.Speak.Text(texts.AskConfigTainted, nil)
	}
	return q
}

// changeLines are "path: old → new", one per change.
func changeLines(sp texts.Speaker, cs []settings.Change, old checked) string {
	lines := make([]string, 0, len(cs))
	for _, ch := range cs {
		before, ok := old[ch.Path]
		lines = append(lines, "• "+texts.Escape(ch.Path)+": "+configValue(sp, before, ok)+" → "+configValue(sp, ch.Value, ch.Value != nil))
	}
	return strings.Join(lines, "\n")
}

// configValue is a value as a question shows it; a missing one is the default.
func configValue(sp texts.Speaker, v any, ok bool) string {
	if !ok || v == nil {
		return sp.Text(texts.AskConfigDefault, nil)
	}
	switch x := v.(type) {
	case string:
		return texts.Escape(x)
	case map[string]any, []any:
		b, err := json.Marshal(x)
		if err != nil {
			return texts.Escape(fmt.Sprint(x))
		}
		return texts.Escape(string(b))
	default:
		return texts.Escape(fmt.Sprint(x))
	}
}

// propose writes an approved config.propose.
func (g Configuring) propose(ctx context.Context, c tool.Call) (tool.Result, error) {
	p, err := proposalOf(c)
	if err != nil {
		return tool.Result{}, err
	}
	return g.write(ctx, c, p, confighist.SourcePropose)
}

// write checks the change again against the file as it is now, writes it
// through the history and answers with the values just before the write
// and a Restart now button (R-CFG-15).
func (g Configuring) write(ctx context.Context, c tool.Call, p proposal, src confighist.Source) (tool.Result, error) {
	old, err := g.check(p.changes)
	if err != nil {
		return tool.Result{}, err
	}
	if err := g.History.Record(ctx, src, p.reason, func() error { return settings.SetPath(g.File, p.changes) }); err != nil {
		return tool.Result{}, err
	}
	button := pending.Button{ID: pending.NewButtonID(), Action: ActionConfigRestart,
		Label: markRestart + c.Speak.Label(texts.ButtonConfigRestart), Principal: c.Principal, Conv: c.Conv,
		Expires: g.Now().Add(configButtonsTTL)}
	if err := g.Buttons.AddButtons(ctx, button); err != nil {
		return tool.Result{}, err
	}
	out := event.Outgoing{IdemKey: c.Key() + ":config", Transport: c.Transport, Conv: c.Conv,
		Text: c.Speak.Text(texts.ReplyConfigWritten, nil), Buttons: []event.Button{{ID: button.ID, Label: button.Label}}}
	return tool.Result{Text: "Written; it takes effect after a restart, which the owner can start with the button below. " +
		"Values just before the write:\n" + plainLines(p.changes, old), Out: []event.Outgoing{out}}, nil
}

// plainLines are "path: old -> new" for the model.
func plainLines(cs []settings.Change, old checked) string {
	var b strings.Builder
	for _, ch := range cs {
		before, ok := old[ch.Path]
		if !ok {
			before = "default"
		}
		after := ch.Value
		if after == nil {
			after = "default"
		}
		fmt.Fprintf(&b, "- %s: %v -> %v\n", ch.Path, before, after)
	}
	return b.String()
}

// sameValue reports whether two decoded YAML values are equal; a missing
// value equals only a missing one.
func sameValue(a any, aok bool, b any, bok bool) bool {
	return aok == bok && reflect.DeepEqual(a, b)
}
