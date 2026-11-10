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
	defer t.m.locks.lock(a.Name)()
	if t.m.Queue.Building(a.Name) {
		return tool.Result{}, fmt.Errorf("%s: %w", ModuleWriteTool, errBeingBuilt(a.Name))
	}
	commit, fresh, err := t.m.writeSource(ctx, a)
	if err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ModuleWriteTool, err)
	}
	if err := t.m.recordFile(a.Name, a.Path, []byte(a.Content), fresh); err != nil {
		return tool.Result{}, fmt.Errorf("%s: wrote %s (commit %s), but could not note it as the node's own, so the folder's builds stay outside content: %w",
			ModuleWriteTool, a.Path, shortCommit(commit), err)
	}
	out, err := json.Marshal(writeAnswer{Written: a.Path, Commit: shortCommit(commit)})
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: string(out)}, nil
}

// tempName is the file a write goes through before it is renamed into
// place: hidden, at the top of the module's folder, so the rename stays in
// the folder's os.Root. Only module.write makes it, under the module's lock;
// one a crash left goes with the next write.
const tempName = ".write.tmp"

// pendingWrite is a write being made: the module's folder as roots nothing
// can be reached out of, and what to undo.
type pendingWrite struct {
	mods  *os.Root // <workspace>/modules
	mod   *os.Root // <workspace>/modules/<name>
	name  string
	rel   string // the file, relative to mod
	fresh bool   // the module's folder was made for this write
	made  string // the topmost folder made in mod for the file; "" for none
	old   []byte // the file's content before the write
	had   bool   // false for a new file
}

// close closes the roots.
func (w *pendingWrite) close() {
	if w.mod != nil {
		_ = w.mod.Close()
	}
	if w.mods != nil {
		_ = w.mods.Close()
	}
}

// writeSource checks, writes and commits one file; fresh tells whether the
// module's folder was made for it. Every write goes through an os.Root on
// the module's folder, so a symlink made after the checks cannot take it out
// of the folder. A failure leaves the folder as it was.
func (m Modules) writeSource(ctx context.Context, a moduleWriteArgs) (string, bool, error) {
	segs, err := pathSegments(a.Path)
	if err != nil {
		return "", false, err
	}
	if len(a.Content) > maxWriteFile {
		return "", false, fmt.Errorf("%s is %d bytes; a file may hold at most 256 KB", a.Path, len(a.Content))
	}
	w := &pendingWrite{name: a.Name, rel: filepath.FromSlash(a.Path)}
	defer w.close()
	if err := m.openModule(w); err != nil {
		w.dropFresh()
		return "", false, err
	}
	if err := w.check(segs, a); err != nil {
		w.dropFresh()
		return "", false, err
	}
	commit, err := m.put(ctx, w, a)
	if err != nil {
		w.undo()
		return "", false, err
	}
	return commit, w.fresh, nil
}

// openModule opens the modules folder and the module's (made when it is
// new) as roots; each must be a folder, not a symlink, and the module's
// root must be the very folder checked.
func (m Modules) openModule(w *pendingWrite) error {
	dir, err := m.sourcesDir(true)
	if err != nil {
		return fmt.Errorf("the workspace's modules folder: %w", err)
	}
	if w.mods, err = os.OpenRoot(dir); err != nil {
		return err
	}
	info, err := w.mods.Lstat(w.name)
	if errors.Is(err, fs.ErrNotExist) {
		if err := w.mods.Mkdir(w.name, 0o750); err != nil {
			return err
		}
		w.fresh = true
		info, err = w.mods.Lstat(w.name)
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("modules/%s is not a folder (a symlink is refused)", w.name)
	}
	if w.mod, err = w.mods.OpenRoot(w.name); err != nil {
		return err
	}
	if opened, err := w.mod.Stat("."); err != nil || !os.SameFile(opened, info) {
		return fmt.Errorf("modules/%s changed while it was opened", w.name)
	}
	return nil
}

// check removes a temporary file a crash left, then checks the path in the
// folder (cleanPath), the limits, and notes the old content and the folders
// the write will make.
func (w *pendingWrite) check(segs []string, a moduleWriteArgs) error {
	if err := w.mod.Remove(tempName); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := cleanPath(w.mod, a.Path); err != nil {
		return err
	}
	if err := writeLimits(filepath.Join(w.mods.Name(), w.name), a); err != nil {
		return err
	}
	old, err := w.mod.ReadFile(w.rel)
	switch {
	case err == nil:
		w.old, w.had = old, true
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	for i := range segs[:len(segs)-1] {
		p := filepath.Join(segs[:i+1]...)
		if _, err := w.mod.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			w.made = p
			break
		}
	}
	return nil
}

// put writes the file and commits it.
func (m Modules) put(ctx context.Context, w *pendingWrite, a moduleWriteArgs) (string, error) {
	if dir := filepath.Dir(w.rel); dir != "." {
		if err := w.mod.MkdirAll(dir, 0o750); err != nil {
			return "", err
		}
	}
	if err := w.replace([]byte(a.Content)); err != nil {
		return "", err
	}
	rel := modulesFolder + "/" + a.Name + "/" + a.Path
	commit, err := m.Workspace.Commit(ctx, []string{rel}, fmt.Sprintf("module %s: write %s", a.Name, a.Path))
	if err != nil {
		return "", fmt.Errorf("commit: %w", err)
	}
	return commit, nil
}

// replace puts data at the file through tempName and a rename, both in the
// module's root.
func (w *pendingWrite) replace(data []byte) error {
	f, err := w.mod.OpenFile(tempName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = w.mod.Rename(tempName, w.rel)
	}
	if err != nil {
		_ = w.mod.Remove(tempName)
	}
	return err
}

// dropFresh removes the module's folder when it was made for a write that
// was refused before anything was written in it.
func (w *pendingWrite) dropFresh() {
	if w.fresh && w.mods != nil {
		_ = w.mods.RemoveAll(w.name)
	}
}

// undo puts back what was there before the write: the module's folder made
// for it goes, or the folders made in it, or the old content returns, or
// the new file goes.
func (w *pendingWrite) undo() {
	switch {
	case w.mods == nil:
	case w.fresh:
		_ = w.mods.RemoveAll(w.name)
	case w.mod == nil:
	case w.made != "":
		_ = w.mod.RemoveAll(w.made)
	case w.had:
		_ = w.replace(w.old)
	default:
		_ = w.mod.Remove(w.rel)
	}
}

// writeLimits checks the module's folder after the write, as a build copies
// it (the file's old size replaced): at most 200 files and 16 MB.
func writeLimits(dir string, a moduleWriteArgs) error {
	files, err := builder.SourceFiles(dir)
	if err != nil {
		return err
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

// cleanPath checks a module-relative path in the module's root: not
// absolute, no "..", no segment starting with "." (so no .cargo or .git),
// not empty, not under target/ (the build's output), and no symlink on the
// way inside the folder, the file itself included; what exists on the way
// is a folder and the file, when it exists, a regular file (R-RUN-17). The
// root keeps a symlink made after the check from leading out of the folder.
func cleanPath(root *os.Root, rel string) error {
	segs, err := pathSegments(rel)
	if err != nil {
		return err
	}
	for i := range segs {
		p := filepath.Join(segs[:i+1]...)
		info, err := root.Lstat(p)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		switch last := i == len(segs)-1; {
		case info.Mode()&fs.ModeSymlink != 0:
			return fmt.Errorf("%s is a symlink; a path may not pass one", filepath.ToSlash(p))
		case !last && !info.IsDir():
			return fmt.Errorf("%s is not a folder", filepath.ToSlash(p))
		case last && !info.Mode().IsRegular():
			return fmt.Errorf("%s is not a file", rel)
		}
	}
	return nil
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
