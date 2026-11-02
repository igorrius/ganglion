package rules

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/igorrius/ganglion/router"
)

// Provider types.
const (
	TypeOpenAI    = "openai"     // Chat Completions: OpenAI, OpenRouter, Ollama, …
	TypeClaudeCLI = "claude-cli" // an unmodified claude -p (ADR-0021)
)

// ProviderDef is a provider in the catalogue. Keys are never in the file:
// KeyEnv names the variable (or <KeyEnv>_FILE) that holds the key.
type ProviderDef struct {
	Name    string `yaml:"name"`
	Type    string `yaml:"type"`
	BaseURL string `yaml:"base_url"`
	KeyEnv  string `yaml:"key_env"`
	Command string `yaml:"command"` // claude-cli: the binary, default "claude"
}

// ModelDef is a model in the catalogue.
type ModelDef struct {
	Provider  string  `yaml:"provider"`
	Name      string  `yaml:"name"`
	Tier      string  `yaml:"tier"`
	Tools     bool    `yaml:"tools"`
	Vision    bool    `yaml:"vision"`
	Local     bool    `yaml:"local"`
	Context   int     `yaml:"context"`
	PriceIn   float64 `yaml:"price_in"`  // USD per million tokens
	PriceOut  float64 `yaml:"price_out"` // USD per million tokens
	LatencyMS int     `yaml:"latency_ms"`
}

// Catalog is the models file: which providers a node uses and their models
// (R-PRV-5).
type Catalog struct {
	Providers []ProviderDef `yaml:"providers"`
	Models    []ModelDef    `yaml:"models"`
}

func isLocalURL(u string) bool {
	return strings.HasPrefix(u, "http://localhost") || strings.HasPrefix(u, "http://127.0.0.1")
}

// LoadCatalog reads and checks a catalogue. Unknown fields are errors, so
// a typo does not silently change routing.
func LoadCatalog(r io.Reader) (Catalog, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return Catalog{}, err
	}
	var c Catalog
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && err != io.EOF {
		return Catalog{}, fmt.Errorf("models: %w", err)
	}

	types := map[string]string{}
	for i, p := range c.Providers {
		at := fmt.Sprintf("models: providers[%d] %q", i, p.Name)
		if p.Type == "" {
			c.Providers[i].Type, p.Type = TypeOpenAI, TypeOpenAI
		}
		switch {
		case p.Name == "":
			return Catalog{}, fmt.Errorf("%s: name is empty", at)
		case types[p.Name] != "":
			return Catalog{}, fmt.Errorf("%s: duplicate provider", at)
		case p.Type != TypeOpenAI && p.Type != TypeClaudeCLI:
			return Catalog{}, fmt.Errorf("%s: type %q is not openai or claude-cli", at, p.Type)
		case p.Type == TypeOpenAI && p.BaseURL == "":
			return Catalog{}, fmt.Errorf("%s: base_url is empty", at)
		case p.Type == TypeOpenAI && p.KeyEnv == "" && !isLocalURL(p.BaseURL):
			return Catalog{}, fmt.Errorf("%s: key_env is empty (only a localhost base_url may go without a key)", at)
		}
		types[p.Name] = p.Type
	}
	if len(c.Models) == 0 {
		return Catalog{}, fmt.Errorf("models: no models: list at least one under models")
	}
	seen := map[string]bool{}
	for i, m := range c.Models {
		at := fmt.Sprintf("models: models[%d] %q", i, m.Name)
		typ := types[m.Provider]
		switch {
		case typ == "":
			return Catalog{}, fmt.Errorf("%s: provider %q is not in providers", at, m.Provider)
		case m.Name == "":
			return Catalog{}, fmt.Errorf("%s: name is empty", at)
		case m.Tier != "cheap" && m.Tier != "smart":
			return Catalog{}, fmt.Errorf("%s: tier %q is not cheap or smart", at, m.Tier)
		case seen[m.Provider+"/"+m.Name]:
			return Catalog{}, fmt.Errorf("%s: duplicate model %s/%s", at, m.Provider, m.Name)
		case typ == TypeClaudeCLI && m.Tools:
			return Catalog{}, fmt.Errorf("%s: tools: true is not supported by claude-cli yet (M3b)", at)
		}
		seen[m.Provider+"/"+m.Name] = true
	}
	return c, nil
}

// RouterModels converts the catalogue's models for rules.New.
func (c Catalog) RouterModels() []router.Model {
	out := make([]router.Model, 0, len(c.Models))
	for _, m := range c.Models {
		out = append(out, router.Model{Provider: m.Provider, Name: m.Name, Tier: m.Tier, Caps: router.ModelCaps{
			Tools: m.Tools, Vision: m.Vision, Local: m.Local, ContextTokens: m.Context,
			PriceInMTok: m.PriceIn, PriceOutMTok: m.PriceOut, Latency: time.Duration(m.LatencyMS) * time.Millisecond,
		}})
	}
	return out
}
