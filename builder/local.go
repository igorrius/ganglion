package builder

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/igorrius/ganglion/internal/procgroup"
	"github.com/igorrius/ganglion/sandbox"
)

// Starter makes a command confined by the sandbox that writes only beneath
// the folders of write (sandbox.Sandbox.Confined, ADR-0064).
type Starter interface {
	Confined(ctx context.Context, write, argv []string, dir string, env ...string) (*exec.Cmd, error)
}

// LocalConfig is what the node tells the local builder; the command line
// fills it, the builder reads no environment.
type LocalConfig struct {
	Cargo   string        // path to cargo, found at start with exec.LookPath and ~/.cargo/bin; "" = none
	Rustup  string        // RUSTUP_HOME to pass, from the node's environment
	Timeout time.Duration // how long a build may run
	MaxCopy int64         // bytes of source a build may copy
	MaxWasm int64         // bytes of the built .wasm the node will read
	// TempRoot is the node's own folder (mode 0700) the build folders are
	// made in, under the user's cache folder; config fills it. "" builds
	// nothing.
	TempRoot string
	// Toolchain is the rustup toolchain every build is pinned to with
	// RUSTUP_TOOLCHAIN, resolved by the command line at start; "" builds
	// nothing (ADR-0064).
	Toolchain string
}

// DefaultLocalConfig is the design's limits: ten minutes, 16 MiB of source and
// of wasm.
// The caller adds Cargo and Rustup.
func DefaultLocalConfig() LocalConfig {
	return LocalConfig{Timeout: 10 * time.Minute, MaxCopy: 16 << 20, MaxWasm: 16 << 20}
}

const (
	// tempPrefix names the build's temporary folder; the janitor removes
	// what a crash left (config.tempPrefixes).
	tempPrefix = "ganglion-build-"
	// logKeep is how much of the end of the build's output the log keeps.
	logKeep = 8 << 10
	// rustTarget is the target a module is built for.
	rustTarget = "wasm32-wasip1"
	// manifestFile is the module's manifest in its source folder.
	manifestFile = "manifest.yaml"
	// waitDelay is how long the output may stay open after cargo exited.
	waitDelay = time.Second
)

// Local builds in the command sandbox on this node.
type Local struct {
	start Starter
	cfg   LocalConfig
	// parents checks the build root and the folders above it
	// (safeParents); the package's tests that build in a t.TempDir under
	// the shared /tmp replace it.
	parents func(root string) error
}

var _ Builder = (*Local)(nil)

// NewLocal makes a builder that starts its commands through start.
func NewLocal(start Starter, cfg LocalConfig) *Local {
	return &Local{start: start, cfg: cfg, parents: safeParents}
}

// Can runs cargo and rustc once and reports Rust when both answer and the
// wasm32-wasip1 target is installed. A node with no cargo, or no target,
// reports no Rust and no error; a toolchain that cannot run is an error.
func (l *Local) Can(ctx context.Context) (Abilities, error) {
	if l.cfg.Cargo == "" || l.cfg.Toolchain == "" {
		return Abilities{}, nil
	}
	temp, err := l.makeTemp()
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	env := l.env(temp)
	version, _, err := l.run(ctx, []string{l.cfg.Cargo, "--version"}, temp, temp, env)
	if err != nil {
		return nil, fmt.Errorf("builder: cargo --version: %w", err)
	}
	rustc := filepath.Join(filepath.Dir(l.cfg.Cargo), "rustc")
	root, _, err := l.run(ctx, []string{rustc, "--print", "sysroot"}, temp, temp, env)
	if err != nil {
		return nil, fmt.Errorf("builder: rustc --print sysroot: %w", err)
	}
	sysroot := strings.TrimSpace(string(root))
	if _, err := os.Stat(filepath.Join(sysroot, "lib", "rustlib", rustTarget)); err != nil {
		return Abilities{}, nil
	}
	return Abilities{Rust: firstLine(string(version))}, nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// Build copies the source to a temporary folder, builds it there in the
// sandbox and reads the wasm and the manifest. The folder is removed before
// Build returns. Only Rust is built; a source in another language is
// ErrNoBuilder.
func (l *Local) Build(ctx context.Context, src Source) (Artifact, Log, error) {
	lang, err := detect(src.Dir)
	if err != nil {
		return Artifact{}, "", err
	}
	if lang != Rust || l.cfg.Cargo == "" || l.cfg.Toolchain == "" {
		return Artifact{}, "", fmt.Errorf("%w: %s", ErrNoBuilder, lang)
	}
	temp, err := l.makeTemp()
	if err != nil {
		return Artifact{}, "", err
	}
	defer func() { _ = os.RemoveAll(temp) }()
	work := filepath.Join(temp, "src")
	if err := copySource(src.Dir, work, l.cfg.MaxCopy); err != nil {
		return Artifact{}, "", err
	}
	manifest, err := readRegular(filepath.Join(work, manifestFile), l.cfg.MaxCopy)
	if err != nil {
		return Artifact{}, "", fmt.Errorf("builder: %s has no %s: %w", src.Name, manifestFile, err)
	}
	argv := []string{l.cfg.Cargo, "build", "--release", "--locked", "--target", rustTarget}
	_, log, err := l.run(ctx, argv, work, temp, l.env(temp))
	if err != nil {
		return Artifact{}, log, fmt.Errorf("builder: building %s: %w", src.Name, err)
	}
	wasm, err := readWasm(temp, l.cfg.MaxWasm)
	if err != nil {
		return Artifact{}, log, fmt.Errorf("builder: %s: %w", src.Name, err)
	}
	return Artifact{Wasm: wasm, Manifest: manifest, Language: Rust, SHA256: digest(wasm, manifest)}, log, nil
}

// digest is modstore.Digest. The builder does not import modstore (it would
// tie the build to the store's whole dependency tree); a test holds the two
// together.
func digest(wasm, manifest []byte) string {
	w, m := sha256.Sum256(wasm), sha256.Sum256(manifest)
	d := sha256.Sum256(append(w[:], m[:]...))
	return hex.EncodeToString(d[:])
}

// tempFolders are made in the build's folder before cargo runs. The sandbox
// splits the folder when the source holds a denied path such as a .env file,
// and nothing new may then be made directly in it (ADR-0064).
var tempFolders = []string{"cargo-home", "target", "tmp"}

// makeTemp makes the build's temporary folder in the root, the only one its
// commands may write (ADR-0064), with the folders cargo and the linker
// write in.
func (l *Local) makeTemp() (string, error) {
	root := l.cfg.TempRoot
	if err := ensureRoot(root); err != nil {
		return "", err
	}
	if err := l.parents(root); err != nil {
		return "", err
	}
	temp, err := os.MkdirTemp(root, tempPrefix)
	if err != nil {
		return "", fmt.Errorf("builder: %w", err)
	}
	for _, f := range tempFolders {
		if err := os.Mkdir(filepath.Join(temp, f), 0o700); err != nil {
			_ = os.RemoveAll(temp)
			return "", fmt.Errorf("builder: %w", err)
		}
	}
	return temp, nil
}

// ensureRoot makes root and checks that it is the node's own folder that
// no one else may enter: a folder, not a symlink, owned by this user, mode
// 0700. Anything else is refused: another user could have made it first.
// No root (the user's cache folder is unknown) builds nothing.
func ensureRoot(root string) error {
	if root == "" {
		return errors.New("builder: the node has no folder of its own to build in (the user's cache folder is unknown), so nothing is built")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("builder: %w", err)
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("builder: %w", err)
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !info.IsDir() || info.Mode().Perm() != 0o700 || !ok || int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("builder: %s must be a folder of the node's own user with mode 0700, so nothing is built", root)
	}
	return nil
}

// safeParents refuses a build root when it or a folder above it, up to "/",
// is a symlink or not a folder, belongs to a user other than root and the
// node's, or may be written by its group or others. Then only root and the
// node's user can put a file in the build's parent folders, and the check
// of checkAncestors cannot be raced (ADR-0064).
func safeParents(root string) error {
	for dir := root; ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil {
			return fmt.Errorf("builder: %s cannot be checked, so nothing is built: %w", dir, err)
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		switch {
		case !info.IsDir() || !ok:
			return fmt.Errorf("builder: %s, above the build folder, is not a folder (a symlink?), so nothing is built", dir)
		case st.Uid != 0 && int(st.Uid) != os.Geteuid():
			return fmt.Errorf("builder: %s, above the build folder, belongs to another user, so nothing is built", dir)
		case info.Mode().Perm()&0o022 != 0:
			return fmt.Errorf("builder: %s, above the build folder, may be written by others, so nothing is built", dir)
		}
		if dir == filepath.Dir(dir) {
			return nil
		}
	}
}

// ancestorConfigs are the files cargo (.cargo/config, .cargo/config.toml)
// and rustup's proxy (rust-toolchain, rust-toolchain.toml, which may name a
// toolchain by path) read in every parent folder of the folder they run in.
var ancestorConfigs = []string{
	filepath.Join(".cargo", "config"), filepath.Join(".cargo", "config.toml"),
	"rust-toolchain", "rust-toolchain.toml",
}

// checkAncestors refuses a build when temp or any folder above it, up to
// "/", holds a file of ancestorConfigs: cargo would read it and build
// something other than the source (ADR-0064). Anyone who can write a parent
// folder, any user in /tmp, could have put it there. A path that cannot be
// checked refuses too. The message names the node's own path.
func checkAncestors(temp string) error {
	for dir := temp; ; dir = filepath.Dir(dir) {
		for _, name := range ancestorConfigs {
			p := filepath.Join(dir, name)
			_, err := os.Lstat(p)
			switch {
			case err == nil:
				return fmt.Errorf("builder: %s is in a parent folder of the build; cargo would read it and build "+
					"something other than the source, so nothing is built until it is removed", p)
			case errors.Is(err, fs.ErrNotExist), errors.Is(err, syscall.ENOTDIR):
			default:
				return fmt.Errorf("builder: %s cannot be checked, so nothing is built: %w", p, err)
			}
		}
		if dir == filepath.Dir(dir) {
			return nil
		}
	}
}

// env is everything a build gets on top of the sandbox's own environment: no
// node variable, a temporary cargo home, target and temporary folder, the
// toolchain folder the node named.
func (l *Local) env(temp string) []string {
	path := strings.Join([]string{filepath.Dir(l.cfg.Cargo), "/usr/local/bin", "/usr/bin", "/bin"}, ":")
	return []string{
		"PATH=" + path,
		"CARGO_HOME=" + filepath.Join(temp, "cargo-home"),
		"CARGO_TARGET_DIR=" + filepath.Join(temp, "target"),
		"RUSTUP_HOME=" + l.cfg.Rustup,
		"HOME=" + temp,
		"TMPDIR=" + filepath.Join(temp, "tmp"),
		"RUSTUP_TOOLCHAIN=" + l.cfg.Toolchain,
	}
}

// run runs argv in dir in the sandbox, writing only beneath temp, in a
// process group of its own, under the build's time limit. It returns stdout
// and the tail of stdout and stderr together; a non-zero exit is an error.
func (l *Local) run(ctx context.Context, argv []string, dir, temp string, env []string) ([]byte, Log, error) {
	if err := checkAncestors(temp); err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, l.cfg.Timeout)
	defer cancel()
	cmd, err := l.start.Confined(ctx, []string{temp}, argv, dir, env...)
	switch {
	case errors.Is(err, sandbox.ErrOff):
		return nil, "", errors.New("the command sandbox is off, so nothing can be built")
	case errors.Is(err, sandbox.ErrCannotConfine):
		return nil, "", errors.New("this kernel's Landlock cannot keep a build to its own folder (ABI 3 or later is needed), so nothing can be built")
	case err != nil:
		return nil, "", err
	}
	tail, out := &tailWriter{max: logKeep}, &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = io.MultiWriter(out, tail), tail
	cmd.WaitDelay = waitDelay
	procgroup.Own(cmd)
	// The sandbox's helper kills what the build left, setsid or not, when it
	// gets SIGTERM; SIGKILL to the group comes after it (ADR-0064).
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	err = cmd.Start()
	sandbox.Release(cmd)
	if err == nil {
		err = cmd.Wait()
	}
	procgroup.Kill(cmd)
	log := Log(tail.String())
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return out.Bytes(), log, fmt.Errorf("it ran past %s and was stopped", l.cfg.Timeout)
	case ctx.Err() != nil:
		return out.Bytes(), log, ctx.Err()
	case errors.Is(err, exec.ErrWaitDelay):
		return out.Bytes(), log, nil
	case err != nil:
		return out.Bytes(), log, err
	}
	return out.Bytes(), log, nil
}

// detect names the language of a source folder from its files.
func detect(dir string) (Language, error) {
	for _, f := range []struct {
		file string
		lang Language
	}{{"Cargo.toml", Rust}, {"go.mod", Go}} {
		if _, err := os.Stat(filepath.Join(dir, f.file)); err == nil {
			return f.lang, nil
		}
	}
	return "", fmt.Errorf("builder: %s has no Cargo.toml or go.mod: the language is unknown", filepath.Base(dir))
}

// readWasm reads the one .wasm file a release build left in its target
// folder beneath temp. The build's own code wrote it, so it is read as
// hostile: the folders on the way are opened inside temp, and a symlink that
// leads out of it is refused (os.Root), and the file itself is read as
// readRegular reads.
func readWasm(temp string, limit int64) ([]byte, error) {
	root, err := os.OpenRoot(temp)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	release, err := root.OpenRoot(filepath.Join("target", rustTarget, "release"))
	if err != nil {
		return nil, fmt.Errorf("target/%s/release cannot be opened inside the build's folder: %w", rustTarget, err)
	}
	defer func() { _ = release.Close() }()
	found, err := fs.Glob(release.FS(), "*.wasm")
	if err != nil || len(found) != 1 {
		return nil, fmt.Errorf("the build must leave exactly one .wasm file in target/%s/release, found %d", rustTarget, len(found))
	}
	f, err := release.OpenFile(found[0], os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("the .wasm the build left: %s cannot be opened as a plain file: %w", found[0], err)
	}
	b, err := readOpened(f, limit)
	if err != nil {
		return nil, fmt.Errorf("the .wasm the build left: %w", err)
	}
	return b, nil
}

// readRegular reads a regular file of at most limit bytes. It never follows a
// symlink and never blocks on a pipe, and it checks the opened handle, not
// the name: what a build left in its folder is not to be trusted, and the node
// reads it outside the sandbox.
func readRegular(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, fmt.Errorf("%s cannot be opened as a plain file: %w", filepath.Base(path), err)
	}
	return readOpened(f, limit)
}

// readOpened reads f, which it closes, when it is a regular file of at most
// limit bytes.
func readOpened(f *os.File, limit int64) ([]byte, error) {
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", filepath.Base(f.Name()))
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s is over %d bytes, too big to take", filepath.Base(f.Name()), limit)
	}
	return b, nil
}

// copySource copies the regular files of src into dst without a top-level
// target folder. A symlink, or more than limit bytes, is refused.
func copySource(src, dst string, limit int64) error {
	var total int64
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		switch {
		case rel == "target" && d.IsDir():
			return filepath.SkipDir
		case d.IsDir():
			return os.MkdirAll(filepath.Join(dst, rel), 0o700)
		case !d.Type().IsRegular():
			return fmt.Errorf("builder: %s is not a regular file (symlinks and devices are refused)", rel)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if total += info.Size(); total > limit {
			return fmt.Errorf("builder: the source is over %d bytes, too big to build", limit)
		}
		return copyFile(p, filepath.Join(dst, rel))
	})
	if err != nil {
		return fmt.Errorf("builder: copy source: %w", err)
	}
	return nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// tailWriter keeps the last max bytes written to it; stdout and stderr write
// to it from two goroutines.
type tailWriter struct {
	max int
	mu  sync.Mutex
	b   []byte
}

func (t *tailWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(p), nil
}

// String is the kept bytes as valid UTF-8: a cut inside a character goes.
func (t *tailWriter) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	b := t.b
	for len(b) > 0 && !utf8.RuneStart(b[0]) {
		b = b[1:]
	}
	return strings.ToValidUTF8(string(b), "?")
}
