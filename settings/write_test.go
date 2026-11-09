package settings_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/igorrius/ganglion/settings"
)

const twoModules = `# the node
node:
  tz: Europe/Kyiv # home

modules:
  # the solar reader
  - name: local/deye-reader
    config:
      host: 192.168.1.5 # inverter
  - name: local/other
    off: true

log:
  level: debug
`

func writeFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ganglion.yaml")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func loaded(t *testing.T, p string) settings.File {
	t.Helper()
	f, err := settings.Load(p, os.ReadFile)
	if err != nil {
		t.Fatalf("the result does not parse: %v\n%s", err, read(t, p))
	}
	return f
}

func TestSetModuleChangesOnlyItsLine(t *testing.T) {
	p := writeFile(t, twoModules)
	err := settings.SetModule(p, settings.Module{Name: "local/other", Off: false, Config: map[string]any{"interval": 30}})
	if err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	for _, keep := range []string{"# the node", "# home", "# the solar reader", "# inverter", "level: debug", "tz: Europe/Kyiv"} {
		if !strings.Contains(got, keep) {
			t.Fatalf("lost %q:\n%s", keep, got)
		}
	}
	if strings.Contains(got, "off:") {
		t.Fatalf("off stayed:\n%s", got)
	}
	f := loaded(t, p)
	if len(f.Modules) != 2 || f.Modules[0].Name != "local/deye-reader" || f.Modules[1].Name != "local/other" {
		t.Fatalf("modules %+v", f.Modules)
	}
	if f.Modules[1].Config["interval"] != 30 || f.Modules[0].Config["host"] != "192.168.1.5" {
		t.Fatalf("config %+v", f.Modules)
	}
}

func TestSetModuleOffKeepsConfigAndAddsAtTheEnd(t *testing.T) {
	p := writeFile(t, twoModules)
	if err := settings.SetModule(p, settings.Module{Name: "local/deye-reader", Config: map[string]any{"host": "192.168.1.5"}, Off: true}); err != nil {
		t.Fatal(err)
	}
	if err := settings.SetModule(p, settings.Module{Name: "local/third"}); err != nil {
		t.Fatal(err)
	}
	f := loaded(t, p)
	if len(f.Modules) != 3 || !f.Modules[0].Off || f.Modules[2].Name != "local/third" {
		t.Fatalf("modules %+v", f.Modules)
	}
	if !strings.Contains(read(t, p), "# inverter") {
		t.Fatalf("comment lost:\n%s", read(t, p))
	}
}

func TestSetModuleCreatesTheSectionAndTheFile(t *testing.T) {
	p := writeFile(t, "node:\n  tz: Europe/Kyiv\n")
	if err := settings.SetModule(p, settings.Module{Name: "local/a"}); err != nil {
		t.Fatal(err)
	}
	if f := loaded(t, p); len(f.Modules) != 1 || f.Modules[0].Name != "local/a" || f.Node.TZ != "Europe/Kyiv" {
		t.Fatalf("file %+v", f)
	}
	q := filepath.Join(t.TempDir(), "new.yaml")
	if err := settings.SetModule(q, settings.Module{Name: "local/a"}); err != nil {
		t.Fatal(err)
	}
	if f := loaded(t, q); len(f.Modules) != 1 {
		t.Fatalf("file %+v", f)
	}
}

func TestRemoveModule(t *testing.T) {
	p := writeFile(t, twoModules)
	if err := settings.RemoveModule(p, "local/deye-reader"); err != nil {
		t.Fatal(err)
	}
	f := loaded(t, p)
	if len(f.Modules) != 1 || f.Modules[0].Name != "local/other" || f.Log.Level != "debug" {
		t.Fatalf("file %+v", f)
	}
	if err := settings.RemoveModule(p, "local/missing"); err != nil {
		t.Fatalf("removing an absent module: %v", err)
	}
}

func TestFailedWriteLeavesTheOldFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only directory")
	}
	p := writeFile(t, twoModules)
	dir := filepath.Dir(p)
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(dir, 0o700) }()
	if err := settings.SetModule(p, settings.Module{Name: "local/x"}); err == nil {
		t.Fatal("want an error")
	}
	if err := settings.RemoveModule(p, "local/other"); err == nil {
		t.Fatal("want an error")
	}
	if got := read(t, p); got != twoModules {
		t.Fatalf("the file changed:\n%s", got)
	}
}

func TestSetModuleRefusesAFileThatIsNotAMapping(t *testing.T) {
	p := writeFile(t, "- a\n- b\n")
	if err := settings.SetModule(p, settings.Module{Name: "local/x"}); err == nil {
		t.Fatal("want an error")
	}
	if got := read(t, p); got != "- a\n- b\n" {
		t.Fatalf("the file changed: %q", got)
	}
}

func TestConcurrentWritersLoseNoEdit(t *testing.T) {
	p := writeFile(t, "node:\n  tz: Europe/Kyiv\n")
	const n = 16
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			if err := settings.SetModule(p, settings.Module{Name: fmt.Sprintf("local/m%d", i), Off: i%2 == 0}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	f := loaded(t, p)
	if len(f.Modules) != n {
		t.Fatalf("%d modules survived, want %d:\n%s", len(f.Modules), n, read(t, p))
	}
	for _, m := range f.Modules {
		var i int
		if _, err := fmt.Sscanf(m.Name, "local/m%d", &i); err != nil || m.Off != (i%2 == 0) {
			t.Fatalf("module %+v", m)
		}
	}
}
