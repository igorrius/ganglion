package settings_test

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/settings"
)

const k7 = "telegram:-1001/45"

func destFile(t *testing.T, src string) settings.DestinationsFile {
	t.Helper()
	path := filepath.Join(t.TempDir(), settings.FileName)
	if src != "" {
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return settings.DestinationsFile{Path: path}
}

func notifyTo(t *testing.T, f settings.DestinationsFile, module string) []string {
	t.Helper()
	file, err := settings.LoadOptional(f.Path, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range file.Modules {
		if m.Name == module {
			return m.NotifyTo
		}
	}
	return nil
}

func TestSetDestinationAddsReplacesAndKeepsTheRest(t *testing.T) {
	f := destFile(t, "# the node\nmodules:\n  - name: local/demo # the reader\n    config: {poll: 5m}\n")
	for _, conv := range []string{"telegram:-1001/7", k7} {
		if err := f.SetDestination(settings.Destination{Name: "family-k7", Conv: conv}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.Destinations()
	if err != nil || len(got) != 1 || got[0] != (settings.Destination{Name: "family-k7", Conv: k7}) {
		t.Fatalf("destinations %+v, err %v", got, err)
	}
	out, _ := os.ReadFile(f.Path)
	for _, want := range []string{"# the node", "# the reader", "poll: 5m"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the file lost %q:\n%s", want, out)
		}
	}
}

func TestSetDestinationRefusesABadLine(t *testing.T) {
	f := destFile(t, "")
	for _, d := range []settings.Destination{
		{Name: "owner", Conv: k7},
		{Name: "Family", Conv: k7},
		{Name: "", Conv: k7},
		{Name: "a b", Conv: k7},
		{Name: "k7", Conv: ""},
		{Name: "k7", Conv: "telegram:-1001/45 and more"},
		{Name: "k7", Conv: "-1001"},
	} {
		if err := f.SetDestination(d); err == nil {
			t.Errorf("SetDestination(%+v) = nil, want an error", d)
		}
	}
	if _, err := os.Stat(f.Path); err == nil {
		t.Error("a refused line still wrote the file")
	}
}

func TestSetNotifyToWritesTheListAndKeepsTheLine(t *testing.T) {
	f := destFile(t, "modules:\n  - name: local/alerts\n    config: {low: 30}\n")
	if err := f.SetDestination(settings.Destination{Name: "family-k7", Conv: k7}); err != nil {
		t.Fatal(err)
	}
	if err := f.SetNotifyTo("local/alerts", []string{"family-k7", settings.DestinationOwner}); err != nil {
		t.Fatal(err)
	}
	if got := notifyTo(t, f, "local/alerts"); !slices.Equal(got, []string{"family-k7", "owner"}) {
		t.Fatalf("notify_to %v", got)
	}
	out, _ := os.ReadFile(f.Path)
	if !strings.Contains(string(out), "low: 30") || !strings.Contains(string(out), "notify_to: [family-k7, owner]") {
		t.Fatalf("the line:\n%s", out)
	}
	// A module with no line gets one.
	if err := f.SetNotifyTo("local/new", []string{"family-k7"}); err != nil {
		t.Fatal(err)
	}
	if got := notifyTo(t, f, "local/new"); !slices.Equal(got, []string{"family-k7"}) {
		t.Fatalf("notify_to of a new line %v", got)
	}
	// No names: back to the owner alone.
	if err := f.SetNotifyTo("local/alerts", nil); err != nil {
		t.Fatal(err)
	}
	if got := notifyTo(t, f, "local/alerts"); got != nil {
		t.Fatalf("notify_to after a reset %v", got)
	}
}

func TestSetNotifyToRefusesWhatIsNotThere(t *testing.T) {
	f := destFile(t, "modules:\n  - name: local/alerts\n")
	for name, names := range map[string][]string{
		"an unknown name": {"nowhere"},
		"a name twice":    {"owner", "owner"},
		"too many":        {"owner", "a", "b", "c", "d", "e", "f", "g", "h"},
	} {
		if err := f.SetNotifyTo("local/alerts", names); !errors.Is(err, settings.ErrNoDestination) {
			t.Errorf("%s: err = %v, want ErrNoDestination", name, err)
		}
	}
	out, _ := os.ReadFile(f.Path)
	if strings.Contains(string(out), "notify_to") || strings.Contains(string(out), "destinations") {
		t.Fatalf("a refused change wrote the file:\n%s", out)
	}
	// With owner alone the writer leaves no empty destinations: key behind.
	if err := f.SetNotifyTo("local/alerts", []string{"owner"}); err != nil {
		t.Fatal(err)
	}
	out, _ = os.ReadFile(f.Path)
	if strings.Contains(string(out), "destinations") {
		t.Fatalf("an empty destinations key was left:\n%s", out)
	}
}

func TestRemoveDestinationRefusesWhileAModuleUsesIt(t *testing.T) {
	f := destFile(t, "")
	if err := f.SetDestination(settings.Destination{Name: "family-k7", Conv: k7}); err != nil {
		t.Fatal(err)
	}
	if err := f.SetNotifyTo("local/alerts", []string{"family-k7"}); err != nil {
		t.Fatal(err)
	}
	err := f.RemoveDestination("family-k7")
	if !errors.Is(err, settings.ErrDestinationUsed) || !strings.Contains(err.Error(), "local/alerts") {
		t.Fatalf("err = %v, want ErrDestinationUsed naming the module", err)
	}
	if err := f.SetNotifyTo("local/alerts", nil); err != nil {
		t.Fatal(err)
	}
	if err := f.RemoveDestination("family-k7"); err != nil {
		t.Fatal(err)
	}
	if err := f.RemoveDestination("family-k7"); err != nil {
		t.Fatalf("removing a destination that is not listed: %v", err)
	}
	if got, err := f.Destinations(); err != nil || len(got) != 0 {
		t.Fatalf("destinations %+v, err %v", got, err)
	}
}

func TestBadDestinationsNameThePath(t *testing.T) {
	const one = "destinations:\n  - {name: k7, conv: 'telegram:-1/2'}\n"
	for src, path := range map[string]string{
		"destinations:\n  - {name: owner, conv: 'telegram:-1/2'}\n":                            "destinations[0].name",
		"destinations:\n  - {name: K7, conv: 'telegram:-1/2'}\n":                               "destinations[0].name",
		"destinations:\n  - {name: k7, conv: 'somewhere'}\n":                                   "destinations[0].conv",
		"destinations:\n  - {name: k7}\n":                                                      "destinations[0].conv",
		one + "  - {name: k7, conv: 'telegram:-1/3'}\n":                                        "destinations[1].name",
		one + "modules:\n  - {name: a, notify_to: [nowhere]}\n":                                "modules[0].notify_to",
		one + "modules:\n  - {name: a, notify_to: [k7, k7]}\n":                                 "modules[0].notify_to",
		"modules:\n  - {name: a}\n  - {name: b, notify_to: [owner, k7]}\n":                     "modules[1].notify_to",
		one + "modules:\n  - {name: a, notify_to: [owner,k7,owner,k7,owner,k7,owner,k7,k7]}\n": "modules[0].notify_to",
	} {
		_, err := settings.Parse([]byte(src))
		if err == nil || !strings.Contains(err.Error(), "ganglion.yaml: "+path+":") {
			t.Errorf("%q: err = %v; want path %s", src, err, path)
		}
	}
	if _, err := settings.Parse([]byte(one + "modules:\n  - {name: a, notify_to: [k7, owner]}\n")); err != nil {
		t.Fatalf("a good file: %v", err)
	}
}
