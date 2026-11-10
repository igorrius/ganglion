package schemas

import (
	"encoding/json"
	"slices"
	"testing"
)

func TestSchemasAreValidJSON(t *testing.T) {
	for _, name := range []string{"node.schema.json", "module-manifest.schema.json", "chatlab-scenario.schema.json"} {
		b, err := Get(name)
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if err := json.Unmarshal(b, &v); err != nil || v["$schema"] == nil || v["$id"] == nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

func TestAt(t *testing.T) {
	b, _ := Get("node.schema.json")
	o, err := At(b, "/$defs/provider")
	if err != nil {
		t.Fatal(err)
	}
	if o.AdditionalProperties || !slices.Contains(o.Properties, "base_url") || !slices.Equal(o.Enums["type"], []string{"claude-cli", "openai"}) ||
		!slices.Equal(o.Required, []string{"name"}) {
		t.Fatalf("model object %+v", o)
	}
	if _, err := At(b, "/$defs/nothing"); err == nil {
		t.Fatal("a missing pointer resolved")
	}
}
