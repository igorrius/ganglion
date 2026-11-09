//go:build linux

package builder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/sandbox"
)

// TestMain lets the test binary be the sandbox's helper, as the ganglion
// binary is.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == sandbox.HelperArg {
		os.Exit(sandbox.Helper())
	}
	os.Exit(m.Run())
}

// call is one command the fake starter was asked for.
type call struct {
	write []string
	argv  []string
	dir   string
	env   []string
}

// fakeStarter records the commands and, for a cargo build, writes what cargo
// would: a .wasm in the target folder the environment names.
type fakeStarter struct {
	mu    sync.Mutex
	calls []call
	wasm  []byte
	exit  string // a shell script to run instead of true
}

func (f *fakeStarter) Confined(ctx context.Context, write, argv []string, dir string, env ...string) (*exec.Cmd, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call{slices.Clone(write), slices.Clone(argv), dir, slices.Clone(env)})
	if target := envValue(env, "CARGO_TARGET_DIR"); target != "" && f.wasm != nil {
		out := filepath.Join(target, rustTarget, "release")
		if err := os.MkdirAll(out, 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(out, "m.wasm"), f.wasm, 0o600); err != nil {
			return nil, err
		}
	}
	if f.exit != "" {
		return exec.CommandContext(ctx, "sh", "-c", f.exit), nil
	}
	return exec.CommandContext(ctx, "true"), nil
}

func envValue(env []string, key string) string {
	for _, e := range env {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			return v
		}
	}
	return ""
}

// source writes a module folder with files and returns it.
func source(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "mod")
	for name, body := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// buildTemp points the temporary folder at one the test owns, mode 0700 as
// a build root must be (testConfig builds there), and returns it.
func buildTemp(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	if err := os.Chmod(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)
	return tmp
}

// testConfig is a config for the fake starter, building in the temporary
// folder of the test (buildTemp).
func testConfig() LocalConfig {
	cfg := DefaultLocalConfig()
	cfg.Cargo, cfg.Rustup, cfg.Toolchain, cfg.TempRoot = "/opt/rust/bin/cargo", "/opt/rust/rustup", "stable-test", os.TempDir()
	return cfg
}

// newLocal is NewLocal for a root in a t.TempDir: /tmp above it may be
// written by others, so the check of the folders above the root is left
// out; the real-Rust tests keep it.
func newLocal(start Starter, cfg LocalConfig) *Local {
	l := NewLocal(start, cfg)
	l.parents = func(string) error { return nil }
	return l
}

var rustFiles = map[string]string{"Cargo.toml": "[package]\n", "manifest.yaml": "name: local/m\n", "src/lib.rs": "//"}

func TestBuildRunsCargoInTheCopyWithATemporaryEnvironment(t *testing.T) {
	tmp := buildTemp(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "secret-token-value")
	dir := source(t, rustFiles)
	fake := &fakeStarter{wasm: []byte("\x00asm\x01\x00\x00\x00")}
	art, _, err := newLocal(fake, testConfig()).Build(t.Context(), Source{Name: "m", Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.calls) != 1 {
		t.Fatalf("%d commands, want 1", len(fake.calls))
	}
	c := fake.calls[0]
	want := []string{"/opt/rust/bin/cargo", "build", "--release", "--locked", "--target", "wasm32-wasip1"}
	if !slices.Equal(c.argv, want) {
		t.Fatalf("argv = %q, want %q", c.argv, want)
	}
	temp := filepath.Dir(c.dir)
	if filepath.Dir(temp) != tmp || !strings.HasPrefix(filepath.Base(temp), "ganglion-build-") || filepath.Base(c.dir) != "src" {
		t.Fatalf("dir = %q, want <tmp>/ganglion-build-*/src", c.dir)
	}
	wantEnv := []string{"PATH=/opt/rust/bin:/usr/local/bin:/usr/bin:/bin", "CARGO_HOME=" + temp + "/cargo-home",
		"CARGO_TARGET_DIR=" + temp + "/target", "RUSTUP_HOME=/opt/rust/rustup", "HOME=" + temp, "TMPDIR=" + temp + "/tmp", "RUSTUP_TOOLCHAIN=stable-test"}
	if !slices.Equal(c.env, wantEnv) {
		t.Fatalf("env = %q, want %q", c.env, wantEnv)
	}
	if !slices.Equal(c.write, []string{temp}) {
		t.Fatalf("write = %q, want only the build's folder %q (ADR-0064)", c.write, temp)
	}
	if strings.Contains(strings.Join(c.env, "\n"), "secret-token-value") || envValue(c.env, "TELEGRAM_BOT_TOKEN") != "" {
		t.Fatal("a node variable reached the build's environment")
	}
	if string(art.Manifest) != rustFiles["manifest.yaml"] || art.Language != Rust || !slices.Equal(art.Wasm, fake.wasm) {
		t.Fatalf("artifact = %+v", art)
	}
	left, _ := os.ReadDir(tmp)
	if len(left) != 0 {
		t.Fatalf("the temporary folder holds %d entries after the build", len(left))
	}
}

func TestArtifactIdentityIsTheStoresDigest(t *testing.T) {
	buildTemp(t)
	fake := &fakeStarter{wasm: []byte("\x00asm\x01\x00\x00\x00")}
	art, _, err := newLocal(fake, testConfig()).Build(t.Context(), Source{Name: "m", Dir: source(t, rustFiles)})
	if err != nil {
		t.Fatal(err)
	}
	if want := modstore.Digest(art.Wasm, art.Manifest); art.SHA256 != want {
		t.Fatalf("SHA256 = %s, modstore.Digest = %s", art.SHA256, want)
	}
}

func TestTheCopyLeavesTheSourceAndSkipsTarget(t *testing.T) {
	buildTemp(t)
	files := map[string]string{"Cargo.toml": "[package]\n", "manifest.yaml": "name: local/m\n", "target/debug/x": "big"}
	dir := source(t, files)
	var seen []string
	fake := &fakeStarter{wasm: []byte("w")}
	b := newLocal(&listStarter{fakeStarter: fake, seen: &seen}, testConfig())
	if _, _, err := b.Build(t.Context(), Source{Name: "m", Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(seen, "target") || !slices.Contains(seen, "Cargo.toml") {
		t.Fatalf("the copy held %q", seen)
	}
	if _, err := os.Stat(filepath.Join(dir, "target", "debug", "x")); err != nil {
		t.Fatalf("the source changed: %v", err)
	}
}

// listStarter lists the working folder when the command is asked for.
type listStarter struct {
	*fakeStarter
	seen *[]string
}

func (l *listStarter) Confined(ctx context.Context, write, argv []string, dir string, env ...string) (*exec.Cmd, error) {
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		*l.seen = append(*l.seen, e.Name())
	}
	return l.fakeStarter.Confined(ctx, write, argv, dir, env...)
}

func TestBuildRefusals(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		cfg   func(*LocalConfig)
		want  string
		is    error
	}{
		"go":            {files: map[string]string{"go.mod": "module x\n", "manifest.yaml": "x"}, is: ErrNoBuilder},
		"no cargo":      {files: rustFiles, cfg: func(c *LocalConfig) { c.Cargo = "" }, is: ErrNoBuilder},
		"no toolchain":  {files: rustFiles, cfg: func(c *LocalConfig) { c.Toolchain = "" }, is: ErrNoBuilder},
		"no root":       {files: rustFiles, cfg: func(c *LocalConfig) { c.TempRoot = "" }, want: "no folder of its own"},
		"relative root": {files: rustFiles, cfg: func(c *LocalConfig) { c.TempRoot = "cache/ganglion/builds" }, want: "not an absolute path"},
		"unknown":       {files: map[string]string{"manifest.yaml": "x"}, want: "language is unknown"},
		"over the cap":  {files: rustFiles, cfg: func(c *LocalConfig) { c.MaxCopy = 10 }, want: "too big"},
		"no manifest":   {files: map[string]string{"Cargo.toml": "[package]\n"}, want: "manifest.yaml"},
		"cargo failure": {files: rustFiles, want: "exit status 3"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tmp := buildTemp(t)
			cfg := testConfig()
			if c.cfg != nil {
				c.cfg(&cfg)
			}
			fake := &fakeStarter{wasm: []byte("w")}
			if name == "cargo failure" {
				fake.exit = "echo boom >&2; exit 3"
			}
			_, log, err := newLocal(fake, cfg).Build(t.Context(), Source{Name: "m", Dir: source(t, c.files)})
			if err == nil {
				t.Fatal("no error")
			}
			if c.is != nil && !errors.Is(err, c.is) || c.want != "" && !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v", err)
			}
			if name == "cargo failure" && !strings.Contains(string(log), "boom") {
				t.Fatalf("log = %q, want the build's output", log)
			}
			if left, _ := os.ReadDir(tmp); len(left) != 0 {
				t.Fatalf("a refused build left %d entries", len(left))
			}
			if name != "cargo failure" && len(fake.calls) != 0 {
				t.Fatal("a refused source started a command")
			}
		})
	}
}

func TestASymlinkInTheSourceIsRefused(t *testing.T) {
	buildTemp(t)
	dir := source(t, rustFiles)
	if err := os.Symlink("/etc/passwd", filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	_, _, err := newLocal(&fakeStarter{}, testConfig()).Build(t.Context(), Source{Name: "m", Dir: dir})
	if err == nil || !strings.Contains(err.Error(), "link") {
		t.Fatalf("error = %v, want one naming the link", err)
	}
}

// planter is a fake starter that leaves what a hostile build could leave in
// the release folder instead of one honest .wasm.
type planter struct {
	*fakeStarter
	plant func(dir string) error
}

func (p *planter) Confined(ctx context.Context, write, argv []string, dir string, env ...string) (*exec.Cmd, error) {
	out := filepath.Join(envValue(env, "CARGO_TARGET_DIR"), rustTarget, "release")
	if err := os.MkdirAll(out, 0o700); err != nil {
		return nil, err
	}
	if err := p.plant(out); err != nil {
		return nil, err
	}
	return p.fakeStarter.Confined(ctx, write, argv, dir, env...)
}

func TestAWasmThatIsNotOneHonestFileIsRefused(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("node-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideWasm := filepath.Join(t.TempDir(), "target", rustTarget, "release")
	if err := os.MkdirAll(outsideWasm, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideWasm, "x.wasm"), []byte("node-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		plant func(dir string) error
		want  string
	}{
		"none":    {func(string) error { return nil }, "exactly one .wasm"},
		"two":     {func(d string) error { return twoFiles(d) }, "found 2"},
		"symlink": {func(d string) error { return os.Symlink(secret, filepath.Join(d, "x.wasm")) }, "cannot be opened as a plain file"},
		"fifo":    {func(d string) error { return syscall.Mkfifo(filepath.Join(d, "x.wasm"), 0o600) }, "not a regular file"},
		"device":  {func(d string) error { return os.Symlink("/dev/zero", filepath.Join(d, "x.wasm")) }, "cannot be opened as a plain file"},
		"huge": {func(d string) error {
			return os.WriteFile(filepath.Join(d, "x.wasm"), make([]byte, 1025), 0o600)
		}, "too big"},
		// A release folder, or a folder above it, swapped for a symlink to a
		// folder outside the build's that holds one honest-looking .wasm.
		"symlinked release": {func(d string) error { return swapForLink(d, outsideWasm) }, "inside the build's folder"},
		"symlinked target": {func(d string) error {
			return swapForLink(filepath.Dir(filepath.Dir(d)), filepath.Dir(filepath.Dir(outsideWasm)))
		}, "inside the build's folder"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			tmp := buildTemp(t)
			cfg := testConfig()
			cfg.MaxWasm = 1024
			b := newLocal(&planter{&fakeStarter{}, c.plant}, cfg)
			done := make(chan error, 1)
			go func() {
				_, _, err := b.Build(t.Context(), Source{Name: "m", Dir: source(t, rustFiles)})
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), c.want) {
					t.Fatalf("error = %v, want %q", err, c.want)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("Build hangs on what the build left")
			}
			if left, _ := os.ReadDir(tmp); len(left) != 0 {
				t.Fatalf("left %d entries", len(left))
			}
		})
	}
}

// swapForLink replaces the folder dir with a symlink to target.
func swapForLink(dir, target string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return os.Symlink(target, dir)
}

func twoFiles(dir string) error {
	for _, n := range []string{"a.wasm", "b.wasm"} {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("w"), 0o600); err != nil {
			return err
		}
	}
	return nil
}

func TestAnOversizedManifestIsRefused(t *testing.T) {
	buildTemp(t)
	files := map[string]string{"Cargo.toml": "[package]\n", "manifest.yaml": strings.Repeat("a", 100)}
	cfg := testConfig()
	cfg.MaxCopy = 50
	_, _, err := newLocal(&fakeStarter{}, cfg).Build(t.Context(), Source{Name: "m", Dir: source(t, files)})
	if err == nil {
		t.Fatal("no error")
	}
}

func TestTheLogKeepsTheLast8KiB(t *testing.T) {
	buildTemp(t)
	fake := &fakeStarter{wasm: []byte("w"), exit: `head -c 20000 /dev/zero | tr '\0' a; echo; echo THE-END`}
	_, log, err := newLocal(fake, testConfig()).Build(t.Context(), Source{Name: "m", Dir: source(t, rustFiles)})
	if err != nil {
		t.Fatal(err)
	}
	if len(log) > logKeep || !strings.HasSuffix(strings.TrimSpace(string(log)), "THE-END") {
		t.Fatalf("log of %d bytes ends %q", len(log), string(log)[max(0, len(log)-20):])
	}
}

func TestATimedOutBuildIsStoppedAndCleanedUp(t *testing.T) {
	tmp := buildTemp(t)
	cfg := testConfig()
	cfg.Timeout = 200 * time.Millisecond
	fake := &fakeStarter{wasm: []byte("w"), exit: "sleep 30"}
	_, _, err := newLocal(fake, cfg).Build(t.Context(), Source{Name: "m", Dir: source(t, rustFiles)})
	if err == nil || !strings.Contains(err.Error(), "ran past") {
		t.Fatalf("error = %v", err)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatalf("a stopped build left %d entries", len(left))
	}
}

// A build past its time limit is sent SIGTERM, which the sandbox's helper
// takes as the word to kill what the build left (ADR-0064), not SIGKILL,
// which would end the helper first.
func TestATimedOutBuildIsSentSIGTERM(t *testing.T) {
	buildTemp(t)
	cfg := testConfig()
	cfg.Timeout = 200 * time.Millisecond
	fake := &fakeStarter{wasm: []byte("w"), exit: `trap 'echo got-term; exit 7' TERM; sleep 30 >/dev/null 2>&1 & wait`}
	_, log, err := newLocal(fake, cfg).Build(t.Context(), Source{Name: "m", Dir: source(t, rustFiles)})
	if err == nil || !strings.Contains(err.Error(), "ran past") {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(string(log), "got-term") {
		t.Fatalf("the build was not sent SIGTERM: %q", log)
	}
}

// ADR-0064, abuse test: a cargo or rustup configuration in a parent folder
// of the build, which anyone who can write that folder could plant, refuses
// the build and Can before any command starts, naming its path.
func TestAConfigInAParentFolderRefusesTheBuild(t *testing.T) {
	for _, name := range []string{".cargo/config", ".cargo/config.toml", "rust-toolchain", "rust-toolchain.toml"} {
		t.Run(name, func(t *testing.T) {
			buildTemp(t)
			parent := t.TempDir()
			planted := filepath.Join(parent, name)
			if err := os.MkdirAll(filepath.Dir(planted), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(planted, []byte("[build]\nrustc-wrapper = \"/x\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg := testConfig()
			cfg.TempRoot = filepath.Join(parent, "builds")
			fake := &fakeStarter{wasm: []byte("w")}
			b := newLocal(fake, cfg)
			_, _, err := b.Build(t.Context(), Source{Name: "m", Dir: source(t, rustFiles)})
			if err == nil || !strings.Contains(err.Error(), planted) {
				t.Fatalf("Build: %v, want a refusal naming %s", err, planted)
			}
			if _, err := b.Can(t.Context()); err == nil || !strings.Contains(err.Error(), planted) {
				t.Fatalf("Can: %v, want a refusal naming %s", err, planted)
			}
			if len(fake.calls) != 0 {
				t.Fatal("a command started under a planted configuration")
			}
			if left, _ := os.ReadDir(cfg.TempRoot); len(left) != 0 {
				t.Fatalf("a refused build left %d entries", len(left))
			}
		})
	}
}

// The build folders are made in the node's own 0700 root; a root that is
// not that (another user could have made it first) refuses the build.
func TestTheTempRootIsTheNodesOwn(t *testing.T) {
	parent := t.TempDir()
	cfg := testConfig()
	cfg.TempRoot = filepath.Join(parent, "builds")
	fake := &fakeStarter{wasm: []byte("w")}
	if _, _, err := newLocal(fake, cfg).Build(t.Context(), Source{Name: "m", Dir: source(t, rustFiles)}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Lstat(cfg.TempRoot); err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("the root: %v, %v", info, err)
	}
	if got := filepath.Dir(filepath.Dir(fake.calls[0].dir)); got != cfg.TempRoot {
		t.Fatalf("the build ran in %s, want a folder in %s", fake.calls[0].dir, cfg.TempRoot)
	}
	open := filepath.Join(parent, "open")
	if err := os.Mkdir(open, 0o755); err != nil { //nolint:gosec // a root that is too open, on purpose
		t.Fatal(err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(cfg.TempRoot, link); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{open, link} {
		cfg.TempRoot = root
		_, _, err := newLocal(&fakeStarter{wasm: []byte("w")}, cfg).Build(t.Context(), Source{Name: "m", Dir: source(t, rustFiles)})
		if err == nil || !strings.Contains(err.Error(), "mode 0700") {
			t.Errorf("root %s: %v, want a refusal", root, err)
		}
	}
}

// ADR-0064: a build root with a folder above it that others may write (a
// t.TempDir is under /tmp) is refused by the real check before anything
// runs; a root whose parents are root's and not writable by others passes.
func TestARootUnderASharedFolderIsRefused(t *testing.T) {
	buildTemp(t)
	cfg := testConfig()
	cfg.TempRoot = filepath.Join(t.TempDir(), "builds")
	fake := &fakeStarter{wasm: []byte("w")}
	_, _, err := NewLocal(fake, cfg).Build(t.Context(), Source{Name: "m", Dir: source(t, rustFiles)})
	if err == nil || !strings.Contains(err.Error(), "may be written by others") {
		t.Fatalf("Build under /tmp: %v, want a refusal naming the shared folder", err)
	}
	if len(fake.calls) != 0 {
		t.Fatal("a command started under a shared folder")
	}
	if info, err := os.Stat("/usr/bin"); err != nil || info.Mode().Perm()&0o022 != 0 {
		t.Skip("no /usr/bin that only root writes")
	}
	if err := safeParents("/usr/bin"); err != nil {
		t.Fatalf("safeParents(/usr/bin) = %v", err)
	}
	if err := safeParents("usr/bin"); err == nil || !strings.Contains(err.Error(), "not an absolute path") {
		t.Fatalf("safeParents of a relative root = %v, want a refusal", err)
	}
}

// realSandbox is the command sandbox of this machine, or the test is skipped.
func realSandbox(t *testing.T) *sandbox.Sandbox {
	t.Helper()
	was, err := sandbox.HostKernel().Dumpable()
	if err != nil {
		t.Fatal(err)
	}
	if err := sandbox.Harden(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sandbox.SetDumpable(was) })
	home, _ := os.UserHomeDir()
	node := sandbox.NodeFiles{DB: filepath.Join(t.TempDir(), "node.db")}
	s := sandbox.New("/proc/self/exe", sandbox.Policy{
		Deny: func() []string { return sandbox.DenyList(home, "/", node, nil, nil) },
		Env:  []string{"LANG=C.UTF-8", "HOME=" + home},
	}, sandbox.HostKernel(), time.Now())
	if ok, why := s.Ready(); !ok {
		t.Skipf("the command sandbox is not ready here: %s", why)
	}
	return s
}

// rustConfig finds cargo the way the node's command line does, pins the
// toolchain rustc names from "/", and builds in a root of the test's own
// under the user's cache folder, whose parents pass the real check; or
// skips.
func rustConfig(t *testing.T) LocalConfig {
	t.Helper()
	home, _ := os.UserHomeDir()
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		cargo = filepath.Join(home, ".cargo", "bin", "cargo")
		if _, err := os.Stat(cargo); err != nil {
			t.Skip("cargo is not installed (rustup, then `rustup target add wasm32-wasip1`)")
		}
	}
	cfg := DefaultLocalConfig()
	cfg.Cargo, cfg.Rustup = cargo, os.Getenv("RUSTUP_HOME")
	if cfg.Rustup == "" {
		cfg.Rustup = filepath.Join(home, ".rustup")
	}
	cmd := exec.CommandContext(t.Context(), filepath.Join(filepath.Dir(cargo), "rustc"), "--print", "sysroot")
	cmd.Dir = "/"
	out, err := cmd.Output()
	if err != nil {
		t.Skipf("rustc does not name its toolchain: %v", err)
	}
	cfg.Toolchain = filepath.Base(strings.TrimSpace(string(out)))
	cfg.TempRoot = testRoot(t)
	return cfg
}

// testRoot is a fresh 0700 build root under the user's cache folder,
// removed after the test; it skips where there is no cache folder.
func testRoot(t *testing.T) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("no cache folder: %v", err)
	}
	base := filepath.Join(cache, "ganglion", "builds-test")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(base, "t-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

// rustBuilder is the local builder on this machine's sandbox and Rust, or
// the test is skipped.
func rustBuilder(t *testing.T) *Local {
	t.Helper()
	b := NewLocal(realSandbox(t), rustConfig(t))
	can, err := b.Can(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if can[Rust] == "" {
		t.Skip("the wasm32-wasip1 target is not installed (`rustup target add wasm32-wasip1`)")
	}
	t.Logf("Can: %v, toolchain %s, root %s", can, b.cfg.Toolchain, b.cfg.TempRoot)
	return b
}

// leftNothing fails the test when the build left an entry in its root.
func leftNothing(t *testing.T, b *Local) {
	t.Helper()
	if left, _ := os.ReadDir(b.cfg.TempRoot); len(left) != 0 {
		t.Fatalf("the build left %d entries in its root", len(left))
	}
}

// buildAndCheck builds src with b, checks the module loads and is
// healthy, and returns the log.
func buildAndCheck(t *testing.T, b *Local, src Source) Log {
	t.Helper()
	art, log, err := b.Build(t.Context(), src)
	if err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	t.Logf("built %d bytes, sha %s\n%s", len(art.Wasm), art.SHA256, log)
	leftNothing(t, b)
	m, err := runner.ParseManifest(art.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	mod, err := runner.Load(t.Context(), art.Wasm, m, runner.Deps{State: runner.NewMemState()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mod.Close(context.Background()) })
	if err := mod.Health(t.Context()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	return log
}

func TestARealRustModuleBuildsInTheSandboxAndRuns(t *testing.T) {
	buildAndCheck(t, rustBuilder(t), Source{Name: "hello", Dir: "testdata/hello"})
}

// A crate with a dependency from crates.io builds: cargo fetches and
// unpacks it in the sandbox, setting 0644 on what it unpacks, which the
// confined filter allows (ADR-0064). It skips when crates.io is out of reach.
func TestARealRustModuleWithADependencyBuilds(t *testing.T) {
	b := rustBuilder(t)
	art, log, err := b.Build(t.Context(), Source{Name: "withdep", Dir: "testdata/withdep"})
	if err != nil && (strings.Contains(string(log), "failed to download") || strings.Contains(string(log), "Couldn't resolve host") ||
		strings.Contains(string(log), "failed to query replaced source")) {
		t.Skipf("crates.io is out of reach:\n%s", log)
	}
	if err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	t.Logf("built %d bytes\n%s", len(art.Wasm), log)
	if !strings.Contains(string(log), "Compiling itoa") {
		t.Fatalf("the dependency was not built:\n%s", log)
	}
	leftNothing(t, b)
}

// ADR-0064, abuse test: a rust-toolchain.toml that names a toolchain by
// path, here in the source where the parents check does not look, does not
// run that toolchain: builds are pinned with RUSTUP_TOOLCHAIN.
func TestABuildIsPinnedToTheNodesToolchain(t *testing.T) {
	b := rustBuilder(t)
	planted := t.TempDir()
	if err := os.Mkdir(filepath.Join(planted, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"cargo", "rustc"} {
		script := "#!/bin/sh\necho PLANTED-" + tool + "-RAN >&2\nexit 1\n"
		if err := os.WriteFile(filepath.Join(planted, "bin", tool), []byte(script), 0o700); err != nil { //nolint:gosec // the planted toolchain
			t.Fatal(err)
		}
	}
	src := filepath.Join(t.TempDir(), "hello")
	if err := os.CopyFS(src, os.DirFS("testdata/hello")); err != nil {
		t.Fatal(err)
	}
	pin := "[toolchain]\npath = \"" + planted + "\"\n"
	if err := os.WriteFile(filepath.Join(src, "rust-toolchain.toml"), []byte(pin), 0o600); err != nil {
		t.Fatal(err)
	}
	log := buildAndCheck(t, b, Source{Name: "hello", Dir: src})
	if strings.Contains(string(log), "PLANTED") {
		t.Fatalf("the planted toolchain ran:\n%s", log)
	}
}

// escapeSource is testdata/escape copied to a folder of the test's, with the
// steps its build script takes.
func escapeSource(t *testing.T, steps ...string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "escape")
	if err := os.CopyFS(dir, os.DirFS("testdata/escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "escape.txt"), []byte(strings.Join(steps, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// sleeping are the pids of the live `sleep <secs>` processes.
func sleeping(secs string) []int {
	want := "sleep\x00" + secs + "\x00"
	entries, _ := os.ReadDir("/proc")
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline"); err == nil && strings.HasSuffix(string(b), want) {
			out = append(out, pid)
		}
	}
	return out
}

// ADR-0064, abuse test: a build script writes only beneath the build's own
// folder (not another folder of the user's, not a .cargo/config.toml in a
// parent of the work folder that every later build would read), and what it
// detaches with setsid does not outlive Build.
func TestABuildWritesOnlyItsFolderAndLeavesNoProcess(t *testing.T) {
	b := rustBuilder(t)
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid here")
	}
	outside := filepath.Join(t.TempDir(), "escaped")
	poison := filepath.Join(filepath.Dir(b.cfg.TempRoot), ".cargo", "config.toml") // a parent of every later build's folder
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(poison)) })
	secs := fmt.Sprintf("6%d.%d", os.Getpid()%10, time.Now().UnixNano()%1_000_000)
	t.Cleanup(func() {
		for _, pid := range sleeping(secs) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	src := escapeSource(t, "write "+outside, "write "+poison, "write-tmp inside", "detach "+secs)
	// A .env in the source is denied, which splits the build's folder: the
	// build must still have the folders it writes in.
	if err := os.WriteFile(filepath.Join(src, ".env"), []byte("TOKEN=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, log, err := b.Build(t.Context(), Source{Name: "escape", Dir: src})
	if err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	t.Logf("the build's log:\n%s", log)
	for _, p := range []string{outside, poison} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the build wrote %s, outside its folder: %v", p, err)
		}
	}
	if !strings.Contains(string(log), "/inside") || !strings.Contains(string(log), "wrote ") {
		t.Errorf("the build could not write its own folder:\n%s", log)
	}
	if !strings.Contains(string(log), "detached sleep "+secs) {
		t.Fatalf("the build script did not start its detached process:\n%s", log)
	}
	if pids := sleeping(secs); len(pids) != 0 {
		t.Fatalf("the detached sleep %v outlived the build", pids)
	}
	leftNothing(t, b)
}
