package chatlab

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router/rules"
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

func catalog(t *testing.T, body string) rules.Catalog {
	t.Helper()
	c, err := rules.LoadCatalog(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestKeysFor(t *testing.T) {
	t.Setenv("LAB_TEST_KEY", "k1")
	c := catalog(t, "providers:\n  - {name: p, type: openai, base_url: 'http://127.0.0.1:1', key_env: LAB_TEST_KEY}\nmodels:\n  - {provider: p, name: m, tier: smart}\n")
	keys, err := KeysFor(c)
	if err != nil || keys["p"] != "k1" {
		t.Fatalf("keys %v, %v", keys, err)
	}
}

func TestKeysForMissing(t *testing.T) {
	c := catalog(t, "providers:\n  - {name: p, type: openai, base_url: 'http://127.0.0.1:1', key_env: LAB_TEST_NONE}\nmodels:\n  - {provider: p, name: m, tier: smart}\n")
	if _, err := KeysFor(c); err == nil || !strings.Contains(err.Error(), "LAB_TEST_NONE") {
		t.Fatalf("err %v", err)
	}
}

func TestKeysForRefusesTelegram(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "real")
	c := catalog(t, "providers:\n  - {name: p, type: openai, base_url: 'http://127.0.0.1:1', key_env: TELEGRAM_BOT_TOKEN}\nmodels:\n  - {provider: p, name: m, tier: smart}\n")
	if _, err := KeysFor(c); err == nil || strings.Contains(err.Error(), "real") {
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
	both := catalog(t, "providers:\n  - {name: c, type: claude-cli}\nmodels:\n  - {provider: c, name: haiku, tier: cheap}\n  - {provider: c, name: sonnet, tier: smart}\n")
	if got := MainModel(both); got != "sonnet" {
		t.Errorf("both: %q", got)
	}
	cheap := catalog(t, "providers:\n  - {name: c, type: claude-cli}\nmodels:\n  - {provider: c, name: haiku, tier: cheap}\n")
	if got := MainModel(cheap); got != "haiku" {
		t.Errorf("cheap: %q", got)
	}
}
