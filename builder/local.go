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
	"time"
	"unicode/utf8"

	"github.com/igorrius/ganglion/internal/procgroup"
	"github.com/igorrius/ganglion/sandbox"
)

// Starter makes a command confined by the sandbox (sandbox.Sandbox).
type Starter interface {
	Command(ctx context.Context, argv []string, dir string, env ...string) (*exec.Cmd, error)
}

// LocalConfig is what the node tells the local builder; the command line
// fills it, the builder reads no environment.
type LocalConfig struct {
	Cargo   string        // path to cargo, found at start with exec.LookPath and ~/.cargo/bin; "" = none
	Rustup  string        // RUSTUP_HOME to pass, from the node's environment
	Timeout time.Duration // how long a build may run
	MaxCopy int64         // bytes of source a build may copy
}

// DefaultLocalConfig is the design's limits: ten minutes, 16 MiB of source.
// The caller adds Cargo and Rustup.
func DefaultLocalConfig() LocalConfig {
	return LocalConfig{Timeout: 10 * time.Minute, MaxCopy: 16 << 20}
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
}

var _ Builder = (*Local)(nil)

// NewLocal makes a builder that starts its commands through start.
func NewLocal(start Starter, cfg LocalConfig) *Local { return &Local{start: start, cfg: cfg} }

// Can runs cargo and rustc once and reports Rust when both answer and the
// wasm32-wasip1 target is installed. A node with no cargo, or no target,
// reports no Rust and no error; a toolchain that cannot run is an error.
func (l *Local) Can(ctx context.Context) (Abilities, error) {
	if l.cfg.Cargo == "" {
		return Abilities{}, nil
	}
	temp, err := os.MkdirTemp("", tempPrefix)
	if err != nil {
		return nil, fmt.Errorf("builder: %w", err)
	}
	defer func() { _ = os.RemoveAll(temp) }()
	env := l.env(temp)
	version, _, err := l.run(ctx, []string{l.cfg.Cargo, "--version"}, temp, env)
	if err != nil {
		return nil, fmt.Errorf("builder: cargo --version: %w", err)
	}
	rustc := filepath.Join(filepath.Dir(l.cfg.Cargo), "rustc")
	root, _, err := l.run(ctx, []string{rustc, "--print", "sysroot"}, temp, env)
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
	if lang != Rust || l.cfg.Cargo == "" {
		return Artifact{}, "", fmt.Errorf("%w: %s", ErrNoBuilder, lang)
	}
	temp, err := os.MkdirTemp("", tempPrefix)
	if err != nil {
		return Artifact{}, "", fmt.Errorf("builder: %w", err)
	}
	defer func() { _ = os.RemoveAll(temp) }()
	work := filepath.Join(temp, "src")
	if err := copySource(src.Dir, work, l.cfg.MaxCopy); err != nil {
		return Artifact{}, "", err
	}
	manifest, err := os.ReadFile(filepath.Join(work, manifestFile))
	if err != nil {
		return Artifact{}, "", fmt.Errorf("builder: %s has no %s: %w", src.Name, manifestFile, err)
	}
	argv := []string{l.cfg.Cargo, "build", "--release", "--locked", "--target", rustTarget}
	_, log, err := l.run(ctx, argv, work, l.env(temp))
	if err != nil {
		return Artifact{}, log, fmt.Errorf("builder: building %s: %w", src.Name, err)
	}
	wasm, err := readWasm(filepath.Join(temp, "target", rustTarget, "release"))
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

// env is everything a build gets on top of the sandbox's own environment: no
// node variable, a temporary cargo home and target folder, the toolchain
// folder the node named.
func (l *Local) env(temp string) []string {
	path := strings.Join([]string{filepath.Dir(l.cfg.Cargo), "/usr/local/bin", "/usr/bin", "/bin"}, ":")
	return []string{
		"PATH=" + path,
		"CARGO_HOME=" + filepath.Join(temp, "cargo-home"),
		"CARGO_TARGET_DIR=" + filepath.Join(temp, "target"),
		"RUSTUP_HOME=" + l.cfg.Rustup,
		"HOME=" + temp,
	}
}

// run runs argv in dir in the sandbox, in a process group of its own, under
// the build's time limit. It returns stdout and the tail of stdout and stderr
// together; a non-zero exit is an error.
func (l *Local) run(ctx context.Context, argv []string, dir string, env []string) ([]byte, Log, error) {
	ctx, cancel := context.WithTimeout(ctx, l.cfg.Timeout)
	defer cancel()
	cmd, err := l.start.Command(ctx, argv, dir, env...)
	if errors.Is(err, sandbox.ErrOff) {
		return nil, "", errors.New("the command sandbox is off, so nothing can be built")
	}
	if err != nil {
		return nil, "", err
	}
	tail, out := &tailWriter{max: logKeep}, &bytes.Buffer{}
	cmd.Stdout, cmd.Stderr = io.MultiWriter(out, tail), tail
	cmd.WaitDelay = waitDelay
	procgroup.Own(cmd)
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

// readWasm reads the one .wasm file a release build left in dir.
func readWasm(dir string) ([]byte, error) {
	found, err := filepath.Glob(filepath.Join(dir, "*.wasm"))
	if err != nil || len(found) != 1 {
		return nil, fmt.Errorf("the build must leave exactly one .wasm file in target/%s/release, found %d", rustTarget, len(found))
	}
	return os.ReadFile(found[0])
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
