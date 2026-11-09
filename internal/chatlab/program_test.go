package chatlab

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/settings"
)

type answering struct {
	usage provider.Usage
	err   error
}

func (answering) Name() string { return "fake" }
func (a answering) Chat(context.Context, provider.ChatRequest) (provider.ChatResponse, error) {
	return provider.ChatResponse{Usage: a.usage}, a.err
}

func TestCounterAddsEveryCall(t *testing.T) {
	var c Counter
	p := c.Wrap(answering{usage: provider.Usage{InputTokens: 10, OutputTokens: 5}})
	for range 2 {
		if _, err := p.Chat(context.Background(), provider.ChatRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	c.Add(1)
	if got := c.Total(); got != 31 {
		t.Fatalf("total %d", got)
	}
	if p.Name() != "fake" {
		t.Fatalf("name %q", p.Name())
	}
}

func TestCounterPassesAFailure(t *testing.T) {
	var c Counter
	boom := errors.New("boom")
	if _, err := c.Wrap(answering{err: boom}).Chat(context.Background(), provider.ChatRequest{}); !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
}

// labModels reads section as the models: section of ganglion.yaml.
func labModels(t *testing.T, section string) settings.Models {
	t.Helper()
	f, err := settings.Parse([]byte("models:\n  " + strings.ReplaceAll(strings.TrimSpace(section), "\n", "\n  ") + "\n"))
	if err != nil {
		t.Fatal(err)
	}
	return *f.Models
}

// keyed is a models section of one local provider whose key is in env.
func keyed(t *testing.T, env string) settings.Models {
	return labModels(t, "providers: [{name: p, type: openai, base_url: 'http://127.0.0.1:1', key: env:"+env+"}]\n"+
		"models: [{provider: p, name: m}]\npurposes: {chat: [{model: m}]}")
}

func TestKeysFor(t *testing.T) {
	t.Setenv("LAB_TEST_KEY", "k1")
	keys, err := KeysFor(keyed(t, "LAB_TEST_KEY"))
	if err != nil || keys["p"] != "k1" {
		t.Fatalf("keys %v, %v", keys, err)
	}
}

func TestKeysForMissing(t *testing.T) {
	if _, err := KeysFor(keyed(t, "LAB_TEST_NONE")); err == nil || !strings.Contains(err.Error(), "LAB_TEST_NONE") {
		t.Fatalf("err %v", err)
	}
}

func TestKeysForRefusesTelegram(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "real")
	if _, err := KeysFor(keyed(t, "TELEGRAM_BOT_TOKEN")); err == nil || strings.Contains(err.Error(), "real") {
		t.Fatalf("err %v", err)
	}
}

func TestScrubEnvironment(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "real")
	t.Setenv("TELEGRAM_TEST_BOT_TOKEN", "real")
	t.Setenv("GANGLION_DB", "bin/live.db")
	t.Setenv("LAB_KEEPS", "yes")
	ScrubEnvironment()
	for _, name := range []string{"TELEGRAM_BOT_TOKEN", "TELEGRAM_TEST_BOT_TOKEN", "GANGLION_DB"} {
		if _, ok := os.LookupEnv(name); ok {
			t.Errorf("%s is still set", name)
		}
	}
	if os.Getenv("LAB_KEEPS") != "yes" {
		t.Error("LAB_KEEPS was removed")
	}
}

func TestMainModel(t *testing.T) {
	both := labModels(t, "providers: [{name: c, type: claude-cli}]\n"+
		"models: [{id: small, provider: c, name: haiku}, {id: big, provider: c, name: sonnet}]\n"+
		"purposes: {chat: [{model: big}, {model: small}], check: [{model: small}]}")
	if got := MainModel(both); got != "sonnet" {
		t.Errorf("the first of chat, by the provider's name: %q", got)
	}
	if got := MainModel(settings.Models{}); got != "" {
		t.Errorf("no models: %q", got)
	}
}
