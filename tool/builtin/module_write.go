package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/igorrius/ganglion/builder"
	"github.com/igorrius/ganglion/tool"
)

// Limits of module.write (R-RUN-17): a file, and a module's folder after the
// write (what a build copies: no target/ or .git/).
const (
	maxWriteFile   = 256 << 10
	maxWriteModule = 16 << 20
	maxWriteFiles  = 200
	// maxWritePath caps a path's bytes.
	maxWritePath = 512
)

// errWriteTainted refuses module.write in a session with outside content:
// outside text must not dictate code whose install window shows only its
// rights (R-POL-6, ADR-0049, ADR-0067).
var errWriteTainted = errors.New(ModuleWriteTool + ": this conversation holds content from outside, so it cannot write a module's source; " +
	"start a new conversation and ask again")

// ModuleWrite is module.write: it writes one file of a module's source into
// workspace/modules/<name>/ and commits it, with no press; the source does
// nothing until it is built and the owner presses on its install window
// (R-RUN-17, ADR-0067).
type ModuleWrite struct{ m Modules }

type moduleWriteArgs struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Content string `json:"content"`
}

// writeAnswer is module.write's answer.
type writeAnswer struct {
	Written string `json:"written"`
	Commit  string `json:"commit"`
}

// Spec implements tool.Handler. module.write is a read for the policy: the
// owner ruled out a press per write (ruling 9 of M4c); Run refuses it in a
// tainted session instead.
func (*ModuleWrite) Spec() tool.Spec {
	return tool.Spec{Name: ModuleWriteTool,
		Description: "Write one file of a module's source in the workspace, modules/<name>/<path>, and commit it. A new name makes a new module folder. " +
			"path is relative, without .., hidden names or target/; at most 256 KB a file, 16 MB and 200 files a module. " +
			"There is no delete: rewrite a file instead. Refused in a conversation that holds content from outside. module.build builds the folder.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"name":{"type":"string","description":"the module's name: lower-case letters, digits, - and _, starting with a letter"},` +
			`"path":{"type":"string","description":"the file's path in the module's folder, such as src/lib.rs, Cargo.toml or manifest.yaml"},` +
			`"content":{"type":"string","description":"the whole new text of the file"}},` +
			`"required":["name","path","content"],"additionalProperties":false}`),
		Class: tool.ClassRead, Policy: tool.PolicyAuto, AdminOnly: true}
}

// Run implements tool.Handler.
func (t *ModuleWrite) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	if c.Tainted {
		return tool.Result{}, errWriteTainted
	}
	var a moduleWriteArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return tool.Result{}, fmt.Errorf("%s: arguments: %w", ModuleWriteTool, err)
	}
	if err := moduleName(ModuleWriteTool, a.Name); err != nil {
		return tool.Result{}, err
	}
	w, err := t.m.planWrite(a)
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ModuleWriteTool, err)
	}
	commit, err := t.m.applyWrite(ctx, a, w)
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ModuleWriteTool, err)
	}
	if err := t.m.recordProvenance(a.Name, originAgent); err != nil {
		return tool.Result{}, fmt.Errorf("%s: wrote %s (commit %s), but could not note the folder as the node's own, so its builds stay outside content: %w",
			ModuleWriteTool, a.Path, shortCommit(commit), err)
	}
	out, err := json.Marshal(writeAnswer{Written: a.Path, Commit: shortCommit(commit)})
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: string(out)}, nil
}

// pendingWrite is a checked write: where the file goes and what to undo.
type pendingWrite struct {
	ws   string // the workspace, resolved
	file string // the file's full path
	// old is the file's content before the write; had is false for a new
	// file.
	old []byte
	had bool
}

// planWrite checks a write before anything is written: the module's folder
// (a folder, not a symlink, when it exists), the path (cleanPath) and the
// limits.
func (m Modules) planWrite(a moduleWriteArgs) (pendingWrite, error) {
	ws, err := filepath.EvalSymlinks(m.Workspace.Dir())
	if err != nil {
		return pendingWrite{}, err
	}
	dir := filepath.Join(ws, modulesFolder, a.Name)
	for _, d := range []string{filepath.Dir(dir), dir} {
		if err := realDir(d, d); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return pendingWrite{}, fmt.Errorf("%s: %w", relTo(ws, d), err)
		}
	}
	file, err := cleanPath(dir, a.Path)
	if err != nil {
		return pendingWrite{}, err
	}
	if err := writeLimits(dir, a); err != nil {
		return pendingWrite{}, err
	}
	w := pendingWrite{ws: ws, file: file}
	w.old, err = os.ReadFile(file)
	switch {
	case err == nil:
		w.had = true
	case !errors.Is(err, fs.ErrNotExist):
		return pendingWrite{}, err
	}
	return w, nil
}

// applyWrite writes the file and commits it; when the commit fails, the old
// content comes back, or the new file and the folders made for it go.
func (m Modules) applyWrite(ctx context.Context, a moduleWriteArgs, w pendingWrite) (string, error) {
	made := missingFrom(w.ws, w.file)
	if err := os.MkdirAll(filepath.Dir(w.file), 0o750); err != nil {
		return "", err
	}
	if err := writeFile(w.file, []byte(a.Content)); err != nil {
		undoWrite(w, made)
		return "", err
	}
	rel := modulesFolder + "/" + a.Name + "/" + a.Path
	commit, err := m.Workspace.Commit(ctx, []string{rel}, fmt.Sprintf("module %s: write %s", a.Name, a.Path))
	if err != nil {
		undoWrite(w, made)
		return "", fmt.Errorf("commit: %w", err)
	}
	return commit, nil
}

// undoWrite puts back what was there before a write: the folders made for
// it go, or the old content returns, or the new file goes.
func undoWrite(w pendingWrite, made string) {
	switch {
	case made != "":
		_ = os.RemoveAll(made)
	case w.had:
		_ = writeFile(w.file, w.old)
	default:
		_ = os.Remove(w.file)
	}
}

// missingFrom is the topmost folder between base and file's folder that
// does not exist yet; "" when they all do.
func missingFrom(base, file string) string {
	missing := ""
	for d := filepath.Dir(file); d != base && strings.HasPrefix(d, base+string(filepath.Separator)); d = filepath.Dir(d) {
		if _, err := os.Lstat(d); !errors.Is(err, fs.ErrNotExist) {
			break
		}
		missing = d
	}
	return missing
}

// writeLimits checks the limits of a write: the file, and the module's
// folder after it, as a build copies it (the file's old size replaced).
func writeLimits(dir string, a moduleWriteArgs) error {
	if len(a.Content) > maxWriteFile {
		return fmt.Errorf("%s is %d bytes; a file may hold at most 256 KB", a.Path, len(a.Content))
	}
	var files []builder.SourceFile
	if _, err := os.Lstat(dir); err == nil {
		if files, err = builder.SourceFiles(dir); err != nil {
			return err
		}
	}
	total, count := int64(len(a.Content)), 1
	for _, f := range files {
		if f.Path != a.Path {
			total, count = total+f.Size, count+1
		}
	}
	if count > maxWriteFiles {
		return fmt.Errorf("modules/%s would hold %d files; a module may hold at most 200 files", a.Name, count)
	}
	if total > maxWriteModule {
		return fmt.Errorf("modules/%s would hold %d bytes; a module may hold at most 16 MB", a.Name, total)
	}
	return nil
}

// cleanPath checks a module-relative path and gives the file's full path in
// dir: not absolute, no "..", no segment starting with "." (so no .cargo or
// .git), not empty, not under target/ (the build's output), and no symlink
// on the way inside the folder, the file itself included; what exists on
// the way is a folder and the file, when it exists, a regular file
// (R-RUN-17).
func cleanPath(dir, rel string) (string, error) {
	segs, err := pathSegments(rel)
	if err != nil {
		return "", err
	}
	p := dir
	for i, s := range segs {
		p = filepath.Join(p, s)
		info, err := os.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			break
		}
		if err != nil {
			return "", err
		}
		switch last := i == len(segs)-1; {
		case info.Mode()&fs.ModeSymlink != 0:
			return "", fmt.Errorf("%s is a symlink; a path may not pass one", strings.Join(segs[:i+1], "/"))
		case !last && !info.IsDir():
			return "", fmt.Errorf("%s is not a folder", strings.Join(segs[:i+1], "/"))
		case last && !info.Mode().IsRegular():
			return "", fmt.Errorf("%s is not a file", rel)
		}
	}
	return filepath.Join(dir, filepath.FromSlash(rel)), nil
}

// pathSegments splits a module-relative path into its names, refusing what
// cleanPath refuses by the text alone.
func pathSegments(rel string) ([]string, error) {
	switch {
	case rel == "":
		return nil, errors.New("path is empty")
	case len(rel) > maxWritePath:
		return nil, fmt.Errorf("path is over %d bytes", maxWritePath)
	case strings.HasPrefix(rel, "/") || filepath.IsAbs(rel):
		return nil, errors.New("path must be relative to the module's folder")
	case strings.ContainsFunc(rel, func(r rune) bool { return r < 0x20 || r == 0x7f || r == '\\' }):
		return nil, errors.New("path holds a control character or a backslash")
	}
	segs := strings.Split(rel, "/")
	for _, s := range segs {
		switch {
		case s == "":
			return nil, fmt.Errorf("%s has an empty name in it", oneLine(rel))
		case strings.HasPrefix(s, "."):
			return nil, fmt.Errorf("%s: a name starting with \".\" (hidden, . or ..) is refused", oneLine(rel))
		}
	}
	if segs[0] == "target" {
		return nil, errors.New("target/ is the build's output, not the source")
	}
	return segs, nil
}

// relTo is p relative to base, for a message; p itself when it is not below.
func relTo(base, p string) string {
	if r, err := filepath.Rel(base, p); err == nil {
		return filepath.ToSlash(r)
	}
	return p
}
