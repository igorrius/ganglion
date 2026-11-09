package settings

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/secrets"
)

// Models is the file's models: section: the providers, their models, and
// for each purpose the ordered list of models it takes (R-RTR-6, ADR-0060).
type Models struct {
	// Providers are the model providers the node calls.
	Providers []Provider `yaml:"providers"`
	// Models are the providers' models the purposes may name.
	Models []ModelDef `yaml:"models"`
	// Purposes maps each purpose to its models in the order they are tried.
	Purposes map[router.Purpose][]Choice `yaml:"purposes"`
}

// Provider is a model provider. Its key is never in the file: Key names
// the variable that holds it.
type Provider struct {
	// Name is how the models name the provider.
	Name string `yaml:"name"`
	// Type is openai (Chat Completions: OpenAI, OpenRouter, Ollama, …) or
	// claude-cli (an unmodified claude -p, ADR-0021); empty is openai.
	Type string `yaml:"type"`
	// BaseURL is an openai provider's API address.
	BaseURL string `yaml:"base_url"`
	// Key is the API key, as env:NAME; only a localhost provider goes
	// without one.
	Key SecretRef `yaml:"key"`
	// Command is the claude-cli binary; empty is "claude".
	Command string `yaml:"command"`
}

// ModelDef is one model of a provider.
type ModelDef struct {
	// ID is how the purposes name the model; empty is Name.
	ID string `yaml:"id"`
	// Provider is the provider's name.
	Provider string `yaml:"provider"`
	// Name is the provider's name for the model.
	Name string `yaml:"name"`
	// Tools says the model can call tools.
	Tools bool `yaml:"tools"`
	// Vision says the model reads images.
	Vision bool `yaml:"vision"`
	// Local says the data stays on the owner's hardware.
	Local bool `yaml:"local"`
	// Context is the model's context window in tokens.
	Context int `yaml:"context"`
	// PriceIn is US dollars per million input tokens; 0 for local and
	// subscription models.
	PriceIn float64 `yaml:"price_in"`
	// PriceOut is US dollars per million output tokens.
	PriceOut float64 `yaml:"price_out"`
	// LatencyMS is the model's typical latency in milliseconds.
	LatencyMS int `yaml:"latency_ms"`
}

// Choice is one entry of a purpose's list.
type Choice struct {
	// Model is a model's id.
	Model string `yaml:"model"`
	// Effort is the reasoning effort; empty leaves the provider's default.
	Effort router.Effort `yaml:"effort"`
}

// Provider types.
const (
	TypeOpenAI    = "openai"
	TypeClaudeCLI = "claude-cli"
)

// efforts lists the efforts each provider type takes.
var efforts = map[string][]router.Effort{
	TypeClaudeCLI: {router.EffortLow, router.EffortMedium, router.EffortHigh, router.EffortXHigh, router.EffortMax},
	TypeOpenAI:    {router.EffortLow, router.EffortMedium, router.EffortHigh},
}

// RouterModels converts the models for rules.New.
func (m Models) RouterModels() []router.Model {
	out := make([]router.Model, 0, len(m.Models))
	for _, d := range m.Models {
		out = append(out, router.Model{ID: d.ID, Provider: d.Provider, Name: d.Name, Caps: router.ModelCaps{
			Tools: d.Tools, Vision: d.Vision, Local: d.Local, ContextTokens: d.Context,
			PriceInMTok: d.PriceIn, PriceOutMTok: d.PriceOut, Latency: time.Duration(d.LatencyMS) * time.Millisecond,
		}})
	}
	return out
}

// RouterPurposes returns each purpose's list; check, compact and
// translate fall back to chat's list, code has no fallback (ruling 2).
func (m Models) RouterPurposes() map[router.Purpose][]router.Choice {
	out := map[router.Purpose][]router.Choice{}
	for p, list := range m.Purposes {
		out[p] = choices(list)
	}
	for _, p := range []router.Purpose{router.PurposeCheck, router.PurposeCompact, router.PurposeTranslate} {
		if _, ok := out[p]; !ok {
			out[p] = out[router.PurposeChat]
		}
	}
	return out
}

func choices(list []Choice) []router.Choice {
	out := make([]router.Choice, len(list))
	for i, c := range list {
		out[i] = router.Choice{Model: c.Model, Effort: c.Effort}
	}
	return out
}

// Keys reads each provider's key through src; a provider whose key is set
// in the file but whose variable is empty fails, naming the variable.
func (m Models) Keys(src secrets.Source) (map[string]string, error) {
	keys := map[string]string{}
	for _, p := range m.Providers {
		if p.Key == "" {
			continue
		}
		k, err := p.Key.Resolve(src)
		if err != nil {
			return nil, err
		}
		if k == "" {
			return nil, fmt.Errorf("%s: models.providers: %s (key of provider %s) is not set; set it or %s_FILE", FileName, p.Key.Name(), p.Name, p.Key.Name())
		}
		keys[p.Name] = k
	}
	return keys, nil
}

// check fills the defaults (an empty type is openai, an empty id is the
// name) and checks the providers, the models and the purposes in that
// order; path is the section's own path.
func (m *Models) check(path string) error {
	types, err := m.checkProviders(path + ".providers")
	if err != nil {
		return err
	}
	owner, err := m.checkModels(path+".models", types)
	if err != nil {
		return err
	}
	return m.checkPurposes(path+".purposes", owner)
}

// checkProviders returns each provider's type by name.
func (m *Models) checkProviders(path string) (map[string]string, error) {
	types := map[string]string{}
	for i := range m.Providers {
		p := &m.Providers[i]
		at := fmt.Sprintf("%s[%d]", path, i)
		if p.Type == "" {
			p.Type = TypeOpenAI
		}
		switch {
		case p.Name == "":
			return nil, pathError(at+".name", "required")
		case types[p.Name] != "":
			return nil, pathError(at+".name", fmt.Sprintf("%q twice", p.Name))
		case efforts[p.Type] == nil:
			return nil, pathError(at+".type", "must be openai or claude-cli")
		case p.Type == TypeOpenAI && !validURL(p.BaseURL):
			return nil, pathError(at+".base_url", "required: an http or https address")
		case p.Type == TypeOpenAI && p.Key == "" && !isLocalURL(p.BaseURL):
			return nil, pathError(at+".key", "required: env:NAME of the API key; only a base_url on localhost goes without one")
		}
		types[p.Name] = p.Type
	}
	return types, nil
}

// isLocalURL reports whether an openai provider runs on this machine and
// so may go without a key.
func isLocalURL(u string) bool {
	return strings.HasPrefix(u, "http://localhost") || strings.HasPrefix(u, "http://127.0.0.1")
}

// owner is a model's provider, for the efforts it takes.
type owner struct{ name, typ string }

// checkModels returns each model's provider by id.
func (m *Models) checkModels(path string, types map[string]string) (map[string]owner, error) {
	if len(m.Models) == 0 {
		return nil, pathError(path, "required: list at least one model")
	}
	owners := map[string]owner{}
	for i := range m.Models {
		d := &m.Models[i]
		at := fmt.Sprintf("%s[%d]", path, i)
		if d.ID == "" {
			d.ID = d.Name
		}
		_, dup := owners[d.ID]
		switch {
		case d.Name == "":
			return nil, pathError(at+".name", "required")
		case dup:
			return nil, pathError(at+".id", fmt.Sprintf("%q twice", d.ID))
		case types[d.Provider] == "":
			return nil, pathError(at+".provider", fmt.Sprintf("no provider %q", d.Provider))
		}
		owners[d.ID] = owner{d.Provider, types[d.Provider]}
	}
	return owners, nil
}

// checkPurposes checks that every purpose is known, chat is there, and
// every choice names a model and an effort its provider takes.
func (m *Models) checkPurposes(path string, owners map[string]owner) error {
	names := make([]string, 0, len(m.Purposes))
	for p := range m.Purposes {
		names = append(names, string(p))
	}
	slices.Sort(names)
	for _, p := range names {
		if !slices.Contains(router.Purposes, router.Purpose(p)) {
			return pathError(path, fmt.Sprintf("unknown purpose %q", p))
		}
	}
	if len(m.Purposes[router.PurposeChat]) == 0 {
		return pathError(path+"."+string(router.PurposeChat), "required: list the models of a turn")
	}
	for _, p := range router.Purposes {
		list, ok := m.Purposes[p]
		if !ok {
			continue
		}
		at := path + "." + string(p)
		if len(list) == 0 {
			return pathError(at, "must list at least one model; leave the key out to use chat's list")
		}
		for i, c := range list {
			if err := checkChoice(fmt.Sprintf("%s[%d]", at, i), c, owners); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkChoice checks that c names a model and an effort its provider takes.
func checkChoice(at string, c Choice, owners map[string]owner) error {
	o, ok := owners[c.Model]
	switch {
	case !ok:
		return pathError(at+".model", fmt.Sprintf("no model %q", c.Model))
	case c.Effort == "":
		return nil
	case !slices.Contains(efforts[TypeClaudeCLI], c.Effort):
		return pathError(at+".effort", "must be "+orList(efforts[TypeClaudeCLI]))
	case !slices.Contains(efforts[o.typ], c.Effort):
		return pathError(at+".effort", fmt.Sprintf("provider %q (%s) takes %s", o.name, o.typ, orList(efforts[o.typ])))
	}
	return nil
}

// orList is "a, b or c".
func orList(es []router.Effort) string {
	s := make([]string, len(es))
	for i, e := range es {
		s[i] = string(e)
	}
	return strings.Join(s[:len(s)-1], ", ") + " or " + s[len(s)-1]
}
