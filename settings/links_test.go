package settings_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/settings"
)

func linksFile(t *testing.T, src string) settings.LinksFile {
	t.Helper()
	path := filepath.Join(t.TempDir(), settings.FileName)
	if src != "" {
		if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return settings.LinksFile{Path: path}
}

func TestLinksAreLoadedAndTheFormIsChecked(t *testing.T) {
	good := "links:\n  - { from: a-b.reading, to: c.reading }\n"
	if f, err := settings.Parse([]byte(good)); err != nil || len(f.Links) != 1 || f.Links[0].To != "c.reading" {
		t.Fatalf("links %+v, err %v", f.Links, err)
	}
	for name, src := range map[string]string{
		"no port":    "links:\n  - { from: a, to: b.x }\n",
		"full name":  "links:\n  - { from: local/a.x, to: b.x }\n",
		"upper case": "links:\n  - { from: a.X, to: b.x }\n",
		"long port":  "links:\n  - { from: a." + strings.Repeat("x", 33) + ", to: b.x }\n",
		"twice":      "links:\n  - { from: a.x, to: b.y }\n  - { from: a.x, to: b.y }\n",
		"unknown":    "links:\n  - { from: a.x, to: b.y, via: z }\n",
	} {
		if _, err := settings.Parse([]byte(src)); err == nil {
			t.Errorf("%s: the file was accepted", name)
		}
	}
}

func TestAddAndRemoveLinkKeepTheRestOfTheFile(t *testing.T) {
	f := linksFile(t, "# the node\nmodules:\n  - name: local/a # the reader\n")
	l := settings.Link{From: "a.x", To: "b.y"}
	for range 2 { // adding twice writes one line
		if err := f.AddLink(l); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.Links()
	if err != nil || len(got) != 1 || got[0] != l {
		t.Fatalf("links %+v, err %v", got, err)
	}
	out, _ := os.ReadFile(f.Path)
	for _, want := range []string{"# the node", "# the reader", "{from: a.x, to: b.y}"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the file lacks %q:\n%s", want, out)
		}
	}
	if err := f.RemoveLink(l); err != nil {
		t.Fatal(err)
	}
	out, _ = os.ReadFile(f.Path)
	if strings.Contains(string(out), "links") || !strings.Contains(string(out), "# the reader") {
		t.Errorf("after the removal:\n%s", out)
	}
	if err := f.RemoveLink(l); err != nil {
		t.Errorf("removing a link that is not there: %v", err)
	}
}

func TestAddLinkRefusesABadForm(t *testing.T) {
	f := linksFile(t, "")
	if err := f.AddLink(settings.Link{From: "a", To: "b.y"}); err == nil {
		t.Fatal("a link with no port was written")
	}
	if _, err := os.Stat(f.Path); err == nil {
		t.Error("a file was made for a refused link")
	}
}

func TestRemoveModuleDropsItsLinks(t *testing.T) {
	src := "modules:\n  - name: local/a\n  - name: local/b\n  - name: local/c\nlinks:\n" +
		"  - { from: a.x, to: b.y }\n  - { from: c.x, to: b.z }\n  - { from: c.x, to: a.z }\n"
	f := linksFile(t, src)
	if err := settings.RemoveModule(f.Path, "local/b"); err != nil {
		t.Fatal(err)
	}
	got, err := f.Links()
	if err != nil || len(got) != 1 || got[0] != (settings.Link{From: "c.x", To: "a.z"}) {
		t.Fatalf("links %+v, err %v", got, err)
	}
	if err := settings.RemoveModule(f.Path, "local/a"); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(f.Path)
	if strings.Contains(string(out), "links") {
		t.Errorf("an empty links section stayed:\n%s", out)
	}
}

func TestLinkTouches(t *testing.T) {
	l := settings.Link{From: "a.x", To: "b.y"}
	for module, want := range map[string]bool{"a": true, "local/b": true, "c": false, "local/ab": false} {
		if got := l.Touches(module); got != want {
			t.Errorf("Touches(%q) = %v", module, got)
		}
	}
}
