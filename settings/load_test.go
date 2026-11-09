package settings_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/settings"
)

func TestUnknownKeyNamesThePath(t *testing.T) {
	_, err := settings.Parse([]byte("agent:\n  limits:\n    daily_turnz: 3\n"))
	if err == nil || !strings.Contains(err.Error(), "agent.limits") || !strings.Contains(err.Error(), "daily_turnz") {
		t.Fatalf("err = %v", err)
	}
}

func TestBadValuesNameThePath(t *testing.T) {
	for src, path := range map[string]string{
		"agent:\n  limits: {daily_turns: -1}\n":   "agent.limits.daily_turns",
		"telegram:\n  reply: sometimes\n":         "telegram.reply",
		"node:\n  tz: Mars/Olympus\n":             "node.tz",
		"node:\n  memory_limit: 10MB\n":           "node.memory_limit",
		"log:\n  level: loud\n":                   "log.level",
		"telegram:\n  admin: bob\n":               "telegram.admin",
		"agent:\n  search: {mode: maybe}\n":       "agent.search.mode",
		"node:\n  tz: \"\"\n":                     "node.tz",
		"telegram:\n  api_url: 'https://u:p@x'\n": "telegram.api_url",
	} {
		_, err := settings.Parse([]byte(src))
		if err == nil || !strings.Contains(err.Error(), "ganglion.yaml: "+path+":") {
			t.Errorf("%q: err = %v; want path %s", src, err, path)
		}
	}
}

func TestErrorsNeverRepeatAValue(t *testing.T) {
	for _, src := range []string{
		"telegram:\n  rich: hunter2\n",
		"agent:\n  workspace: {a: hunter2}\n",
		"telegram:\n  reply: hunter2\n",
		"hunter2: 1\n",
		"telegram: [hunter2\n",
	} {
		_, err := settings.Parse([]byte(src))
		if err == nil {
			t.Errorf("%q: no error", src)
		} else if strings.Contains(err.Error(), "hunter2") && !strings.Contains(src, "hunter2: 1") {
			t.Errorf("%q: error repeats a value: %v", src, err)
		}
	}
}

func TestMissingFileIsDefault(t *testing.T) {
	read := func(string) ([]byte, error) { return nil, fs.ErrNotExist }
	f, err := settings.LoadOptional("ganglion.yaml", read)
	if err != nil {
		t.Fatal(err)
	}
	if f.Node.TZ != "Europe/Kyiv" || f.Telegram.Token.Name() != "TELEGRAM_BOT_TOKEN" || f.Models != nil {
		t.Fatalf("defaults: %+v", f)
	}
}

func TestEmptyFileIsDefault(t *testing.T) {
	f, err := settings.Parse(nil)
	if err != nil || f.Agent.Limits.DailyTurns != 300 {
		t.Fatalf("got %+v, %v", f.Agent.Limits, err)
	}
}

// R-CFG-1: a path the operator named must exist; a typo must not start a
// node on defaults.
func TestNamedFileMustExist(t *testing.T) {
	read := func(string) ([]byte, error) { return nil, fs.ErrNotExist }
	_, err := settings.Load("/etc/ganglion/gangilon.yaml", read)
	if err == nil || err.Error() != "ganglion.yaml: /etc/ganglion/gangilon.yaml: no such file" {
		t.Fatalf("err = %v", err)
	}
}

func TestNamedFileLoads(t *testing.T) {
	read := func(string) ([]byte, error) { return []byte("node: {tz: UTC}\n"), nil }
	f, err := settings.Load("ganglion.yaml", read)
	if err != nil || f.Node.TZ != "UTC" {
		t.Fatalf("got %+v, %v", f.Node, err)
	}
}

func TestEmptyTZFails(t *testing.T) {
	_, err := settings.Parse([]byte("node:\n  tz: \"\"\n"))
	if err == nil || err.Error() != "ganglion.yaml: node.tz: must name a time zone" {
		t.Fatalf("err = %v", err)
	}
}

func TestAPIURLRefusesCredentials(t *testing.T) {
	_, err := settings.Parse([]byte("telegram:\n  api_url: 'https://bot:hunter2@api.telegram.org'\n"))
	if err == nil || !strings.Contains(err.Error(), "telegram.api_url: must not carry credentials") || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("err = %v", err)
	}
}

// A section whose children are all commented out is null in YAML: it keeps
// its defaults.
func TestEmptySectionKeepsDefaults(t *testing.T) {
	f, err := settings.Parse([]byte("telegram:\n  # token: env:BOT\nsandbox:\n  deny:\nagent:\n  limits:\n"))
	if err != nil {
		t.Fatal(err)
	}
	d := settings.Default()
	if f.Telegram != d.Telegram || f.Sandbox.Deny != nil || f.Agent.Limits != d.Agent.Limits {
		t.Fatalf("got %+v, %+v, %+v", f.Telegram, f.Sandbox, f.Agent.Limits)
	}
}

func TestDuplicateKeysFail(t *testing.T) {
	for src, want := range map[string]string{
		"node: {tz: UTC}\nmodels: {}\nmodels: {}\n": `ganglion.yaml: key "models" twice`,
		"telegram:\n  rich: true\n  rich: false\n":  `ganglion.yaml: telegram: key "rich" twice`,
	} {
		if _, err := settings.Parse([]byte(src)); err == nil || err.Error() != want {
			t.Errorf("%q: err = %v; want %q", src, err, want)
		}
	}
}
