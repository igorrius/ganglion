package rules

import (
	"os"
	"slices"
	"testing"

	"github.com/igorrius/ganglion/schemas"
)

// The schema in schemas/models.schema.json must describe exactly what
// LoadCatalog reads (ADR-0023).
func TestCatalogSchemaMatchesLoader(t *testing.T) {
	b, err := schemas.Get("models.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pointer string
		v       any
	}{
		{"", Catalog{}},
		{"/$defs/provider", ProviderDef{}},
		{"/$defs/model", ModelDef{}},
	} {
		o, err := schemas.At(b, tc.pointer)
		if err != nil {
			t.Fatal(err)
		}
		if want := schemas.YAMLFields(tc.v); !slices.Equal(o.Properties, want) {
			t.Errorf("%q: schema properties %v, loader fields %v", tc.pointer, o.Properties, want)
		}
		if o.AdditionalProperties {
			t.Errorf("%q: the loader rejects unknown fields; the schema must too", tc.pointer)
		}
	}
	prov, _ := schemas.At(b, "/$defs/provider")
	if want := []string{TypeClaudeCLI, TypeOpenAI}; !slices.Equal(prov.Enums["type"], want) {
		t.Errorf("provider types: schema %v, loader %v", prov.Enums["type"], want)
	}
	model, _ := schemas.At(b, "/$defs/model")
	if !slices.Equal(model.Enums["tier"], []string{"cheap", "smart"}) {
		t.Errorf("tiers: schema %v", model.Enums["tier"])
	}
}

func TestExampleCatalogLoads(t *testing.T) {
	f, err := os.Open("../../models.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := LoadCatalog(f); err != nil {
		t.Fatalf("models.example.yaml: %v", err)
	}
}
