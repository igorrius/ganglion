package rules

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/router"
)

const example = `
providers:
  - {name: claude, type: claude-cli}
  - {name: openrouter, type: openai, base_url: https://openrouter.ai/api/v1, key_env: OPENROUTER_API_KEY}
  - {name: ollama, type: openai, base_url: http://localhost:11434/v1}
models:
  - {provider: claude, name: haiku, tier: cheap, context: 200000}
  - {provider: openrouter, name: deepseek/deepseek-chat, tier: cheap, tools: true, context: 64000, price_in: 0.27, price_out: 1.1, latency_ms: 800}
  - {provider: ollama, name: qwen3:8b, tier: cheap, tools: true, local: true, context: 32000}
`

func TestLoadCatalog(t *testing.T) {
	c, err := LoadCatalog(strings.NewReader(example))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) != 3 || c.Providers[0].Type != "claude-cli" || c.Providers[1].KeyEnv != "OPENROUTER_API_KEY" {
		t.Fatalf("providers %+v", c.Providers)
	}
	ms := c.RouterModels()
	want := router.Model{Provider: "openrouter", Name: "deepseek/deepseek-chat", Tier: "cheap",
		Caps: router.ModelCaps{Tools: true, ContextTokens: 64000, PriceInMTok: 0.27, PriceOutMTok: 1.1, Latency: 800 * time.Millisecond}}
	if len(ms) != 3 || ms[1] != want || !ms[2].Caps.Local {
		t.Fatalf("models %+v", ms)
	}
}

func TestCatalogErrors(t *testing.T) {
	for _, tc := range []struct{ yaml, want string }{
		{"providers: [{name: x, type: openai, base_url: http://h}]\nmodels: []", "key_env"},
		{"providers: [{name: x, type: openai}]\nmodels: []", "base_url"},
		{"providers: [{name: x, type: magic}]\nmodels: []", "type"},
		{"providers: [{name: x, type: claude-cli}]\nmodels: [{provider: y, name: m, tier: cheap}]", "provider"},
		{"providers: [{name: x, type: claude-cli}]\nmodels: [{provider: x, name: '', tier: cheap}]", "name"},
		{"providers: [{name: x, type: claude-cli}]\nmodels: [{provider: x, name: m, tier: huge}]", "tier"},
		{"providers: [{name: x, type: claude-cli}]\nmodels: [{provider: x, name: m, tier: cheap}, {provider: x, name: m, tier: smart}]", "duplicate"},
		{"providers: [{name: x, type: claude-cli}, {name: x, type: claude-cli}]\nmodels: []", "duplicate"},
		{"providers: [{name: x, type: claude-cli}]\nmodels: [{provider: x, name: m, tier: cheap, prise_in: 1}]", "prise_in"},
		{"providers: [{name: x, type: claude-cli}]\nmodels: []", "no models"},
	} {
		_, err := LoadCatalog(strings.NewReader(tc.yaml))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v, want an error mentioning %q", tc.yaml, err, tc.want)
		}
	}
}

// ADR-0036: the claude CLI gets the agent's tools over the MCP bridge, so
// its models may say tools: true, and the router then sends tool turns to
// Haiku through the CLI first (price 0), DeepSeek after it.
func TestCatalogAllowsCLITools(t *testing.T) {
	c, err := LoadCatalog(strings.NewReader(`
providers:
  - {name: claude, type: claude-cli}
  - {name: openrouter, type: openai, base_url: https://openrouter.ai/api/v1, key_env: OPENROUTER_API_KEY}
models:
  - {provider: claude, name: haiku, tier: cheap, tools: true, context: 200000}
  - {provider: openrouter, name: deepseek/deepseek-v4.1-flash, tier: cheap, tools: true, context: 1048576, price_in: 0.045, price_out: 0.5}
`))
	if err != nil {
		t.Fatal(err)
	}
	r, err := New(c.RouterModels())
	if err != nil {
		t.Fatal(err)
	}
	cs, err := r.Route(context.Background(), router.TaskReq{Kind: router.TaskMessage, Tools: true})
	if err != nil || len(cs) != 2 || cs[0].Provider != "claude" || cs[0].Model != "haiku" {
		t.Fatalf("tool turn routed to %+v (%v), want claude/haiku first", cs, err)
	}
}
