package settings_test

import (
	"strings"
	"testing"

	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/settings"
)

const models = `
models:
  providers:
    - {name: claude, type: claude-cli}
    - {name: or, type: openai, base_url: https://openrouter.ai/api/v1, key: env:OR_KEY}
  models:
    - {id: sonnet, provider: claude, name: sonnet, tools: true, context: 200000}
    - {id: haiku, provider: claude, name: haiku, tools: true, context: 200000}
    - {id: flash, provider: or, name: deepseek/deepseek-v4.1-flash, tools: true, context: 1048576, price_in: 0.045, price_out: 0.5}
  purposes:
    chat:  [{model: sonnet, effort: medium}, {model: haiku}]
    check: [{model: haiku, effort: low}, {model: sonnet, effort: low}]
`

func TestPurposesAndDefaults(t *testing.T) {
	f, err := settings.Parse([]byte(models))
	if err != nil {
		t.Fatal(err)
	}
	p := f.Models.RouterPurposes()
	if got := p[router.PurposeChat]; len(got) != 2 || got[0] != (router.Choice{Model: "sonnet", Effort: router.EffortMedium}) {
		t.Fatalf("chat = %+v", got)
	}
	if got := p[router.PurposeCompact]; len(got) != 2 || got[0].Model != "sonnet" {
		t.Fatalf("compact falls back to chat: %+v", got)
	}
	if _, ok := p[router.PurposeCode]; ok {
		t.Fatal("code has no default")
	}
}

func TestModelsErrors(t *testing.T) {
	for name, c := range map[string]struct{ src, want string }{
		"unknown purpose":  {strings.Replace(models, "check:", "think:", 1), `models.purposes: unknown purpose "think"`},
		"unknown model":    {strings.Replace(models, "{model: haiku}]", "{model: opus}]", 1), `models.purposes.chat[1].model: no model "opus"`},
		"bad effort":       {strings.Replace(models, "effort: medium", "effort: huge", 1), "models.purposes.chat[0].effort"},
		"effort too high":  {strings.Replace(models, "chat:  [{model: sonnet, effort: medium}", "chat:  [{model: flash, effort: max}", 1), `models.purposes.chat[0].effort: provider "or" (openai) takes low, medium or high`},
		"no chat":          {strings.Replace(models, "chat:  [{model: sonnet, effort: medium}, {model: haiku}]\n", "", 1), "models.purposes.chat: required"},
		"literal key":      {strings.Replace(models, "key: env:OR_KEY", "key: sk-123", 1), "models.providers[1].key"},
		"duplicate id":     {strings.Replace(models, "{id: haiku,", "{id: sonnet,", 1), `models.models[1].id: "sonnet" twice`},
		"openai needs key": {strings.Replace(models, ", key: env:OR_KEY", "", 1), "models.providers[1].key: required"},
	} {
		_, err := settings.Parse([]byte(c.src))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v; want %q", name, err, c.want)
		}
	}
}

func TestIDDefaultsToName(t *testing.T) {
	f, err := settings.Parse([]byte("models:\n  providers: [{name: c, type: claude-cli}]\n  models: [{provider: c, name: haiku}]\n  purposes: {chat: [{model: haiku}]}\n"))
	if err != nil || f.Models.RouterModels()[0].ID != "haiku" {
		t.Fatalf("got %+v, %v", f.Models, err)
	}
}

func TestModelsErrorsNeverRepeatAValue(t *testing.T) {
	for _, src := range []string{
		strings.Replace(models, "key: env:OR_KEY", "key: sk-hunter2", 1),
		strings.Replace(models, "effort: medium", "effort: hunter2", 1),
		strings.Replace(models, "type: claude-cli", "type: hunter2", 1),
	} {
		_, err := settings.Parse([]byte(src))
		if err == nil || strings.Contains(err.Error(), "hunter2") {
			t.Errorf("err = %v; want an error without the value", err)
		}
	}
}

func TestModelsChecks(t *testing.T) {
	for name, c := range map[string]struct{ src, want string }{
		"unknown provider": {strings.Replace(models, "{id: haiku, provider: claude", "{id: haiku, provider: nobody", 1), `models.models[1].provider: no provider "nobody"`},
		"unknown type":     {strings.Replace(models, "type: claude-cli", "type: grpc", 1), "models.providers[0].type: must be openai or claude-cli"},
		"no models":        {"models:\n  providers: [{name: c, type: claude-cli}]\n  purposes: {chat: [{model: x}]}\n", "models.models: required"},
		"duplicate name":   {strings.Replace(models, "{name: or,", "{name: claude,", 1), `models.providers[1].name: "claude" twice`},
		"unknown key":      {strings.Replace(models, "context: 200000}", "context: 200000, tier: smart}", 1), `models.models[0]: unknown key "tier"`},
	} {
		_, err := settings.Parse([]byte(c.src))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v; want %q", name, err, c.want)
		}
	}
}

func TestLocalOpenAIGoesWithoutAKey(t *testing.T) {
	src := "models:\n  providers: [{name: ollama, base_url: http://localhost:11434/v1}]\n  models: [{provider: ollama, name: qwen, local: true}]\n  purposes: {chat: [{model: qwen, effort: high}]}\n"
	f, err := settings.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if f.Models.Providers[0].Type != settings.TypeOpenAI {
		t.Fatalf("an empty type is openai: %+v", f.Models.Providers[0])
	}
}

func TestKeysNameTheUnsetVariable(t *testing.T) {
	f, err := settings.Parse([]byte(models))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Models.Keys(fakeSource{}); err == nil || !strings.Contains(err.Error(), "OR_KEY") || !strings.Contains(err.Error(), "or") {
		t.Fatalf("err = %v", err)
	}
	keys, err := f.Models.Keys(fakeSource{"OR_KEY": "sk-1"})
	if err != nil || keys["or"] != "sk-1" || len(keys) != 1 {
		t.Fatalf("keys = %v, %v", keys, err)
	}
}

func TestRouterModels(t *testing.T) {
	f, err := settings.Parse([]byte(models))
	if err != nil {
		t.Fatal(err)
	}
	got := f.Models.RouterModels()[2]
	if got.ID != "flash" || got.Provider != "or" || got.Name != "deepseek/deepseek-v4.1-flash" || !got.Caps.Tools ||
		got.Caps.ContextTokens != 1048576 || got.Caps.PriceInMTok != 0.045 || got.Caps.PriceOutMTok != 0.5 {
		t.Fatalf("got %+v", got)
	}
}
