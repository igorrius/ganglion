package settings_test

import (
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"time"

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
	if !reflect.DeepEqual(f.Telegram, d.Telegram) || f.Sandbox.Deny != nil || f.Agent.Limits != d.Agent.Limits {
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

func TestUpdateSection(t *testing.T) {
	if !settings.Default().Update.Check {
		t.Fatal("update.check is on by default")
	}
	for src, want := range map[string]bool{"": true, "update: {check: true}\n": true, "update: {check: false}\n": false,
		"update: {check: off}\n": false, "update:\n  check: on\n": true} {
		f, err := settings.Parse([]byte(src))
		if err != nil || f.Update.Check != want {
			t.Errorf("%q: check %v, %v; want %v", src, f.Update.Check, err, want)
		}
	}
	if _, err := settings.Parse([]byte("update:\n  check: sometimes\n")); err == nil {
		t.Error("a word that is not a bool was accepted")
	}
	if _, err := settings.Parse([]byte("update:\n  every: 1h\n")); err == nil {
		t.Error("an unknown key was accepted")
	}
}

func TestMemorySection(t *testing.T) {
	f, err := settings.Parse([]byte("memory:\n  prompt_budget: 12000\n  review: {enabled: false, quiet: 30m}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Memory.PromptBudget != 12000 || f.Memory.Review.Enabled || time.Duration(f.Memory.Review.Quiet) != 30*time.Minute {
		t.Fatalf("memory %+v", f.Memory)
	}
	d := settings.Default().Memory
	if d.PromptBudget != 24000 || !d.Review.Enabled || time.Duration(d.Review.Quiet) != 10*time.Minute {
		t.Fatalf("defaults %+v", d)
	}
	for src, want := range map[string]string{
		"memory:\n  budget: 1\n":                            `memory: unknown key "budget"`,
		"memory:\n  review: {quiet: off}\n":                 "memory.review.quiet: must be above 0 while the review is enabled",
		"memory:\n  review: {enabled: false, quiet: off}\n": "",
	} {
		_, err := settings.Parse([]byte(src))
		if want == "" {
			if err != nil {
				t.Errorf("%q: %v", src, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v; want %q", src, err, want)
		}
	}
}

// R-CFG-11: the public listener and the webhook mode are checked at the
// start, with the path that is wrong.
func TestWebhookSettings(t *testing.T) {
	for src, path := range map[string]string{
		"http:\n  addr: nowhere\n":                                          "http.addr",
		"http:\n  local_addr: '0.0.0.0:8090'\n":                             "http.local_addr",
		"http:\n  local_addr: '[::]:8090'\n":                                "http.local_addr",
		"http:\n  local_addr: '192.168.1.10:8090'\n":                        "http.local_addr",
		"http:\n  local_addr: 'example.com:8090'\n":                         "http.local_addr",
		"http:\n  local_addr: ':8090'\n":                                    "http.local_addr",
		"http:\n  local_addr: '127.0.0.1:0'\n":                              "http.local_addr",
		"http:\n  local_addr: '127.0.0.5:8090'\n":                           "http.local_addr",
		"http:\n  local_addr: '[::ffff:127.0.0.1]:8090'\n":                  "http.local_addr",
		"http:\n  local_addr: '127.0.0.1'\n":                                "http.local_addr",
		"http:\n  public_url: http://x.example\n":                           "http.public_url",
		"http:\n  public_url: https://x.example/hook\n":                     "http.public_url",
		"http:\n  public_url: https://x.example:9000\n":                     "http.public_url",
		"http:\n  public_url: 'https://u:p@x.example'\n":                    "http.public_url",
		"http:\n  public_url: 'https://x.example?a=1'\n":                    "http.public_url",
		"telegram:\n  updates: push\n":                                      "telegram.updates",
		"telegram:\n  updates: webhook\n":                                   "telegram.updates",
		"telegram:\n  updates: webhook\nhttp:\n  addr: ':8080'\n":           "telegram.updates",
		"telegram:\n  updates: webhook\nhttp:\n  public_url: https://x.y\n": "telegram.updates",
	} {
		_, err := settings.Parse([]byte(src))
		if err == nil || !strings.Contains(err.Error(), "ganglion.yaml: "+path+":") {
			t.Errorf("%q: err = %v; want path %s", src, err, path)
		}
	}
	f, err := settings.Parse([]byte("telegram:\n  updates: webhook\nhttp:\n  addr: ':8080'\n  public_url: https://x.example:8443/\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !f.HTTP.On() || f.Telegram.Updates != settings.UpdatesWebhook {
		t.Errorf("file %+v %+v", f.HTTP, f.Telegram.Updates)
	}
	if d := settings.Default(); d.HTTP.On() || d.Telegram.Updates != settings.UpdatesPolling {
		t.Errorf("the default opens a listener or leaves polling: %+v %q", d.HTTP, d.Telegram.Updates)
	}
}

// R-HTTP-1, R-HTTP-3: the listener of modules' pages is on 127.0.0.1:8090
// unless the file says another loopback address or off.
func TestLocalAddr(t *testing.T) {
	for src, want := range map[string]string{
		"":                           "127.0.0.1:8090",
		"http:\n  addr: ':8080'\n":   "127.0.0.1:8090",
		"http:\n  local_addr: off\n": "off",
		"http:\n  local_addr: 'localhost:9000'\n": "localhost:9000",
		"http:\n  local_addr: '[::1]:8090'\n":     "[::1]:8090",
		"http:\n  local_addr: 'LOCALHOST:8090'\n": "LOCALHOST:8090",
	} {
		f, err := settings.Parse([]byte(src))
		if err != nil || f.HTTP.LocalAddr != want {
			t.Errorf("%q: %q, %v; want %q", src, f.HTTP.LocalAddr, err, want)
		}
	}
}
