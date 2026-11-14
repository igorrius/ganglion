package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/igorrius/ganglion/builder"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
)

// maxImport caps the bytes module.import copies.
const maxImport = 16 << 20

// examplesRel is how a checkout names the examples folder; a call may name
// an example this way wherever the node keeps it.
const examplesRel = "examples/modules"

// ModuleImport is module.import: it copies an example module the node ships
// into the workspace as modules/<name> and commits it, after the owner's
// press (ruling 5 of M4b). Only the examples folder is read: the sandbox
// keeps commands out of the workspace, and an arbitrary host folder is not
// the agent's to copy.
type ModuleImport struct{ m Modules }

type importArgs struct {
	From string `json:"from"`
	Name string `json:"name"`
}

// Spec implements tool.Handler.
func (t *ModuleImport) Spec() tool.Spec {
	return tool.Spec{Name: ModuleImportTool,
		Description: "Copy an example module the node ships into the workspace as modules/<name>, so module.build can build it. " +
			"from names the example: its folder name, or examples/modules/<example> (the node keeps them in " + t.m.Examples + "). " +
			"The owner approves the copy with a press; the copy is committed to the workspace's history. A name already in the workspace is refused.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"from":{"type":"string","description":"the example, such as reader-template or examples/modules/reader-template"},` +
			`"name":{"type":"string","description":"the module's name: lower-case letters, digits, - and _, starting with a letter"}},` +
			`"required":["from","name"],"additionalProperties":false}`),
		Class: tool.ClassSideEffect, Policy: tool.PolicyConfirm, AdminOnly: true}
}

// Offered implements tool.Gated: only on a node with an examples folder.
func (t *ModuleImport) Offered() bool { return t.m.Examples != "" }

// Question implements tool.Questioner: the example's folder on this machine
// and the workspace folder it becomes. A call that would be refused is
// refused before it is asked.
func (t *ModuleImport) Question(_ context.Context, c tool.Call) (string, error) {
	a, src, err := t.check(c)
	if err != nil {
		return "", err
	}
	return whole(ModuleImportTool, c.Speak.Text(texts.AskModuleImport, texts.Vars{"from": shown(src), varName: item(a.Name)}))
}

// Run implements tool.Handler: only pressed, it copies and commits.
func (t *ModuleImport) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	if !c.Approved {
		return tool.Result{}, fmt.Errorf("%s: an import needs the owner's press", ModuleImportTool)
	}
	a, src, err := t.check(c)
	if err != nil {
		return tool.Result{}, err
	}
	defer t.m.locks.lock(a.Name)()
	if err := t.free(a.Name); err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ModuleImportTool, err)
	}
	commit, err := t.copyIn(ctx, src, a.Name)
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ModuleImportTool, err)
	}
	if err := t.m.recordFolder(a.Name, originExample+filepath.Base(src)); err != nil {
		return tool.Result{}, fmt.Errorf("%s: copied %s as modules/%s (commit %s), but could not note the folder as the node's own, "+
			"so its builds stay outside content: %w", ModuleImportTool, src, a.Name, shortCommit(commit), err)
	}
	return tool.Result{Text: fmt.Sprintf("copied %s into the workspace as modules/%s (commit %s); module.build {\"name\": %q} builds it",
		src, a.Name, shortCommit(commit), a.Name)}, nil
}

// check reads the call and finds the example: the arguments, the example's
// folder, or why the call is refused, in the node's words.
func (t *ModuleImport) check(c tool.Call) (importArgs, string, error) {
	var a importArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return importArgs{}, "", fmt.Errorf("%s: arguments: %w", ModuleImportTool, err)
	}
	if err := moduleName(ModuleImportTool, a.Name); err != nil {
		return importArgs{}, "", err
	}
	src, err := t.example(a.From)
	if err != nil {
		return importArgs{}, "", fmt.Errorf("%s: %w", ModuleImportTool, err)
	}
	if err := t.free(a.Name); err != nil {
		return importArgs{}, "", fmt.Errorf("%s: %w", ModuleImportTool, err)
	}
	return a, src, nil
}

// errNotExample refuses a from that is not one of the node's examples.
var errNotExample = errors.New("from must name an example in the node's examples folder, by its folder name or as examples/modules/<example>")

// example is the folder of the example from names: <Examples>/<x>, where from
// is x, examples/modules/x or the folder's own path. Any other path is
// refused, and so is an example that is a symlink or leads out of the
// examples folder.
func (t *ModuleImport) example(from string) (string, error) {
	if t.m.Examples == "" {
		return "", errors.New("this node has no examples folder")
	}
	root, err := filepath.EvalSymlinks(t.m.Examples)
	if err != nil {
		return "", fmt.Errorf("the examples folder: %w", err)
	}
	clean := path.Clean(filepath.ToSlash(strings.TrimSpace(from)))
	dir, x := path.Split(clean)
	dir = strings.TrimSuffix(dir, "/")
	switch {
	case modstore.CheckShortName(x) != "":
		return "", errNotExample
	case dir == "", dir == examplesRel, dir == filepath.ToSlash(filepath.Clean(t.m.Examples)), dir == filepath.ToSlash(root):
	default:
		return "", errNotExample
	}
	src := filepath.Join(root, x)
	if err := realDir(src, src); errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("there is no example %s", x)
	} else if err != nil {
		return "", fmt.Errorf("the example %s: %w", x, err)
	}
	return src, nil
}

// free checks that modules/<name> is not in the workspace yet.
func (t *ModuleImport) free(name string) error {
	base, err := t.m.sourcesDir(false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("the workspace's modules folder: %w", err)
	}
	if _, err := os.Lstat(filepath.Join(base, name)); err == nil {
		return fmt.Errorf("modules/%s is already in the workspace; choose another name", name)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// copyIn copies src into a temporary folder beside the target, moves it into
// place and commits it; a failure leaves nothing behind.
func (t *ModuleImport) copyIn(ctx context.Context, src, name string) (string, error) {
	base, err := t.m.sourcesDir(true)
	if err != nil {
		return "", err
	}
	tmp, err := os.MkdirTemp(base, ".import-"+name+"-")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	if err := builder.CopySource(src, tmp, maxImport); err != nil {
		return "", err
	}
	target := filepath.Join(base, name)
	if err := os.Rename(tmp, target); err != nil {
		return "", err
	}
	rel := modulesFolder + "/" + name
	commit, err := t.m.Workspace.Commit(ctx, []string{rel}, fmt.Sprintf("module %s: import from %s", name, src))
	if err != nil {
		_ = os.RemoveAll(target)
		return "", err
	}
	return commit, nil
}

// shortCommit is a commit id as git shows it short.
func shortCommit(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
