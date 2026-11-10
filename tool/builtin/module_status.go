package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
)

// Limits of what module.status shows of a config value.
const (
	// maxShownValue is the longest config value shown; a longer one is
	// "<set>".
	maxShownValue = 200
	// hiddenValue stands for a value module.status does not show.
	hiddenValue = "<set>"
)

// plainKey is a config key module.status names: the manifest's key names
// are the module's text, so only a plain one is shown (R-RUN-14).
var plainKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// secretKey is a key name that suggests a secret: its value is not shown,
// even though config values are the owner's and never secrets by design.
var secretKey = regexp.MustCompile(`(?i)secret|passw|token|key|auth|cred|pin|private`)

// ModuleStatus is module.status: what the owner sees on a module's card, for
// the agent: its state, active version, health with the node's hint, runs and
// the last failure from the journal, and its settings with the owner's
// values. Everything is the node's own data; no text the module wrote.
type ModuleStatus struct{ m Modules }

type statusArgs struct {
	Name string `json:"name"`
}

// configAnswer is one declared config key and what the owner set.
type configAnswer struct {
	Key        string `json:"key"`
	Type       string `json:"type"`
	Required   bool   `json:"required"`
	Set        bool   `json:"set"`
	Value      string `json:"value,omitempty"`
	HasDefault bool   `json:"has_default,omitempty"`
}

// failureAnswer is the active version's last failure.
type failureAnswer struct {
	Kind string `json:"kind"`
	At   string `json:"at"`
}

// statusAnswer is module.status's answer.
type statusAnswer struct {
	Module      string         `json:"module"`
	State       string         `json:"state"`
	Version     string         `json:"version,omitempty"`
	Health      string         `json:"health"`
	HealthHint  string         `json:"health_hint,omitempty"`
	Runs        int            `json:"runs"`
	Failures    int            `json:"failures"`
	LastFailure *failureAnswer `json:"last_failure,omitempty"`
	Config      []configAnswer `json:"config"`
	KeysHidden  int            `json:"config_keys_not_shown,omitempty"`
}

// Spec implements tool.Handler.
func (*ModuleStatus) Spec() tool.Spec {
	return tool.Spec{Name: ModuleStatusTool,
		Description: "How an installed module of the owner stands, as its /modules card shows it: state (running; off, turned off by the owner; " +
			"crashloop, turned off by the node after 5 failures in an hour; stopped, it should run and did not start), active version, " +
			"health (its check now: ok, or the kind it failed with and the node's hint), runs, failures and the last failure's kind and time, " +
			"and its settings: each declared key with its type, whether it is required and the value the owner set. " +
			"Use it when a module fails, before guessing the cause.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"name":{"type":"string","description":"the module's name, as in its tools (deye-reader)"}},` +
			`"required":["name"],"additionalProperties":false}`),
		Class: tool.ClassRead, Policy: tool.PolicyAuto, AdminOnly: true}
}

// Run implements tool.Handler. The answer is outside content: the settings'
// values come from a file the agent did not write.
func (t *ModuleStatus) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	var a statusArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return tool.Result{}, fmt.Errorf("%s: arguments: %w", ModuleStatusTool, err)
	}
	name := modstore.ShortName(a.Name)
	if err := moduleName(ModuleStatusTool, name); err != nil {
		return tool.Result{}, err
	}
	card, err := t.m.Host.Card(ctx, name)
	if errors.Is(err, modstore.ErrUnknownModule) {
		return tool.Result{}, fmt.Errorf("%s: no installed module named %s", ModuleStatusTool, name)
	}
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ModuleStatusTool, err)
	}
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // "<set>" reads as itself
	if err := enc.Encode(t.answer(card)); err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: strings.TrimSuffix(b.String(), "\n"), External: true}, nil
}

// answer is the card in module.status's words.
func (t *ModuleStatus) answer(c modstore.Card) statusAnswer {
	out := statusAnswer{Module: c.Name, State: string(c.State), Version: c.Version, Config: []configAnswer{}}
	switch c.Health {
	case "":
		out.Health = texts.Shipped().Label(texts.English, texts.HintModuleNotRunning, nil)
	case modstore.HealthOK:
		out.Health = string(c.Health)
	default:
		out.Health, out.HealthHint = string(c.Health), t.m.Host.Hint(c.Name, c.Health)
	}
	if e, ok := activeEntry(c); ok {
		out.Runs, out.Failures = e.Runs, e.Failures
		if !e.LastFailure.IsZero() {
			out.LastFailure = &failureAnswer{Kind: e.LastFailureKind, At: e.LastFailure.Format(time.RFC3339)}
		}
	}
	for _, l := range c.Config {
		if !plainKey.MatchString(l.Key) {
			out.KeysHidden++
			continue
		}
		out.Config = append(out.Config, configLine(l))
	}
	return out
}

// configLine is one config key as module.status shows it: a value that may
// be a secret or is long stands as "<set>"; a default is noted, not shown,
// for it is the manifest's text.
func configLine(l modstore.ConfigLine) configAnswer {
	a := configAnswer{Key: l.Key, Type: l.Type, Required: l.Required, Set: l.Set, HasDefault: l.Default != ""}
	if !l.Set {
		return a
	}
	a.Value = l.Value
	if secretKey.MatchString(l.Key) || utf8.RuneCountInString(l.Value) > maxShownValue {
		a.Value = hiddenValue
	}
	return a
}

// activeEntry is the journal's entry of the active version.
func activeEntry(c modstore.Card) (modstore.Entry, bool) {
	i := slices.IndexFunc(c.Journal, func(e modstore.Entry) bool { return e.Version == c.Version && e.State == string(modstore.StateActive) })
	if i < 0 {
		return modstore.Entry{}, false
	}
	return c.Journal[i], true
}
