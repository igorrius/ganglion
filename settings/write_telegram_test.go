package settings_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/settings"
)

func family(chat int64, name string) settings.Group {
	return settings.Group{Chat: chat, Name: name, Role: settings.GroupRoleFamily}
}

func TestSetGroupAddsALineAndKeepsTheRest(t *testing.T) {
	path := filepath.Join(t.TempDir(), settings.FileName)
	src := "# the node\ntelegram:\n  reply: auto # quoted in groups\nmodules:\n  - name: local/deye\n"
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	f := settings.TelegramFile{Path: path}
	if err := f.SetGroup(family(-1001, "Family: the \"best\" #1")); err != nil {
		t.Fatal(err)
	}
	got, err := f.Groups()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != family(-1001, "Family: the \"best\" #1") {
		t.Fatalf("groups = %+v", got)
	}
	out, _ := os.ReadFile(path)
	for _, want := range []string{"# the node", "# quoted in groups", "name: local/deye"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("the file lost %q:\n%s", want, out)
		}
	}
}

func TestSetGroupReplacesTheLineOfTheSameChat(t *testing.T) {
	f := settings.TelegramFile{Path: filepath.Join(t.TempDir(), settings.FileName)}
	for _, name := range []string{"Old", "New"} {
		if err := f.SetGroup(family(-1001, name)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := f.Groups()
	if err != nil || len(got) != 1 || got[0].Name != "New" {
		t.Fatalf("groups = %+v, err = %v", got, err)
	}
}

func TestSetGroupFillsACommentedOutList(t *testing.T) {
	path := filepath.Join(t.TempDir(), settings.FileName)
	if err := os.WriteFile(path, []byte("telegram:\n  groups:\n  #  - chat: -1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := settings.TelegramFile{Path: path}
	if err := f.SetGroup(family(-1001, "A")); err != nil {
		t.Fatal(err)
	}
	if got, err := f.Groups(); err != nil || len(got) != 1 {
		t.Fatalf("groups = %+v, err = %v", got, err)
	}
}

func TestRemoveGroup(t *testing.T) {
	f := settings.TelegramFile{Path: filepath.Join(t.TempDir(), settings.FileName)}
	for _, g := range []settings.Group{family(-1001, "A"), family(-1002, "B")} {
		if err := f.SetGroup(g); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.RemoveGroup(-1001); err != nil {
		t.Fatal(err)
	}
	if err := f.RemoveGroup(-1001); err != nil {
		t.Fatalf("removing a group that is not listed: %v", err)
	}
	got, err := f.Groups()
	if err != nil || len(got) != 1 || got[0].Chat != -1002 {
		t.Fatalf("groups = %+v, err = %v", got, err)
	}
}

func TestSetGroupRefusesABadLine(t *testing.T) {
	f := settings.TelegramFile{Path: filepath.Join(t.TempDir(), settings.FileName)}
	for _, g := range []settings.Group{
		{Chat: 42, Name: "a private chat", Role: settings.GroupRoleFamily},
		{Chat: -1001, Name: "x", Role: "admin"},
		{Chat: -1001, Name: "x"},
	} {
		if err := f.SetGroup(g); err == nil {
			t.Errorf("SetGroup(%+v) = nil, want an error", g)
		}
	}
	if _, err := os.Stat(f.Path); err == nil {
		t.Error("a refused line still wrote the file")
	}
}

func TestBadGroupLinesNameThePath(t *testing.T) {
	for src, path := range map[string]string{
		"telegram:\n  groups:\n    - {chat: 42, role: family}\n":                                 "telegram.groups[0].chat",
		"telegram:\n  groups:\n    - {chat: -1, role: admin}\n":                                  "telegram.groups[0].role",
		"telegram:\n  groups:\n    - {chat: -1}\n":                                               "telegram.groups[0].role",
		"telegram:\n  groups:\n    - {chat: -1, role: family}\n    - {chat: -1, role: family}\n": "telegram.groups[1].chat",
	} {
		_, err := settings.Parse([]byte(src))
		if err == nil || !strings.Contains(err.Error(), "ganglion.yaml: "+path+":") {
			t.Errorf("%q: err = %v; want path %s", src, err, path)
		}
	}
}
