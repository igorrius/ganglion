package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/igorrius/ganglion/builder"
	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/tool"
)

// Names of the module tools (R-RUN-9, ADR-0061).
const (
	ModuleImportTool  = "module.import"
	ModuleBuildTool   = "module.build"
	ModuleInstallTool = "module.install"
	ModuleStatusTool  = "module.status"
	ModuleWriteTool   = "module.write"
	ModuleReadTool    = "module.read"
)

// modulesFolder is the workspace folder that holds the modules' sources.
const modulesFolder = "modules"

// ModuleStore plans and installs local builds (modstore.Store).
type ModuleStore interface {
	PlanLocal(ctx context.Context, b modstore.Build, secrets modstore.Secrets, config map[string]any, owners modstore.CellOwners) (modstore.Plan, error)
	InstallLocal(ctx context.Context, b modstore.Build, sha string) (modstore.Outcome, error)
}

// ModuleSettings are the modules: lines of the node's settings file
// (settings.ModulesFile).
type ModuleSettings interface {
	Modules() ([]settings.Module, error)
	SetModule(m settings.Module) error
}

// CellReader reads a cell (cells.Service): its owner decides whether a
// module may report it.
type CellReader interface {
	Get(ctx context.Context, id string) (cells.Cell, error)
}

// ModuleHost runs the owner's modules (modhost.Host): it checks a build in a
// throwaway instance before it is installed, brings a module in line with
// the store and the settings, rolls one back, tells how one stands (its
// /modules card) and gives the node's hint for a failure kind.
type ModuleHost interface {
	Check(ctx context.Context, b modstore.Build, config map[string]any) error
	Reload(ctx context.Context, module string) error
	Rollback(ctx context.Context, name string) (modstore.Outcome, error)
	Card(ctx context.Context, name string) (modstore.Card, error)
	Hint(name string, kind modstore.Health) string
}

// Workspace is the agent's workspace repository (gitrepo.Repo): where the
// modules' sources are, committed.
type Workspace interface {
	Dir() string
	Commit(ctx context.Context, paths []string, msg string) (string, error)
	Head(ctx context.Context) (string, error)
	Dirty(ctx context.Context, rel string) (bool, error)
}

// Modules is what the module tools work with. Every field is required but
// NodeSecrets (none on a node without secrets in its file);
// Examples is "" on a node without an examples folder, and module.import is
// then not offered.
type Modules struct {
	Workspace Workspace
	Builder   builder.Builder
	Store     ModuleStore
	Secrets   secrets.Source
	// NodeSecrets are the names of the node's own secrets (the bot token,
	// the providers' and the search keys): no module is granted one.
	NodeSecrets []string
	Settings    ModuleSettings
	Cells       CellReader
	Host        ModuleHost
	// Builds is the folder builds wait in until they are installed:
	// <data>/modules/builds, which the janitor sweeps (ruling 6 of M4b).
	Builds string
	// Examples is the folder of example modules module.import copies from
	// (examples/modules in a checkout, /app/examples/modules in the image).
	Examples string
	// Provenance is the folder of the records of each module folder as the
	// node's own tools left it: <data>/modules/provenance, outside the
	// workspace and the command sandbox's reach (ruling 7 of M4c, R-RUN-17).
	Provenance string
	// Now is the clock the records are stamped with.
	Now func() time.Time
}

// NewModuleTools returns module.import, module.write, module.read,
// module.build, module.install and module.status over m: the agent copies an
// example into the workspace, writes and reads its source (R-RUN-17,
// ADR-0067), builds it, the owner approves its rights with one press
// (R-RUN-9, ADR-0061), and the agent reads how an installed module stands.
func NewModuleTools(m Modules) []tool.Handler {
	return []tool.Handler{&ModuleImport{m: m}, &ModuleWrite{m: m}, &ModuleRead{m: m}, &ModuleBuild{m: m}, &ModuleInstall{m: m},
		&ModuleStatus{m: m}}
}

// moduleName checks the name of a module's source folder: the short name of
// local/<name>, so a path, "..", or an empty name is refused.
func moduleName(tool, name string) error {
	if why := modstore.CheckShortName(name); why != "" {
		return fmt.Errorf("%s: %q is not a module name: %s", tool, oneLine(name), why)
	}
	return nil
}

// realDir checks that dir is a folder that is not a symlink and that,
// resolved, is exactly want: nothing on the way leads elsewhere. It returns
// fs.ErrNotExist when dir is missing.
func realDir(dir, want string) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a folder (a symlink is refused)", filepath.Base(dir))
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	if resolved != want {
		return fmt.Errorf("%s leads out of its folder", filepath.Base(dir))
	}
	return nil
}

// sourcesDir is the workspace's modules folder, resolved; it must be a
// folder that is not a symlink. create makes it when it is missing.
func (m Modules) sourcesDir(create bool) (string, error) {
	ws, err := filepath.EvalSymlinks(m.Workspace.Dir())
	if err != nil {
		return "", err
	}
	dir := filepath.Join(ws, modulesFolder)
	if create {
		if err := os.Mkdir(dir, 0o750); err != nil && !errors.Is(err, fs.ErrExist) {
			return "", err
		}
	}
	return dir, realDir(dir, dir)
}

// sourceDir is the source folder of module name: exactly
// <workspace>/modules/<name>, a folder, not a symlink, and inside the
// workspace's modules folder once resolved. Its errors are in tool's words.
func (m Modules) sourceDir(tool, name string) (string, error) {
	dir, err := m.moduleFolder(name)
	switch {
	case errors.Is(err, errNoModules):
		return "", fmt.Errorf("%s: the workspace has no modules folder; %s copies an example there", tool, ModuleImportTool)
	case errors.Is(err, fs.ErrNotExist):
		return "", fmt.Errorf("%s: there is no modules/%s in the workspace; %s copies an example there", tool, name, ModuleImportTool)
	case err != nil:
		return "", fmt.Errorf("%s: %w", tool, err)
	}
	return dir, nil
}

// errNoModules is a workspace without its modules folder.
var errNoModules = errors.New("the workspace has no modules folder")

// moduleFolder is <workspace>/modules/<name>, checked as sourceDir says;
// errNoModules without the modules folder, fs.ErrNotExist without the
// module's.
func (m Modules) moduleFolder(name string) (string, error) {
	base, err := m.sourcesDir(false)
	if errors.Is(err, fs.ErrNotExist) {
		return "", errNoModules
	}
	if err != nil {
		return "", fmt.Errorf("the workspace's modules folder: %w", err)
	}
	dir := filepath.Join(base, name)
	if err := realDir(dir, dir); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		return "", fmt.Errorf("modules/%s: %w", name, err)
	}
	return dir, nil
}

// digestPattern is a build's digest as module.build answers it.
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// buildRecord is what the node notes of a build beside its two files: who
// and what made it. The module's name is checked against the manifest when
// it is planned, so the record cannot name another module.
type buildRecord struct {
	Module   string `json:"module"`
	Language string `json:"language"`
	Commit   string `json:"commit"`
	Note     string `json:"note"`
}

// buildPath is the path of a build's file with ext.
func (m Modules) buildPath(sha, ext string) string { return filepath.Join(m.Builds, sha+ext) }

// saveBuild writes a build's wasm, manifest and record into the builds
// folder, each through a temporary file and a rename.
func (m Modules) saveBuild(a builder.Artifact, rec buildRecord) error {
	if err := os.MkdirAll(m.Builds, 0o700); err != nil {
		return err
	}
	meta, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	for _, f := range []struct {
		ext  string
		data []byte
	}{{".wasm", a.Wasm}, {".yaml", a.Manifest}, {".json", meta}} {
		if err := writeFile(m.buildPath(a.SHA256, f.ext), f.data); err != nil {
			return err
		}
	}
	return nil
}

// writeOnce writes data at path unless a file is there already, which it
// keeps: the data goes to a temporary file that is linked into place, so
// path holds either nothing or the whole first write.
func writeOnce(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), path); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	return nil
}

// writeFile replaces path with data through a temporary file beside it.
func writeFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		_ = os.Remove(f.Name())
		return err
	}
	return nil
}

// errNoBuild is a digest with no build waiting.
var errNoBuild = errors.New("no build with this digest is waiting; module.build makes one, and builds are kept for 24 hours")

// loadBuild reads the build named sha and checks that its files are the ones
// the digest names: what is planned and installed is exactly that build.
func (m Modules) loadBuild(sha string) (modstore.Build, error) {
	if !digestPattern.MatchString(sha) {
		return modstore.Build{}, errors.New("build is the 64-character digest module.build answered")
	}
	wasm, err := os.ReadFile(m.buildPath(sha, ".wasm"))
	if errors.Is(err, fs.ErrNotExist) {
		return modstore.Build{}, errNoBuild
	}
	if err != nil {
		return modstore.Build{}, err
	}
	manifest, err := os.ReadFile(m.buildPath(sha, ".yaml"))
	if err != nil {
		return modstore.Build{}, errNoBuild
	}
	var rec buildRecord
	raw, err := os.ReadFile(m.buildPath(sha, ".json"))
	if err != nil || json.Unmarshal(raw, &rec) != nil {
		return modstore.Build{}, errNoBuild
	}
	if modstore.Digest(wasm, manifest) != sha {
		return modstore.Build{}, modstore.ErrBuildMismatch
	}
	return modstore.Build{Module: rec.Module, Wasm: wasm, Manifest: manifest, Language: rec.Language, Note: rec.Note, Commit: rec.Commit}, nil
}

// plan plans b against the node as it is now: its secrets, the module's
// settings line and the cells' owners.
func (m Modules) plan(ctx context.Context, b modstore.Build) (modstore.Plan, error) {
	line, err := m.line(b.Module)
	if err != nil {
		return modstore.Plan{}, err
	}
	return m.Store.PlanLocal(ctx, b, modstore.Secrets{Set: m.secretSet, Node: m.NodeSecrets}, line.Config, m.owner)
}

// line is the settings line of module; one with only the name when there is
// none.
func (m Modules) line(module string) (settings.Module, error) {
	mods, err := m.Settings.Modules()
	if err != nil {
		return settings.Module{}, err
	}
	for _, x := range mods {
		if x.Name == module {
			return x, nil
		}
	}
	return settings.Module{Name: module}, nil
}

// secretSet reports whether the node has a value for the secret name; the
// value itself goes nowhere.
func (m Modules) secretSet(name string) bool {
	v, err := m.Secrets.Secret(name)
	return err == nil && v != ""
}

// owner is modstore.CellOwners over the node's cells.
func (m Modules) owner(ctx context.Context, id string) (string, bool, error) {
	c, err := m.Cells.Get(ctx, id)
	if errors.Is(err, cells.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return c.Owner, true, nil
}

// manifestOf parses the manifest of a build whose plan has no refusal, so it
// parses.
func manifestOf(b modstore.Build) (runner.Manifest, error) { return runner.ParseManifest(b.Manifest) }

// keyHash names a call in a file name: its key holds an event id.
func keyHash(c tool.Call) string {
	sum := sha256.Sum256([]byte(c.Key()))
	return hex.EncodeToString(sum[:8])
}
