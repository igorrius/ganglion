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
		"agent:\n  limits: {daily_turns: -1}\n": "agent.limits.daily_turns",
		"telegram:\n  reply: sometimes\n":       "telegram.reply",
		"node:\n  tz: Mars/Olympus\n":           "node.tz",
		"node:\n  memory_limit: 10MB\n":         "node.memory_limit",
		"log:\n  level: loud\n":                 "log.level",
		"telegram:\n  admin: bob\n":             "telegram.admin",
		"agent:\n  search: {mode: maybe}\n":     "agent.search.mode",
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
	f, err := settings.Load("ganglion.yaml", read)
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
