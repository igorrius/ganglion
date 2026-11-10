package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/igorrius/ganglion/builder"
	"github.com/igorrius/ganglion/internal/redact"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/tool"
)

// ModuleBuild is module.build: it builds modules/<name> from the workspace in
// the sandbox, keeps the build as a file and answers its rights. It installs
// nothing, so it needs no press (ruling 6 of M4b, ADR-0061).
type ModuleBuild struct{ m Modules }

type buildArgs struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

// Spec implements tool.Handler.
func (*ModuleBuild) Spec() tool.Spec {
	return tool.Spec{Name: ModuleBuildTool,
		Description: "Build the module in the workspace's modules/<name> in the sandbox. It installs nothing: it answers the build's digest, " +
			"language, size, version and the rights its manifest asks for, what they add to the installed version's, and why the node " +
			"would refuse it. module.install with that digest asks the owner. A failed build answers the end of its log.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"name":{"type":"string","description":"the module's folder under modules/ in the workspace"},` +
			`"note":{"type":"string","description":"one line on what changed, kept in the module's journal"}},` +
			`"required":["name"],"additionalProperties":false}`),
		Class: tool.ClassRead, Policy: tool.PolicyAuto, AdminOnly: true}
}

// rightsAnswer is a plan's rights as module.build answers them.
type rightsAnswer struct {
	Hosts        []string `json:"hosts,omitempty"`
	Secrets      []string `json:"secrets,omitempty"`
	CellsCreated []string `json:"cells_created,omitempty"`
	CellsTaken   []string `json:"cells_taken,omitempty"`
	CellsRead    []string `json:"cells_read,omitempty"`
	Reacts       []string `json:"reacts_to,omitempty"`
	NotifyPerDay int      `json:"notify_per_day,omitempty"`
	Crons        []string `json:"crons,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	Memory       string   `json:"memory,omitempty"`
	Timeout      string   `json:"timeout,omitempty"`
}

// buildAnswer is module.build's answer.
type buildAnswer struct {
	Build    string        `json:"build"`
	Module   string        `json:"module"`
	Language string        `json:"language"`
	Size     int           `json:"size"`
	Version  int           `json:"version"`
	Rights   *rightsAnswer `json:"rights,omitempty"`
	Widening *rightsAnswer `json:"widening,omitempty"`
	Refusal  string        `json:"refusal,omitempty"`
	// Detail is the refusal's text taken from the source: outside content.
	Detail string `json:"refusal_detail,omitempty"`
	Next   string `json:"next"`
}

// failedAnswer is the answer of a build that failed: its error and log are
// the build tool's text and the source's, outside content.
type failedAnswer struct {
	Error   string `json:"error"`
	Detail  string `json:"detail"`
	LogTail string `json:"log_tail"`
}

// Run implements tool.Handler.
func (t *ModuleBuild) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	var a buildArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return tool.Result{}, fmt.Errorf("%s: arguments: %w", ModuleBuildTool, err)
	}
	if err := moduleName(ModuleBuildTool, a.Name); err != nil {
		return tool.Result{}, err
	}
	dir, err := t.m.sourceDir(a.Name)
	if err != nil {
		return tool.Result{}, err
	}
	art, log, err := t.m.Builder.Build(ctx, builder.Source{Name: a.Name, Dir: dir})
	if errors.Is(err, builder.ErrNoBuilder) {
		return tool.Result{}, fmt.Errorf("%s: %w", ModuleBuildTool, err)
	}
	if err != nil {
		return failed(err, log)
	}
	if art.SHA256 != modstore.Digest(art.Wasm, art.Manifest) {
		return tool.Result{}, fmt.Errorf("%s: the builder named the build wrongly", ModuleBuildTool)
	}
	b := modstore.Build{Module: modstore.LocalPrefix + a.Name, Wasm: art.Wasm, Manifest: art.Manifest, Language: string(art.Language),
		Note: oneLine(a.Note), Commit: t.commit(ctx, a.Name)}
	if err := t.m.saveBuild(art, buildRecord{Module: b.Module, Language: b.Language, Commit: b.Commit, Note: b.Note}); err != nil {
		return tool.Result{}, fmt.Errorf("%s: keep the build: %w", ModuleBuildTool, err)
	}
	p, err := t.m.plan(ctx, b)
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ModuleBuildTool, err)
	}
	return answerPlan(b, p)
}

// failed answers a build that failed, as outside content.
func failed(err error, log builder.Log) (tool.Result, error) {
	out, jerr := json.Marshal(failedAnswer{Error: "the build failed", Detail: redact.String(err.Error()), LogTail: redact.String(string(log))})
	if jerr != nil {
		return tool.Result{}, jerr
	}
	return tool.Result{Text: string(out), External: true}, nil
}

// commit is the workspace commit the source was built from, marked when the
// folder differs from it; "" in a workspace with no commit.
func (t *ModuleBuild) commit(ctx context.Context, name string) string {
	head, err := t.m.Workspace.Head(ctx)
	if err != nil {
		return ""
	}
	if dirty, err := t.m.Workspace.Dirty(ctx, modulesFolder+"/"+name); err != nil || dirty {
		return head + "-dirty"
	}
	return head
}

// answerPlan is the plan as the model reads it. It is outside content: the
// names of the rights are the manifest's words, as is the refusal's detail
// (R-RUN-13).
func answerPlan(b modstore.Build, p modstore.Plan) (tool.Result, error) {
	ans := buildAnswer{Build: p.SHA256, Module: b.Module, Language: b.Language, Size: p.Size, Version: p.Version,
		Refusal: p.Refusal, Detail: p.RefusalDetail}
	if p.Refusal != "" {
		ans.Next = "the node would refuse to install this build; change the source and build again"
	} else {
		ans.Rights, ans.Widening = rightsOf(p.Rights), rightsOf(p.Widening)
		ans.Next = "module.install with this build shows the owner these rights and installs it after their press"
	}
	out, err := json.Marshal(ans)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: string(out), External: true}, nil
}

// rightsOf is r as answered; nil when r is empty.
func rightsOf(r modstore.Rights) *rightsAnswer {
	a := rightsAnswer{Hosts: r.Hosts, Secrets: r.Secrets, CellsCreated: r.CellsCreated, CellsTaken: r.CellsTaken,
		CellsRead: r.CellsRead, Reacts: r.Reacts, NotifyPerDay: r.NotifyPerDay, Crons: r.Crons, Tools: r.Tools}
	if r.Memory > 0 {
		a.Memory = memoryShown(r.Memory)
	}
	if r.Timeout > 0 {
		a.Timeout = timeoutShown(r.Timeout)
	}
	if a.Memory == "" && a.Timeout == "" && a.NotifyPerDay == 0 && len(slices.Concat(a.Hosts, a.Secrets, a.CellsCreated, a.CellsTaken, a.CellsRead, a.Reacts, a.Crons, a.Tools)) == 0 {
		return nil
	}
	return &a
}
