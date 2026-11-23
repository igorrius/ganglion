package agent

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/texts"
)

// Modules lists, updates and rolls back the node's built-in modules
// (R-RUN-8).
type Modules interface {
	List(ctx context.Context) ([]modstore.Version, error)
	Update(ctx context.Context, name string) (modstore.Outcome, error)
	Rollback(ctx context.Context, name string) (modstore.Outcome, error)
}

// NoModules is a node without a module store.
type NoModules struct{}

// List lists nothing.
func (NoModules) List(context.Context) ([]modstore.Version, error) { return nil, nil }

// Update has no module to update.
func (NoModules) Update(context.Context, string) (modstore.Outcome, error) {
	return modstore.Outcome{}, modstore.ErrUnknownModule
}

// Rollback has no module to roll back.
func (NoModules) Rollback(context.Context, string) (modstore.Outcome, error) {
	return modstore.Outcome{}, modstore.ErrUnknownModule
}

// LocalModules is what /modules does with the owner's modules (modhost.Host,
// R-RUN-10): every method takes the name with or without "local/", and
// answers modstore.ErrUnknownModule for one it does not have.
type LocalModules interface {
	Local(ctx context.Context) ([]modstore.LocalModule, error)
	Card(ctx context.Context, name string) (modstore.Card, error)
	// Configure refuses with a *modstore.ConfigError.
	Configure(ctx context.Context, name, key, value string) error
	// SetOff and Configure answer modstore.ErrNotStarted when the module is
	// on and did not start.
	SetOff(ctx context.Context, name string, off bool) error
	Rollback(ctx context.Context, name string) (modstore.Outcome, error)
	Remove(ctx context.Context, name string) error
	// Links are the links of the node's file that name the module, as
	// `a.x → b.y`: removing it takes them too.
	Links(ctx context.Context, name string) ([]string, error)
	// Run is the journal's row of a module's run id; false when it has none
	// (R-RUN-20).
	Run(ctx context.Context, name, id string) (modstore.Run, bool, error)
}

// NoLocalModules is a node that hosts no local modules.
type NoLocalModules struct{}

// Local lists nothing.
func (NoLocalModules) Local(context.Context) ([]modstore.LocalModule, error) { return nil, nil }

// Card has no module.
func (NoLocalModules) Card(context.Context, string) (modstore.Card, error) {
	return modstore.Card{}, modstore.ErrUnknownModule
}

// Configure has no module.
func (NoLocalModules) Configure(context.Context, string, string, string) error {
	return modstore.ErrUnknownModule
}

// SetOff has no module.
func (NoLocalModules) SetOff(context.Context, string, bool) error { return modstore.ErrUnknownModule }

// Rollback has no module.
func (NoLocalModules) Rollback(context.Context, string) (modstore.Outcome, error) {
	return modstore.Outcome{}, modstore.ErrUnknownModule
}

// Remove has no module.
func (NoLocalModules) Remove(context.Context, string) error { return modstore.ErrUnknownModule }

// Links has no module.
func (NoLocalModules) Links(context.Context, string) ([]string, error) { return nil, nil }

// Run has no journal.
func (NoLocalModules) Run(context.Context, string, string) (modstore.Run, bool, error) {
	return modstore.Run{}, false, nil
}

// Words of /modules and the names of the values its texts take.
const (
	wordOff    = "off"
	wordOn     = "on"
	varName    = "name"
	varVersion = "version"
	varValue   = "value"
	varCells   = "cells"
	varKind    = "kind"
	wordRuns   = "runs"
	wordUpdate = "update"
	wordConfig = "config"
)

const modulesNote = commandPrefix + "the owner ran /modules: "

// moduleReply is what /modules answers and what the history notes. The note
// never holds text taken from a module's source (a setting's name, a note
// of the journal): the model does not read it (R-RUN-13).
type moduleReply struct {
	text, note string
	buttons    []event.Button // attached to the reply: the card's Runs button
}

// dateLayout is the day and month, the same in every language.
const dateLayout = "02.01"

// modules is /modules: the list, a module's card, and its update, rollback,
// config, off, on and remove, for an admin, without a model. The reply and
// a [command] note in the history say what happened.
func (a *Agent) modules(ctx context.Context, t *turn, arg string) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		t.reply(a.say(ctx, t, texts.ReplyNotAvailable, nil))
		return nil
	}
	// A message of several lines, each a /modules command, runs them in order
	// with one answer each; a line that is no command makes the whole message
	// a usage error, so no line's text is taken for a name.
	lines := strings.Split(strings.TrimSpace(arg), "\n")
	for i := 1; i < len(lines); i++ {
		rest, ok := strings.CutPrefix(strings.TrimSpace(lines[i]), "/modules")
		if !ok || (rest != "" && rest[0] != ' ' && rest[0] != '\t') {
			lines = []string{wordUpdate} // a verb with no name is the usage line
			break
		}
		lines[i] = rest
	}
	for _, line := range lines {
		r, err := a.modulesReply(ctx, t, strings.TrimSpace(line))
		if err != nil {
			return fmt.Errorf("agent: /modules: %w", err)
		}
		t.reply(r.text)
		if len(r.buttons) > 0 {
			t.out[len(t.out)-1].Buttons = r.buttons
		}
		t.note(modulesNote + r.note)
	}
	return nil
}

// modulesReply runs the command in arg.
func (a *Agent) modulesReply(ctx context.Context, t *turn, arg string) (moduleReply, error) {
	verb, name, _ := strings.Cut(arg, " ")
	name = strings.TrimSpace(name)
	// A name is one word; config alone takes more (key=value). Text after it
	// is refused unechoed, not taken for part of the name.
	if verb != wordConfig && strings.ContainsAny(name, " \t\r\n") {
		return plain(a.say(ctx, t, texts.ModulesUsage, nil)), nil
	}
	switch {
	case verb == "":
		return a.listModules(ctx, t)
	case verb == wordUpdate && name != "":
		return a.outcomeReply(ctx, t, name, func() (modstore.Outcome, error) { return a.d.Modules.Update(ctx, name) })
	case verb == "rollback" && name != "":
		return a.rollback(ctx, t, name)
	case (verb == wordOff || verb == wordOn) && name != "":
		return a.switchModule(ctx, t, name, verb == wordOff)
	case verb == "remove" && name != "":
		return a.removeModule(ctx, t, name)
	case verb == wordConfig && name != "":
		return a.configModule(ctx, t, name)
	case verb == wordRuns && name != "":
		return a.runsReply(ctx, t, name)
	case name == "" && !isModulesVerb(verb):
		return a.moduleCard(ctx, t, verb)
	}
	return plain(a.say(ctx, t, texts.ModulesUsage, nil)), nil
}

func isModulesVerb(w string) bool {
	switch w {
	case wordUpdate, "rollback", wordOff, wordOn, "remove", wordConfig, wordRuns:
		return true
	}
	return false
}

// plain is a reply whose note is its text.
func plain(text string) moduleReply { return moduleReply{text: text, note: text} }

// unknown is the reply to a name no module has.
func (a *Agent) unknown(ctx context.Context, t *turn, name string) moduleReply {
	return plain(a.say(ctx, t, texts.ModulesUnknown, texts.Vars{varName: texts.Escape(name)}))
}

// listModules lists the built-in modules' versions and the local modules'
// states.
func (a *Agent) listModules(ctx context.Context, t *turn) (moduleReply, error) {
	vs, err := a.d.Modules.List(ctx)
	if err != nil {
		return moduleReply{}, err
	}
	local, err := a.d.Local.Local(ctx)
	if err != nil {
		return moduleReply{}, err
	}
	lines := a.builtinLines(ctx, t, vs)
	for _, m := range local {
		lines = append(lines, "- "+a.say(ctx, t, texts.ModulesLocalRow, texts.Vars{varName: texts.Escape(m.Name),
			"state": a.stateText(ctx, t, m), varVersion: texts.Escape(m.Version)}))
	}
	if len(lines) == 0 {
		return plain(a.say(ctx, t, texts.ModulesNone, nil)), nil
	}
	return plain(a.say(ctx, t, texts.ModulesHeader, nil) + "\n" + strings.Join(lines, "\n")), nil
}

// stateText is how a local module stands, in words: on its edge, what the
// edge says it runs (R-EDGE-6); the edge's words are escaped.
func (a *Agent) stateText(ctx context.Context, t *turn, m modstore.LocalModule) string {
	if m.State != modstore.RunEdge || m.Edge == nil {
		return a.say(ctx, t, stateKey(m.State), nil)
	}
	e := m.Edge
	running := a.say(ctx, t, texts.ModulesEdgeNothing, nil)
	if e.Version != "" {
		running = a.say(ctx, t, texts.ModulesEdgeRunning, texts.Vars{varVersion: texts.Escape(e.Version)})
	}
	var how string
	switch e.Result {
	case modstore.EdgeRunning:
		how = running
	case modstore.EdgeRefused:
		how = a.say(ctx, t, texts.ModulesEdgeRefused, texts.Vars{varVersion: texts.Escape(m.Version), "reason": texts.Escape(e.Reason), "running": running})
	case modstore.EdgeUndelivered:
		how = a.say(ctx, t, texts.ModulesEdgeUndelivered, nil)
	default:
		how = a.say(ctx, t, texts.ModulesEdgeWaiting, texts.Vars{varVersion: texts.Escape(m.Version)})
	}
	return a.say(ctx, t, texts.ModulesStateEdge, texts.Vars{"node": texts.Escape(e.Node), "how": how})
}

// stateKey is the text of a local module's state.
func stateKey(s modstore.RunState) texts.Key {
	switch s {
	case modstore.RunRunning:
		return texts.ModulesStateRunning
	case modstore.RunOff:
		return texts.ModulesStateOff
	case modstore.RunCrashloop:
		return texts.ModulesStateCrashloop
	case modstore.RunStopped, modstore.RunEdge: // an edge module's words are stateText's
	}
	return texts.ModulesStateStopped
}

// builtinLines is a line for each built-in module: its versions.
func (a *Agent) builtinLines(ctx context.Context, t *turn, vs []modstore.Version) []string {
	var (
		order []string
		lines = map[string][]string{}
	)
	for _, v := range vs {
		if v.Source == modstore.SourceLocal {
			continue
		}
		if _, seen := lines[v.Module]; !seen {
			order = append(order, v.Module)
		}
		var s string
		switch v.State {
		case modstore.StateActive:
			s = texts.Escape(v.Version)
			if v.Source != modstore.SourceBuiltin {
				s += " (" + texts.Escape(v.Source) + ")"
			}
		case modstore.StatePrevious:
			s = a.say(ctx, t, texts.ModulesPrevious, texts.Vars{varVersion: texts.Escape(v.Version)})
		case modstore.StateFailed:
			s = a.say(ctx, t, texts.ModulesFailed, texts.Vars{varVersion: texts.Escape(v.Version), "reason": texts.Escape(v.Reason)})
		case modstore.StateRetired:
			continue
		}
		lines[v.Module] = append(lines[v.Module], s)
	}
	out := make([]string, 0, len(order))
	for _, m := range order {
		out = append(out, "- "+texts.Escape(m)+": "+strings.Join(lines[m], "; "))
	}
	return out
}

// moduleCard is /modules <name>: how the module stands, every version it
// has had and its settings. The journal's notes and the manifest's words
// are shown to the owner here and never noted for the model.
func (a *Agent) moduleCard(ctx context.Context, t *turn, name string) (moduleReply, error) {
	c, err := a.d.Local.Card(ctx, name)
	if errors.Is(err, modstore.ErrUnknownModule) {
		return a.unknown(ctx, t, name), nil
	}
	if err != nil {
		return moduleReply{}, err
	}
	loc := a.cfg.Location
	since := ""
	if n := len(c.Journal); n > 0 {
		since = c.Journal[n-1].Installed.In(loc).Format(dateLayout)
	}
	vars := texts.Vars{varName: texts.Escape(c.Name), "since": since}
	var b strings.Builder
	if c.State == modstore.RunRunning {
		b.WriteString(a.say(ctx, t, texts.ModulesCardRunning, vars))
	} else {
		vars["state"] = a.stateText(ctx, t, c.LocalModule)
		b.WriteString(a.say(ctx, t, texts.ModulesCardStopped, vars))
	}
	if line := a.healthLine(ctx, t, c.Health); line != "" {
		b.WriteString("\n" + line)
	}
	for _, line := range a.reflexLines(ctx, t, c) {
		b.WriteString("\n" + line)
	}
	rows := make([]string, len(c.Journal))
	for i, e := range c.Journal {
		rows[i] = a.journalRow(ctx, t, e)
	}
	if len(rows) > 0 {
		b.WriteString("\n" + a.say(ctx, t, texts.ModulesCardVersions, texts.Vars{"lines": strings.Join(rows, "\n")}))
	}
	if len(c.Config) > 0 {
		b.WriteString("\n" + a.say(ctx, t, texts.ModulesCardSettings, texts.Vars{"lines": a.settingRows(ctx, t, c.Config)}))
	}
	buttons, err := a.runsButtons(ctx, a.speaker(ctx, t), t, c)
	if err != nil {
		return moduleReply{}, err
	}
	return moduleReply{text: b.String(), note: "the owner opened the card of " + c.Name, buttons: buttons}, nil
}

// reflexLines are the card's lines of what the module reacts to, which
// modules it feeds and its messages today; none for a module with none of
// them. A count over the limit shows the limit: the refused ones were not
// sent.
func (a *Agent) reflexLines(ctx context.Context, t *turn, c modstore.Card) []string {
	var lines []string
	if len(c.ReactsTo) > 0 {
		lines = append(lines, a.say(ctx, t, texts.ModulesCardReacts, texts.Vars{varCells: escaped(c.ReactsTo)}))
	}
	if len(c.Feeds) > 0 {
		lines = append(lines, a.say(ctx, t, texts.ModulesCardFeeds, texts.Vars{"modules": escaped(c.Feeds)}))
	}
	if c.NotifyPerDay > 0 {
		lines = append(lines, a.say(ctx, t, texts.ModulesCardNotify,
			texts.Vars{"n": strconv.Itoa(min(c.NotifiedToday, c.NotifyPerDay)), "limit": strconv.Itoa(c.NotifyPerDay)}))
	}
	return lines
}

// escaped is names escaped and joined by commas.
func escaped(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = texts.Escape(n)
	}
	return strings.Join(out, ", ")
}

// healthLine is the running module's health check on the card (ruling 3
// of M4b): ok, or the kind it failed with in the node's words; none when the
// module does not run.
func (a *Agent) healthLine(ctx context.Context, t *turn, h modstore.Health) string {
	switch h {
	case "":
		return ""
	case modstore.HealthOK:
		return a.say(ctx, t, texts.ModulesCardHealthOK, nil)
	}
	return a.say(ctx, t, texts.ModulesCardHealthFailed, texts.Vars{varKind: texts.Escape(string(h))})
}

// journalRow is one version: v3  14.11  "the note"  1,340 runs, 0 failures  ← active.
func (a *Agent) journalRow(ctx context.Context, t *turn, e modstore.Entry) string {
	note := ""
	if e.Note != "" {
		note = `"` + texts.Escape(e.Note) + `"  `
	}
	mark := texts.ModulesMarkRetired
	switch e.State {
	case string(modstore.StateActive):
		mark = texts.ModulesMarkActive
	case string(modstore.StatePrevious):
		mark = texts.ModulesMarkPrevious
	case string(modstore.StateFailed):
		mark = texts.ModulesMarkFailed
	}
	return a.say(ctx, t, texts.ModulesJournalRow, texts.Vars{varVersion: texts.Escape(e.Version),
		"date": e.Installed.In(a.cfg.Location).Format(dateLayout), "note": note, "runs": grouped(e.Runs),
		"failures": grouped(e.Failures), "mark": a.say(ctx, t, mark, nil)})
}

// grouped is n with a comma between thousands: 1,340.
func grouped(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0 && s[i-1] != '-'; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// settingRows is a line for each setting the manifest declares.
func (a *Agent) settingRows(ctx context.Context, t *turn, lines []modstore.ConfigLine) string {
	rows := make([]string, len(lines))
	for i, l := range lines {
		value := a.say(ctx, t, texts.ModulesSettingUnset, nil)
		switch {
		case l.Set:
			value = texts.Escape(l.Value)
		case l.Default != "":
			value = a.say(ctx, t, texts.ModulesSettingDefault, texts.Vars{varValue: texts.Escape(l.Default)})
		}
		required := ""
		if l.Required {
			required = a.say(ctx, t, texts.ModulesSettingRequired, nil)
		}
		rows[i] = a.say(ctx, t, texts.ModulesSettingRow, texts.Vars{"key": texts.Escape(l.Key), varValue: value,
			"type": texts.Escape(l.Type), "required": required})
	}
	return strings.Join(rows, "\n")
}

// configModule is /modules config <name> key=value: the key must be one the
// manifest declares and the value of its type. Neither the key nor the value
// reaches the history.
func (a *Agent) configModule(ctx context.Context, t *turn, rest string) (moduleReply, error) {
	name, assign, _ := strings.Cut(rest, " ")
	key, value, ok := strings.Cut(strings.TrimSpace(assign), "=")
	if key = strings.TrimSpace(key); !ok || key == "" {
		return plain(a.say(ctx, t, texts.ModulesUsage, nil)), nil
	}
	err := a.d.Local.Configure(ctx, name, key, strings.TrimSpace(value))
	short := texts.Escape(modstore.ShortName(name))
	vars := texts.Vars{varName: short, "key": texts.Escape(key)}
	var ce *modstore.ConfigError
	switch {
	case errors.Is(err, modstore.ErrUnknownModule):
		return a.unknown(ctx, t, name), nil
	case errors.As(err, &ce) && errors.Is(err, modstore.ErrUndeclaredKey):
		vars["keys"] = a.say(ctx, t, texts.ModulesConfigNoKeys, nil)
		if len(ce.Declared) > 0 {
			vars["keys"] = a.say(ctx, t, texts.ModulesConfigKeys, texts.Vars{"keys": texts.Escape(strings.Join(ce.Declared, ", "))})
		}
		return moduleReply{text: a.say(ctx, t, texts.ModulesConfigUndeclared, vars), note: "the owner tried to set a setting that " + name + " does not declare"}, nil
	case errors.As(err, &ce):
		vars["type"] = texts.Escape(ce.Type)
		return moduleReply{text: a.say(ctx, t, texts.ModulesConfigWrongType, vars), note: "the owner tried to set a setting of " + name + " to a value of the wrong type"}, nil
	case errors.Is(err, modstore.ErrNotStarted):
		return moduleReply{text: a.say(ctx, t, texts.ModulesNotStarted, vars), note: "the owner changed a setting of " + name + "; it did not start"}, nil
	case err != nil:
		return moduleReply{}, err
	}
	return moduleReply{text: a.say(ctx, t, texts.ModulesConfigDone, vars), note: "the owner changed a setting of " + name}, nil
}

// switchModule turns a local module off or on.
func (a *Agent) switchModule(ctx context.Context, t *turn, name string, off bool) (moduleReply, error) {
	err := a.d.Local.SetOff(ctx, name, off)
	vars := texts.Vars{varName: texts.Escape(modstore.ShortName(name))}
	switch {
	case errors.Is(err, modstore.ErrUnknownModule):
		return a.unknown(ctx, t, name), nil
	case errors.Is(err, modstore.ErrNotStarted):
		return plain(a.say(ctx, t, texts.ModulesNotStarted, vars)), nil
	case err != nil:
		return moduleReply{}, err
	case off:
		return plain(a.say(ctx, t, texts.ModulesOffDone, vars)), nil
	}
	return plain(a.say(ctx, t, texts.ModulesOnDone, vars)), nil
}

// removeModule removes a local module; its cells and journal stay.
func (a *Agent) removeModule(ctx context.Context, t *turn, name string) (moduleReply, error) {
	taken, err := a.d.Local.Links(ctx, name) // read first: the removal drops them
	if err != nil {
		return moduleReply{}, err
	}
	err = a.d.Local.Remove(ctx, name)
	switch {
	case errors.Is(err, modstore.ErrUnknownModule):
		return a.unknown(ctx, t, name), nil
	case err != nil:
		return moduleReply{}, err
	}
	vars := texts.Vars{varName: texts.Escape(modstore.ShortName(name))}
	if len(taken) == 0 {
		return plain(a.say(ctx, t, texts.ModulesRemoved, vars)), nil
	}
	vars["links"] = texts.Escape(strings.Join(taken, ", "))
	return plain(a.say(ctx, t, texts.ModulesRemovedLinks, vars)), nil
}

// rollback returns a local module to its previous version, or else a
// built-in one.
func (a *Agent) rollback(ctx context.Context, t *turn, name string) (moduleReply, error) {
	o, err := a.d.Local.Rollback(ctx, name)
	if errors.Is(err, modstore.ErrUnknownModule) {
		return a.outcomeReply(ctx, t, name, func() (modstore.Outcome, error) { return a.d.Modules.Rollback(ctx, name) })
	}
	if errors.Is(err, modstore.ErrNotStarted) {
		text := a.outcomeText(ctx, t, o) + " " + a.say(ctx, t, texts.ModulesNotStarted, texts.Vars{varName: texts.Escape(modstore.ShortName(o.Module))})
		return plain(text), nil
	}
	if err != nil {
		return moduleReply{}, err
	}
	return plain(a.outcomeText(ctx, t, o)), nil
}

// outcomeReply runs an update or a rollback and says what it did.
func (a *Agent) outcomeReply(ctx context.Context, t *turn, name string, run func() (modstore.Outcome, error)) (moduleReply, error) {
	o, err := run()
	if errors.Is(err, modstore.ErrUnknownModule) {
		return a.unknown(ctx, t, name), nil
	}
	if err != nil {
		return moduleReply{}, err
	}
	return plain(a.outcomeText(ctx, t, o)), nil
}

// outcomeText says what an update or a rollback did.
func (a *Agent) outcomeText(ctx context.Context, t *turn, o modstore.Outcome) string {
	vars := texts.Vars{varModule: texts.Escape(modstore.ShortName(o.Module)), "from": texts.Escape(o.From), "to": texts.Escape(o.To)}
	switch o.Result {
	case modstore.ResultInstalled:
		return a.say(ctx, t, texts.ModulesUpdated, vars)
	case modstore.ResultUpToDate:
		return a.say(ctx, t, texts.ModulesUpToDate, vars)
	case modstore.ResultRolledBack:
		return a.say(ctx, t, texts.ModulesRolledBack, vars)
	case modstore.ResultNothingToRollBack:
		return a.say(ctx, t, texts.ModulesNothingToRollBack, vars)
	case modstore.ResultRefused, modstore.ResultFailedSelfTest:
	}
	vars["reason"] = texts.Escape(strings.TrimSuffix(o.Reason, "."))
	return a.say(ctx, t, texts.ModulesStays, vars)
}
