package settings_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/schemas"
	"github.com/igorrius/ganglion/settings"
)

// The schema in schemas/node.schema.json must describe exactly what Parse
// reads (ADR-0023).
func TestSchemaMatchesTheLoader(t *testing.T) {
	schema, err := schemas.Get("node.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for pointer, v := range map[string]any{
		"":                 settings.File{},
		"/$defs/node":      settings.Node{},
		"/$defs/log":       settings.Log{},
		"/$defs/metrics":   settings.Metrics{},
		"/$defs/telegram":  settings.Telegram{},
		"/$defs/reactions": settings.Reactions{},
		"/$defs/agent":     settings.Agent{},
		"/$defs/limits":    settings.Limits{},
		"/$defs/search":    settings.Search{},
		"/$defs/sandbox":   settings.Sandbox{},
		"/$defs/models":    settings.Models{},
		"/$defs/provider":  settings.Provider{},
		"/$defs/model":     settings.ModelDef{},
		"/$defs/choice":    settings.Choice{},
		"/$defs/module":    settings.Module{},
		"/$defs/memory":    settings.Memory{},
		"/$defs/review":    settings.Review{},
	} {
		o, err := schemas.At(schema, pointer)
		if err != nil {
			t.Fatalf("%q: %v", pointer, err)
		}
		if want := schemas.YAMLFields(v); !slices.Equal(o.Properties, want) {
			t.Errorf("%q: schema properties %v, loader fields %v", pointer, o.Properties, want)
		}
		if o.AdditionalProperties {
			t.Errorf("%q: additionalProperties must be false: the loader is strict", pointer)
		}
	}
	purposes, err := schemas.At(schema, "/$defs/purposes")
	if err != nil {
		t.Fatal(err)
	}
	want := make([]string, len(router.Purposes))
	for i, p := range router.Purposes {
		want[i] = string(p)
	}
	slices.Sort(want)
	if !slices.Equal(purposes.Properties, want) {
		t.Errorf("purposes %v, router.Purposes %v", purposes.Properties, want)
	}
	if purposes.AdditionalProperties {
		t.Error("purposes: additionalProperties must be false")
	}
}

// Every enum of the schema is the loader's closed set: each value loads, and
// a value outside the schema's set does not.
func TestSchemaEnumsMatchTheLoader(t *testing.T) {
	schema, err := schemas.Get("node.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	providers := "models: {providers: [{name: c, type: claude-cli}], models: [{provider: c, name: m}], purposes: {chat: [{model: m%s}]}}\n"
	for _, tc := range []struct {
		pointer, key, doc string
		want              []string
	}{
		{"/$defs/log", "level", "log: {level: %s}\n", []string{"debug", "info", "warn", "error"}},
		{"/$defs/telegram", "reply", "telegram: {reply: %s}\n", []string{"auto", "always", "off"}},
		{"/$defs/search", "mode", "agent: {search: {mode: %s}}\n", []string{"auto", "off"}},
		{"/$defs/provider", "type", "models: {providers: [{name: p, type: %s, base_url: 'http://localhost:1'}], models: [{provider: p, name: m}], purposes: {chat: [{model: m}]}}\n",
			[]string{settings.TypeOpenAI, settings.TypeClaudeCLI}},
		{"/$defs/choice", "effort", providers, []string{
			string(router.EffortLow), string(router.EffortMedium), string(router.EffortHigh), string(router.EffortXHigh), string(router.EffortMax)}},
	} {
		o, err := schemas.At(schema, tc.pointer)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(tc.want)
		if got := o.Enums[tc.key]; !slices.Equal(got, tc.want) {
			t.Errorf("%s %s: schema enum %v, loader set %v", tc.pointer, tc.key, got, tc.want)
		}
		doc := func(v string) string {
			if tc.key == "effort" {
				return fmt.Sprintf(tc.doc, ", effort: "+v)
			}
			return fmt.Sprintf(tc.doc, v)
		}
		for _, v := range tc.want {
			if _, err := settings.Parse([]byte(doc(v))); err != nil {
				t.Errorf("%s %s: %q is in the schema but the loader refuses it: %v", tc.pointer, tc.key, v, err)
			}
		}
		if _, err := settings.Parse([]byte(doc("bogus"))); err == nil {
			t.Errorf("%s %s: the loader accepts a value outside the enum", tc.pointer, tc.key)
		}
	}
}

func TestEveryPropertyIsDescribed(t *testing.T) {
	b, err := schemas.Get("node.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(b, &root); err != nil {
		t.Fatal(err)
	}
	var walk func(pointer string, node any)
	walk = func(pointer string, node any) {
		switch n := node.(type) {
		case map[string]any:
			if props, ok := n["properties"].(map[string]any); ok {
				for name, p := range props {
					if pm, ok := p.(map[string]any); !ok || strings.TrimSpace(fmt.Sprint(pm["description"])) == "" || pm["description"] == nil {
						t.Errorf("%s/properties/%s has no description", pointer, name)
					}
				}
			}
			for k, v := range n {
				walk(pointer+"/"+k, v)
			}
		case []any:
			for i, v := range n {
				walk(fmt.Sprintf("%s/%d", pointer, i), v)
			}
		}
	}
	walk("", root)
	defs, _ := root["$defs"].(map[string]any)
	for name, d := range defs {
		if dm, _ := d.(map[string]any); dm["type"] == "object" && dm["properties"] == nil {
			t.Errorf("/$defs/%s: an object with no properties", name)
		}
	}
}

func TestExamplesLoad(t *testing.T) {
	// The operator's template sits at the root, beside .env.example; the
	// test tools' files sit in examples/. Each names the schema relative to
	// itself.
	header := map[string]string{
		"../ganglion.example.yaml":             "# yaml-language-server: $schema=schemas/node.schema.json",
		"../examples/ganglion.lab.yaml":        "# yaml-language-server: $schema=../schemas/node.schema.json",
		"../examples/ganglion.haiku-only.yaml": "# yaml-language-server: $schema=../schemas/node.schema.json",
		"../examples/ganglion.echo.yaml":       "# yaml-language-server: $schema=../schemas/node.schema.json",
	}
	for p, want := range header {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		f, err := settings.Parse(src)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		// The echo bot runs without a model.
		if f.Models == nil && !strings.HasSuffix(p, "ganglion.echo.yaml") {
			t.Errorf("%s: no models section", p)
		}
		if first, _, _ := strings.Cut(string(src), "\n"); first != want {
			t.Errorf("%s: first line %q", p, first)
		}
	}
}

// R-CFG-4: every default the schema names is the loader's: the file that
// sets a key to its schema default loads as settings.Default().
func TestSchemaDefaultsMatchTheLoader(t *testing.T) {
	b, err := schemas.Get("node.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var root map[string]any
	if err := dec.Decode(&root); err != nil {
		t.Fatal(err)
	}
	defs, _ := root["$defs"].(map[string]any)
	found := 0
	schemaDefaults(defs, root, nil, func(path []string, v any) {
		found++
		f, err := settings.Parse([]byte(yamlAt(path, v)))
		if err != nil {
			t.Errorf("%s: default %v does not load: %v", strings.Join(path, "."), v, err)
		} else if !reflect.DeepEqual(f, settings.Default()) {
			t.Errorf("%s: schema default %v differs from settings.Default()", strings.Join(path, "."), v)
		}
	})
	if found < 15 {
		t.Fatalf("found %d defaults; the walk lost its way", found)
	}
}

// schemaDefaults calls visit for every property under node that has a
// default, following $ref and oneOf into objects but not into arrays or
// maps: their items have no place in settings.Default().
func schemaDefaults(defs, node map[string]any, path []string, visit func([]string, any)) {
	props, _ := node["properties"].(map[string]any)
	for name, p := range props {
		pm, _ := p.(map[string]any)
		at := append(slices.Clone(path), name)
		if d, ok := pm["default"]; ok {
			visit(at, d)
		}
		refs := []any{pm}
		if alts, ok := pm["oneOf"].([]any); ok {
			refs = append(refs, alts...)
		}
		for _, r := range refs {
			rm, _ := r.(map[string]any)
			if ref, ok := rm["$ref"].(string); ok {
				def, _ := defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
				schemaDefaults(defs, def, at, visit)
			}
		}
	}
}

// yamlAt is a file that sets the key at path to v.
func yamlAt(path []string, v any) string {
	var b strings.Builder
	for i, k := range path {
		b.WriteString(strings.Repeat("  ", i) + k + ":")
		if i < len(path)-1 {
			b.WriteString("\n")
		}
	}
	switch x := v.(type) {
	case string:
		b.WriteString(" " + strconv.Quote(x))
	default:
		fmt.Fprintf(&b, " %v", x)
	}
	return b.String() + "\n"
}
