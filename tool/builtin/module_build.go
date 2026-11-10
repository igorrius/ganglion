package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/igorrius/ganglion/builder"
	"github.com/igorrius/ganglion/internal/redact"
	"github.com/igorrius/ganglion/jobs"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/tool"
)

// ModuleBuild is module.build: it builds modules/<name> from the workspace in
// the sandbox, in the background, keeps the build as a file and tells its
// rights in a note when it ends (ADR-0069). It installs nothing, so it needs
// no press (ruling 6 of M4b, ADR-0061).
type ModuleBuild struct{ m Modules }

type buildArgs struct {
	Name string `json:"name"`
	Note string `json:"note"`
}

// Spec implements tool.Handler.
func (*ModuleBuild) Spec() tool.Spec {
	return tool.Spec{Name: ModuleBuildTool,
		Description: "Build the module in the workspace's modules/<name> in the sandbox, in the background: it answers at once, a card in " +
			"the chat shows the build's progress, and the result comes back as a note when it ends. It installs nothing: the note gives " +
			"the build's digest, language, size, version and the rights its manifest asks for, what they add to the installed version's, " +
			"and why the node would refuse it. module.install with that digest asks the owner. A failed build's note gives the end of " +
			"its log. One build runs at a time; a module being built cannot be built or written until its note comes, and after 5 " +
			"builds of a module in a row the node refuses another until the owner writes.",
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

// buildingAnswer is module.build's answer at once: the build runs in the
// background and its end comes back as a note (ADR-0069).
type buildingAnswer struct {
	Building string `json:"building"`
	Job      string `json:"job"`
	Next     string `json:"next"`
}

// buildingNext is what the model is told to expect.
const buildingNext = "the result comes back as a note when the build ends; a card in the chat shows its progress"

// Run implements tool.Handler: it checks the module, queues its build and
// answers at once with the job's key, which the session keeps until the
// build's end resumes it (R-RUN-9, ADR-0069). A module whose build runs or
// waits is refused. The answer is the node's words and the module's name:
// not outside content.
func (t *ModuleBuild) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	var a buildArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return tool.Result{}, fmt.Errorf("%s: arguments: %w", ModuleBuildTool, err)
	}
	if err := moduleName(ModuleBuildTool, a.Name); err != nil {
		return tool.Result{}, err
	}
	if _, err := t.m.sourceDir(ModuleBuildTool, a.Name); err != nil {
		return tool.Result{}, err
	}
	defer t.m.locks.lock(a.Name)() // a write under way ends before the build is queued
	key := buildKey(a.Name, c)
	rec := jobRecord{Key: key, Module: a.Name, Conv: c.Conv, Transport: c.Transport, Principal: c.Principal, Depth: c.Depth}
	if err := t.m.Queue.submit(ctx, rec, t.work(a)); err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ModuleBuildTool, err)
	}
	out, err := json.Marshal(buildingAnswer{Building: a.Name, Job: key, Next: buildingNext})
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: string(out), Job: key}, nil
}

// buildKey is the job key of a build that call c asked for: the module and
// the call, so the same call asked again is the same build.
func buildKey(name string, c tool.Call) string { return "build:" + name + ":" + keyHash(c) }

// work is the build of a's module as the queue runs it: the answer it ends
// with is the one module.build gave when it built inside the call.
func (t *ModuleBuild) work(a buildArgs) buildWork {
	return func(ctx context.Context, progress builder.ProgressFunc) buildEnd {
		end, err := t.build(ctx, a, progress)
		if err != nil {
			return errorEnd(err)
		}
		return end
	}
}

// errorAnswer is the answer of a build the node could not run or keep.
type errorAnswer struct {
	Error string `json:"error"`
}

// errorEnd is a build that ended in err, in the node's words.
func errorEnd(err error) buildEnd {
	out, jerr := json.Marshal(errorAnswer{Error: redact.String(err.Error())})
	if jerr != nil {
		panic("builtin: a struct of strings always marshals: " + jerr.Error())
	}
	return buildEnd{Answer: string(out), State: jobs.StateFailed}
}

// build builds a's module, keeps the build as a file and answers its plan.
// The answer is clean only when the folder is the node's own both before
// and after the build (R-RUN-17, ADR-0068).
func (t *ModuleBuild) build(ctx context.Context, a buildArgs, progress builder.ProgressFunc) (buildEnd, error) {
	dir, err := t.m.sourceDir(ModuleBuildTool, a.Name)
	if err != nil {
		return buildEnd{}, err
	}
	own := t.m.ownSource(a.Name)
	art, log, err := t.m.Builder.Build(ctx, builder.Source{Name: a.Name, Dir: dir, Progress: progress})
	own = own && t.m.ownSource(a.Name)
	if errors.Is(err, builder.ErrNoBuilder) {
		return buildEnd{}, fmt.Errorf("%s: %w", ModuleBuildTool, err)
	}
	if err != nil {
		return ended(jobs.StateFailed)(failed(err, log, own))
	}
	if art.SHA256 != modstore.Digest(art.Wasm, art.Manifest) {
		return buildEnd{}, fmt.Errorf("%s: the builder named the build wrongly", ModuleBuildTool)
	}
	b := modstore.Build{Module: modstore.LocalPrefix + a.Name, Wasm: art.Wasm, Manifest: art.Manifest, Language: string(art.Language),
		Note: oneLine(a.Note), Commit: t.commit(ctx, a.Name)}
	if err := t.m.saveBuild(art, buildRecord{Module: b.Module, Language: b.Language, Commit: b.Commit, Note: b.Note}); err != nil {
		return buildEnd{}, fmt.Errorf("%s: keep the build: %w", ModuleBuildTool, err)
	}
	p, err := t.m.plan(ctx, b)
	if err != nil {
		return buildEnd{}, fmt.Errorf("%s: %w", ModuleBuildTool, err)
	}
	return ended(jobs.StateDone)(answerPlan(b, p, own))
}

// ended turns an answer into a build's end as s.
func ended(s jobs.State) func(tool.Result, error) (buildEnd, error) {
	return func(res tool.Result, err error) (buildEnd, error) {
		if err != nil {
			return buildEnd{}, err
		}
		return buildEnd{Answer: res.Text, External: res.External, State: s}, nil
	}
}

// failed answers a build that failed. Of the node's own source (own) it
// keeps only rustc's diagnostics about the module's own files and is not
// outside content; otherwise the log's end is outside content (R-RUN-17).
func failed(err error, log builder.Log, own bool) (tool.Result, error) {
	tail := string(log)
	if own {
		tail = builder.OwnDiagnostics(log)
	}
	out, jerr := json.Marshal(failedAnswer{Error: "the build failed", Detail: redact.String(err.Error()), LogTail: redact.String(tail)})
	if jerr != nil {
		return tool.Result{}, jerr
	}
	return tool.Result{Text: string(out), External: !own}, nil
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

// answerPlan is the plan as the model reads it. It is outside content
// unless the source is the node's own (own): the names of the rights are the
// manifest's words, as is the refusal's detail (R-RUN-13, R-RUN-17).
func answerPlan(b modstore.Build, p modstore.Plan, own bool) (tool.Result, error) {
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
	return tool.Result{Text: string(out), External: !own}, nil
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
