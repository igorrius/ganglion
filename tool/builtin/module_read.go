package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"unicode/utf8"

	"github.com/igorrius/ganglion/builder"
	"github.com/igorrius/ganglion/tool"
)

// maxListed caps the files module.read lists.
const maxListed = 1000

// ModuleRead is module.read: a file of a module's source in the workspace,
// or of an example the node ships, or the list of its files. A workspace
// answer is the agent's own only while the folder is as the node's tools left
// it; otherwise it is outside content (R-RUN-17, R-RUN-13). An example is the
// node's own: reading one needs no copy in the workspace (#350).
type ModuleRead struct{ m Modules }

type readArgs struct {
	Name    string `json:"name"`
	Example string `json:"example"`
	Path    string `json:"path"`
}

// listAnswer is module.read's answer without a path.
type listAnswer struct {
	Files []builder.SourceFile `json:"files"`
	// More counts the files past the first maxListed.
	More int `json:"more,omitempty"`
}

// Spec implements tool.Handler.
func (*ModuleRead) Spec() tool.Spec {
	return tool.Spec{Name: ModuleReadTool,
		Description: "Read a module's source: with name, a module in the workspace; with example, an example the node ships (such as " +
			"reflex-template), read in place, with no copy and no question. With path, one file; without it, the list of the folder's files " +
			"with their sizes (no target/ or .git/). A workspace folder changed by other means than module.write or module.import answers " +
			"as outside content.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"name":{"type":"string","description":"the module's folder under modules/ in the workspace; give name or example, not both"},` +
			`"example":{"type":"string","description":"an example the node ships, such as reader-template, reflex-template or web-template"},` +
			`"path":{"type":"string","description":"the file's path in the folder, such as src/lib.rs; leave it out for the list"}},` +
			`"additionalProperties":false}`),
		Class: tool.ClassRead, Policy: tool.PolicyAuto, AdminOnly: true}
}

// Run implements tool.Handler. Whether the folder is the node's own is
// decided after it is read, so a change before the read shows.
func (t *ModuleRead) Run(_ context.Context, c tool.Call) (tool.Result, error) {
	var a readArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return tool.Result{}, fmt.Errorf("%s: arguments: %w", ModuleReadTool, err)
	}
	dir, own, err := t.folder(a)
	if err != nil {
		return tool.Result{}, err
	}
	var text string
	if a.Path == "" {
		text, err = listFiles(dir)
	} else {
		text, err = readSource(dir, a.Path)
	}
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ModuleReadTool, err)
	}
	return tool.Result{Text: text, External: !own()}, nil
}

// folder is the folder a call reads and whether its files are the node's
// own, asked after the read. A workspace name that is not there but names an
// example says how to read the example instead of copying it.
func (t *ModuleRead) folder(a readArgs) (string, func() bool, error) {
	switch {
	case (a.Name == "") == (a.Example == ""):
		return "", nil, fmt.Errorf("%s: give name (a module in the workspace) or example (an example the node ships), one of them", ModuleReadTool)
	case a.Example != "":
		dir, err := t.m.exampleDir(a.Example)
		if err != nil {
			return "", nil, fmt.Errorf("%s: %w", ModuleReadTool, err)
		}
		return dir, func() bool { return true }, nil
	}
	if err := moduleName(ModuleReadTool, a.Name); err != nil {
		return "", nil, err
	}
	dir, err := t.m.sourceDir(ModuleReadTool, a.Name)
	if err != nil {
		if _, xerr := t.m.exampleDir(a.Name); xerr == nil {
			return "", nil, fmt.Errorf("%w; %s is an example the node ships: read it with {\"example\": %q}, with no copy", err, a.Name, a.Name)
		}
		return "", nil, err
	}
	return dir, func() bool { return t.m.ownSource(a.Name) }, nil
}

// listFiles is the folder's files as the digest walks them, as JSON.
func listFiles(dir string) (string, error) {
	files, err := builder.SourceFiles(dir)
	if err != nil {
		return "", err
	}
	ans := listAnswer{Files: files}
	if len(files) > maxListed {
		ans.Files, ans.More = files[:maxListed], len(files)-maxListed
	}
	if ans.Files == nil {
		ans.Files = []builder.SourceFile{}
	}
	out, err := json.Marshal(ans)
	return string(out), err
}

// readSource is the text of the file rel in dir: a regular file reached
// with no symlink on the way, at most maxWriteFile bytes, valid UTF-8.
func readSource(dir, rel string) (string, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
	if err := cleanPath(root, rel); err != nil {
		return "", err
	}
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		return "", fmt.Errorf("there is no %s in the module's folder", rel)
	}
	defer func() { _ = f.Close() }()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a file", rel)
	}
	b, err := io.ReadAll(io.LimitReader(f, maxWriteFile+1))
	switch {
	case err != nil:
		return "", err
	case len(b) > maxWriteFile:
		return "", fmt.Errorf("%s is over 256 KB, more than module.read reads", rel)
	case !utf8.Valid(b):
		return "", fmt.Errorf("%s is not text", rel)
	}
	return string(b), nil
}
