package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"

	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/tool"
)

// ModuleInstall is module.install: it asks the owner once with the rights of
// every build of the request and the links between their ports (the install
// window), and, after the press, installs exactly those builds in order,
// writes each module's settings line, starts them and writes the links
// (R-RUN-9, ADR-0061, ADR-0111).
type ModuleInstall struct{ m Modules }

// Spec implements tool.Handler.
func (*ModuleInstall) Spec() tool.Spec {
	return tool.Spec{Name: ModuleInstallTool,
		Description: "Install the builds module.build made, by their digests: one to five builds, each of a different module, " +
			"and the links the request needs between their ports and the installed modules. The owner sees, in one window, what each module may do " +
			"(the hosts it reaches, the secrets it reads, the cells it creates and reads, when it runs, its tools and limits), its trial, " +
			"whether its source was written after outside content, and each link with the shape that crosses it; one press installs the builds in order " +
			"and writes the links. A build or link the node would refuse is not asked: the answer says why. " +
			"Call it once, when every module of the request is built and its trial passed. link.add links modules already installed.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"builds":{"type":"array","minItems":1,"maxItems":5,"items":{"type":"string"},"description":"the builds' digests, as module.build answered them, in the order to install"},` +
			`"build":{"type":"string","description":"one build's digest: the same as builds with one digest"},` +
			`"links":{"type":"array","maxItems":10,"items":{"type":"object","properties":{` +
			`"from":{"type":"string","description":"the producer or the caller: module.port, such as battery-reader.reading"},` +
			`"to":{"type":"string","description":"the receiver or the server: module.port, such as battery-alerts.reading"}},` +
			`"required":["from","to"],"additionalProperties":false},"description":"links between ports of these modules and installed ones, written after the installs"},` +
			`"note":{"type":"string","description":"one line on what changed, kept in each module's journal"}},` +
			`"additionalProperties":false}`),
		Class: tool.ClassSideEffect, Policy: tool.PolicyConfirm, AdminOnly: true}
}

// Question implements tool.Questioner: the install window, built from the
// plans of the builds as the node is now and the links checked with the
// builds installed. A build or link the node would refuse is refused here,
// without a question. The window is kept for the press.
func (t *ModuleInstall) Question(ctx context.Context, c tool.Call) (string, error) {
	_, rw, err := t.prepare(ctx, c)
	if err != nil {
		return "", err
	}
	q, err := whole(ModuleInstallTool, requestText(c.Speak, rw))
	if err != nil {
		return "", err
	}
	if err := t.m.recordWindow(c, rw); err != nil {
		return "", fmt.Errorf("%s: keep the question: %w", ModuleInstallTool, err)
	}
	return q, nil
}

// Run implements tool.Handler: only pressed, and only when the request now
// shows the window the owner pressed on, it installs each build in order (it
// checks that the build loads and its health passes, installs it, writes its
// settings line with the manifest's defaults and reloads the module,
// R-RUN-9), then writes each link and tells the module host. It stops at the
// first item that fails; the answer says what was done, what failed and what
// was not tried.
func (t *ModuleInstall) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	if !c.Approved {
		return tool.Result{}, fmt.Errorf("%s: an install needs the owner's press", ModuleInstallTool)
	}
	req, rw, err := t.prepare(ctx, c)
	if err != nil {
		return tool.Result{}, err
	}
	if err := t.m.sameWindow(c, rw); err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ModuleInstallTool, err)
	}
	var o outcome
	for i, b := range req.builds {
		line, err := t.install(ctx, c, req, b)
		if err != nil {
			if len(req.builds) == 1 && len(req.links) == 0 {
				return tool.Result{}, err
			}
			o.failed, o.rest = err.Error(), t.untried(req, i+1, 0)
			return tool.Result{}, o.failure()
		}
		o.done = append(o.done, line)
	}
	if err := t.link(ctx, req, &o); err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: strings.Join(o.done, "\n")}, nil
}

// untried names the builds from index b and the links from index l that a
// press that stopped did not reach.
func (t *ModuleInstall) untried(req installRequest, b, l int) []string {
	var out []string
	for _, x := range req.builds[min(b, len(req.builds)):] {
		out = append(out, "install "+x.build.Module)
	}
	for _, x := range req.links[min(l, len(req.links)):] {
		out = append(out, "link "+x.String())
	}
	return out
}

// link writes the links of the request, each checked as the link tools check
// it, with the modules as they are now, then tells the module host once.
func (t *ModuleInstall) link(ctx context.Context, req installRequest, o *outcome) error {
	if len(req.links) == 0 {
		return nil
	}
	wrote := false
	defer func() {
		if wrote {
			t.m.Links.Changed(ctx)
		}
	}()
	for i, l := range req.links {
		if err := t.m.Links.write(ctx, l); err != nil {
			o.failed, o.rest = "link "+l.String()+": "+err.Error(), t.untried(req, len(req.builds), i+1)
			return o.failure()
		}
		wrote = true
		o.done = append(o.done, "the link "+l.String()+" is written; no module was rebuilt")
	}
	return nil
}

// install installs one build of the request and says what came of it: the
// build is checked to load and pass its health trigger, installed through
// the store, its settings line written and the module reloaded.
func (t *ModuleInstall) install(ctx context.Context, c tool.Call, req installRequest, ib installBuild) (string, error) {
	b := ib.build
	if err := t.check(ctx, b); err != nil {
		return "", err
	}
	b.By = c.Principal
	if req.note != "" {
		b.Note = req.note
	}
	o, err := t.m.Store.InstallLocal(ctx, b, ib.sha)
	if err != nil {
		return "", fmt.Errorf("%s: %w", ModuleInstallTool, err)
	}
	switch o.Result {
	case modstore.ResultInstalled:
	case modstore.ResultUpToDate:
		return fmt.Sprintf("%s: this build (version %s) is already the active one; nothing changed", o.Module, o.To), nil
	case modstore.ResultRefused, modstore.ResultFailedSelfTest, modstore.ResultRolledBack, modstore.ResultNothingToRollBack:
		return "", fmt.Errorf("%s: %s stays as it was: %s", ModuleInstallTool, o.Module, o.Reason)
	}
	return t.start(ctx, b, o), nil
}

// errBuildRunning refuses an install while a build of the module runs or
// waits: its trial report is not written yet.
var errBuildRunning = errors.New("a build of this module is running; wait for its result")

// check loads the build in a throwaway instance with the module's settings
// and runs its health trigger: a build that fails is refused and nothing
// changes (R-RUN-9).
func (t *ModuleInstall) check(ctx context.Context, b modstore.Build) error {
	line, err := t.m.line(b.Module)
	if err != nil {
		return fmt.Errorf("%s: %w", ModuleInstallTool, err)
	}
	if err := t.m.Host.Check(ctx, b, line.Config); err != nil {
		return fmt.Errorf("%s: %s is not installed and nothing changed: %w", ModuleInstallTool, b.Module, err)
	}
	return nil
}

// start writes the module's settings line, keeping the owner's values and
// adding the manifest's defaults, reloads it, and says what came of it. A
// version that does not start is rolled back to the one it replaced.
func (t *ModuleInstall) start(ctx context.Context, b modstore.Build, o modstore.Outcome) string {
	done := fmt.Sprintf("%s: version %s installed", o.Module, o.To)
	if o.From != "" {
		done = fmt.Sprintf("%s: version %s installed in place of %s", o.Module, o.To, o.From)
	}
	line, werr := t.writeLine(b)
	switch {
	case t.m.Host.Reload(ctx, b.Module) != nil:
		return done + t.rollBack(ctx, o)
	case werr != nil:
		return done + ", but its settings line could not be written, so it runs with its defaults; /status says how it is"
	case line.Off:
		return done + "; it stays off, as its settings say, until the owner turns it on"
	case line.OnEdge() != "":
		return done + " and sent to the edge " + line.OnEdge() + ", which runs it once it passes its health check there; " +
			"until then the version before it runs; /modules " + modstore.ShortName(o.Module) + " shows which"
	}
	return done + " and running"
}

// rollBack returns a module whose new version did not start to the version
// it replaced, and says what came of it; a first install has none.
func (t *ModuleInstall) rollBack(ctx context.Context, o modstore.Outcome) string {
	if o.From == "" {
		return ", but it did not start; /status says why"
	}
	r, err := t.m.Host.Rollback(ctx, o.Module)
	switch {
	case err == nil && r.Result == modstore.ResultRolledBack:
		return fmt.Sprintf(", but it did not start, so the node rolled back to version %s, which runs; the node's log says why", r.To)
	case r.Result == modstore.ResultRolledBack:
		return fmt.Sprintf(", but it did not start; the node rolled back to version %s, which did not start either; /status says why", r.To)
	}
	return ", but it did not start, and rolling back to the version before failed; /status says why"
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
	case typeNumber:
		if n, err := strconv.ParseFloat(k.Default, 64); err == nil {
			return n
		}
	case typeBoolean:
		if v, err := strconv.ParseBool(k.Default); err == nil {
			return v
		}
	}
	return k.Default
}
