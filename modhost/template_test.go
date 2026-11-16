//go:build linux

package modhost_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/builder"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/modtrial"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/sandbox"
)

// TestMain lets the test binary be the sandbox's helper, as the ganglion
// binary is: the reflex template is built in the command sandbox.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == sandbox.HelperArg {
		os.Exit(sandbox.Helper())
	}
	os.Exit(m.Run())
}

const templatesRoot = "../examples/modules"

// producerReportsValue is a producer module, local/producer, whose cron run
// reports its cell value as the operation in op_cron says.
const producerReportsValue = `name: probe
version: 1
triggers:
  - cron: "*/30 * * * *"
capabilities:
  state: true
cells:
  report:
    value: {kind: sensor, type: integer, about: "A reading"}
config:
  op_cron: {type: string, default: '{"op":"cell_report","name":"value","json":12}'}
limits:
  timeout: 5s
`

// The reflex template, built once by the node's own builder, listens to a
// producer's cell: a value below its threshold makes one module.notify
// event and the same value again none. The build fetches crates from crates.io, so it runs only
// with GANGLION_NET_TESTS=1 (make test-net, docs/conventions.md section 9).
func TestTheReflexTemplateNotifiesOnALowValue(t *testing.T) {
	if os.Getenv("GANGLION_NET_TESTS") == "" {
		t.Skip("it fetches crates from crates.io: set GANGLION_NET_TESTS=1 (make test-net)")
	}
	art := buildTemplate(t, "reflex-template")
	reflex := modstore.Active{Module: "local/reflex-template", SHA256: art.SHA256, Wasm: art.Wasm, Manifest: art.Manifest}
	e := newEnv(t, active(t, "local/producer", producerReportsValue), reflex)
	e.start(t)

	e.runCron(t, "local/producer", "slot-1")
	trigs := e.triggers(t)
	if len(trigs) != 1 || trigs[0].Module != "local/reflex-template" {
		t.Fatalf("triggers %+v", trigs)
	}
	if err := e.host.RunTrigger(t.Context(), trigs[0], "01EVTRIGGER1"); err != nil {
		t.Fatal(err)
	}
	got := e.notifies(t)
	if len(got) != 1 || got[0].p.Module != "local/reflex-template" || got[0].p.Text != "producer.value fell below 30: it is 12" {
		t.Fatalf("notifies %+v", got)
	}

	// still low: the episode is said once
	if err := e.host.RunTrigger(t.Context(), trigs[0], "01EVTRIGGER2"); err != nil {
		t.Fatal(err)
	}
	if got := e.notifies(t); len(got) != 1 {
		t.Fatalf("a second message: %+v", got)
	}
}

// Each template's examples.yaml passes on the template built by the node's
// own builder: the trial run a build gets is the one the template ships
// with. Net-gated like the test above.
func TestTheTemplatesPassTheirOwnExamples(t *testing.T) {
	if os.Getenv("GANGLION_NET_TESTS") == "" {
		t.Skip("it fetches crates from crates.io: set GANGLION_NET_TESTS=1 (make test-net)")
	}
	for _, name := range []string{"reflex-template", "reader-template"} {
		t.Run(name, func(t *testing.T) {
			art := buildTemplate(t, name)
			m, err := runner.ParseManifest(art.Manifest)
			if err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(filepath.Join(templatesRoot, name, modtrial.FileName))
			if err != nil {
				t.Fatal(err)
			}
			ex, err := modtrial.Parse(data, m, name)
			if err != nil {
				t.Fatal(err)
			}
			rep, err := modtrial.Run(t.Context(), art.Wasm, m, name, ex, modtrial.Options{Start: time.Now()})
			if err != nil {
				t.Fatal(err)
			}
			if !rep.Passed {
				t.Fatalf("the trial failed: %s", rep.ModelLine())
			}
			if len(rep.Examples) != len(ex.Examples) {
				t.Fatalf("%d of %d examples ran", len(rep.Examples), len(ex.Examples))
			}
		})
	}
}

// buildTemplate builds the template with the node's builder, or skips.
func buildTemplate(t *testing.T, name string) builder.Artifact {
	t.Helper()
	b := builder.NewLocal(realSandbox(t), rustConfig(t))
	can, err := b.Can(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if can[builder.Rust] == "" {
		t.Skip("the wasm32-wasip1 target is not installed (`rustup target add wasm32-wasip1`)")
	}
	art, log, err := b.Build(t.Context(), builder.Source{Name: name, Dir: filepath.Join(templatesRoot, name)})
	if err != nil && offline(string(log)) {
		t.Skipf("crates.io is out of reach:\n%s", log)
	}
	if err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	return art
}

func offline(log string) bool {
	for _, s := range []string{"failed to download", "Couldn't resolve host", "failed to query replaced source"} {
		if strings.Contains(log, s) {
			return true
		}
	}
	return false
}

// realSandbox is the command sandbox of this machine, or the test is
// skipped (as in the builder's tests).
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
// under the user's cache folder; or skips.
func rustConfig(t *testing.T) builder.LocalConfig {
	t.Helper()
	home, _ := os.UserHomeDir()
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		cargo = filepath.Join(home, ".cargo", "bin", "cargo")
		if _, err := os.Stat(cargo); err != nil {
			t.Skip("cargo is not installed (rustup, then `rustup target add wasm32-wasip1`)")
		}
	}
	cfg := builder.DefaultLocalConfig()
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
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Skipf("no cache folder: %v", err)
	}
	base := filepath.Join(cache, "ganglion", "builds-test")
	if err := os.MkdirAll(base, 0o700); err != nil {
		t.Fatal(err)
	}
	if cfg.TempRoot, err = os.MkdirTemp(base, "t-"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(cfg.TempRoot) })
	return cfg
}
