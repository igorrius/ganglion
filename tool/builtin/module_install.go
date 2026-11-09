package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"

	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/tool"
)

// ModuleInstall is module.install: it asks the owner with the build's rights
// (the install window) and, after the press, installs exactly that build,
// writes its settings line and starts it (R-RUN-9, ADR-0061).
type ModuleInstall struct{ m Modules }

type installArgs struct {
	Build string `json:"build"`
	Note  string `json:"note"`
}

// Spec implements tool.Handler.
func (*ModuleInstall) Spec() tool.Spec {
	return tool.Spec{Name: ModuleInstallTool,
		Description: "Install a build module.build made, by its digest. The owner sees what the module may do (the hosts it reaches, " +
			"the secrets it reads, the cells it creates and reads, when it runs, its tools and limits) and installs it with a press; " +
			"then it runs in the node. A build the node would refuse is not asked. The press installs exactly that build.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"build":{"type":"string","description":"the build's digest, as module.build answered it"},` +
			`"note":{"type":"string","description":"one line on what changed, kept in the module's journal"}},` +
			`"required":["build"],"additionalProperties":false}`),
		Class: tool.ClassSideEffect, Policy: tool.PolicyConfirm, AdminOnly: true}
}

// Question implements tool.Questioner: the install window, built from the
// plan of the build as the node is now. A build the node would refuse is
// refused here, without a question. The window is kept for the press.
func (t *ModuleInstall) Question(ctx context.Context, c tool.Call) (string, error) {
	_, _, w, err := t.prepare(ctx, c)
	if err != nil {
		return "", err
	}
	q, err := whole(ModuleInstallTool, windowText(c.Speak, w))
	if err != nil {
		return "", err
	}
	if err := t.m.recordWindow(c, w); err != nil {
		return "", fmt.Errorf("%s: keep the question: %w", ModuleInstallTool, err)
	}
	return q, nil
}

// Run implements tool.Handler: only pressed, and only when the plan now shows
// the window the owner pressed on, it installs the build, writes its settings
// line with the manifest's defaults and reloads the module.
func (t *ModuleInstall) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	if !c.Approved {
		return tool.Result{}, fmt.Errorf("%s: an install needs the owner's press", ModuleInstallTool)
	}
	a, b, w, err := t.prepare(ctx, c)
	if err != nil {
		return tool.Result{}, err
	}
	if err := t.m.sameWindow(c, w); err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ModuleInstallTool, err)
	}
	b.By = c.Principal
	if note := oneLine(a.Note); note != "" {
		b.Note = note
	}
	o, err := t.m.Store.InstallLocal(ctx, b, a.Build)
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ModuleInstallTool, err)
	}
	switch o.Result {
	case modstore.ResultInstalled:
	case modstore.ResultUpToDate:
		return tool.Result{Text: fmt.Sprintf("%s: this build (version %s) is already the active one; nothing changed", o.Module, o.To)}, nil
	case modstore.ResultRefused, modstore.ResultFailedSelfTest, modstore.ResultRolledBack, modstore.ResultNothingToRollBack:
		return tool.Result{}, fmt.Errorf("%s: %s stays as it was: %s", ModuleInstallTool, o.Module, o.Reason)
	}
	return tool.Result{Text: t.start(ctx, b, o)}, nil
}

// prepare reads the call, loads its build and plans it: a refusal is the
// node's words, never the source's detail.
func (t *ModuleInstall) prepare(ctx context.Context, c tool.Call) (installArgs, modstore.Build, window, error) {
	var a installArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return installArgs{}, modstore.Build{}, window{}, fmt.Errorf("%s: arguments: %w", ModuleInstallTool, err)
	}
	b, err := t.m.loadBuild(a.Build)
	if err != nil {
		return installArgs{}, modstore.Build{}, window{}, fmt.Errorf("%s: %w", ModuleInstallTool, err)
	}
	p, err := t.m.plan(ctx, b)
	if err != nil {
		return installArgs{}, modstore.Build{}, window{}, fmt.Errorf("%s: %w", ModuleInstallTool, err)
	}
	if p.Refusal != "" {
		return installArgs{}, modstore.Build{}, window{}, fmt.Errorf("%s: the node refuses this build: %s (module.build shows the detail)", ModuleInstallTool, p.Refusal)
	}
	m, err := manifestOf(b)
	if err != nil {
		return installArgs{}, modstore.Build{}, window{}, fmt.Errorf("%s: %w", ModuleInstallTool, err)
	}
	return a, b, windowOf(b, p, m), nil
}

// start writes the module's settings line, keeping the owner's values and
// adding the manifest's defaults, reloads it, and says what came of it.
func (t *ModuleInstall) start(ctx context.Context, b modstore.Build, o modstore.Outcome) string {
	done := fmt.Sprintf("%s: version %s installed", o.Module, o.To)
	if o.From != "" {
		done = fmt.Sprintf("%s: version %s installed in place of %s", o.Module, o.To, o.From)
	}
	line, werr := t.writeLine(b)
	switch {
	case t.m.Host.Reload(ctx, b.Module) != nil:
		return done + ", but it did not start; /status says why"
	case werr != nil:
		return done + ", but its settings line could not be written, so it runs with its defaults; /status says how it is"
	case line.Off:
		return done + "; it stays off, as its settings say, until the owner turns it on"
	}
	return done + " and running"
}

// writeLine writes b's settings line: the owner's values kept, the
// manifest's defaults added for the keys it lacks, off as it was.
func (t *ModuleInstall) writeLine(b modstore.Build) (settings.Module, error) {
	line, err := t.m.line(b.Module)
	if err != nil {
		return settings.Module{}, err
	}
	m, err := manifestOf(b)
	if err != nil {
		return settings.Module{}, err
	}
	line.Config = withDefaults(line.Config, m)
	return line, t.m.Settings.SetModule(line)
}

// withDefaults is cfg with the manifest's default for every key it lacks.
func withDefaults(cfg map[string]any, m runner.Manifest) map[string]any {
	out := maps.Clone(cfg)
	for key, k := range m.Config {
		if _, set := out[key]; set || k.Default == "" {
			continue
		}
		if out == nil {
			out = map[string]any{}
		}
		out[key] = typedDefault(k)
	}
	return out
}

// typedDefault is a key's default as a value of its type, so the settings
// line reads as the owner would write it.
func typedDefault(k runner.ConfigKey) any {
	switch k.Type {
	case "number":
		if n, err := strconv.ParseFloat(k.Default, 64); err == nil {
			return n
		}
	case "boolean":
		if v, err := strconv.ParseBool(k.Default); err == nil {
			return v
		}
	}
	return k.Default
}
