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
// or the list of its files. The answer is the agent's own only while the
// folder is as the node's tools left it; otherwise it is outside content
// (R-RUN-17, R-RUN-13).
type ModuleRead struct{ m Modules }

type readArgs struct {
	Name string `json:"name"`
	Path string `json:"path"`
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
		Description: "Read a module's source in the workspace: with path, the file modules/<name>/<path>; without it, the list of the folder's files " +
			"with their sizes (no target/ or .git/). A folder changed by other means than module.write or module.import answers as outside content.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"name":{"type":"string","description":"the module's folder under modules/ in the workspace"},` +
			`"path":{"type":"string","description":"the file's path in the module's folder, such as src/lib.rs; leave it out for the list"}},` +
			`"required":["name"],"additionalProperties":false}`),
		Class: tool.ClassRead, Policy: tool.PolicyAuto, AdminOnly: true}
}

// Run implements tool.Handler. Whether the folder is the node's own is
// decided after it is read, so a change before the read shows.
func (t *ModuleRead) Run(_ context.Context, c tool.Call) (tool.Result, error) {
	var a readArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return tool.Result{}, fmt.Errorf("%s: arguments: %w", ModuleReadTool, err)
	}
	if err := moduleName(ModuleReadTool, a.Name); err != nil {
		return tool.Result{}, err
	}
	dir, err := t.m.sourceDir(ModuleReadTool, a.Name)
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
	return tool.Result{Text: text, External: !t.m.ownSource(a.Name)}, nil
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
	if _, err := cleanPath(dir, rel); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return "", err
	}
	defer func() { _ = root.Close() }()
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
