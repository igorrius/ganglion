package runner

import (
	"os"
	"slices"
	"testing"

	"github.com/igorrius/ganglion/schemas"
)

// The schema in schemas/module-manifest.schema.json must describe exactly
// what ParseManifest reads (ADR-0023).
func TestManifestSchemaMatchesLoader(t *testing.T) {
	b, err := schemas.Get("module-manifest.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pointer string
		want    []string
	}{
		{"", append(schemas.YAMLFields(Manifest{}), "limits")},
		{"/properties/triggers/items", schemas.YAMLFields(Trigger{})},
		{"/$defs/cellDecl", schemas.YAMLFields(CellDecl{})},
		{"/$defs/configKey", schemas.YAMLFields(ConfigKey{})},
		{"/properties/cells", schemas.YAMLFields(Cells{})},
		{"/properties/capabilities", schemas.YAMLFields(Capabilities{})},
		{"/properties/escalate/items", schemas.YAMLFields(Escalation{})},
		{"/properties/limits", []string{"memory", "timeout"}},
	} {
		o, err := schemas.At(b, tc.pointer)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(tc.want)
		if !slices.Equal(o.Properties, tc.want) {
			t.Errorf("%q: schema properties %v, loader fields %v", tc.pointer, o.Properties, tc.want)
		}
		if o.AdditionalProperties {
			t.Errorf("%q: the loader rejects unknown fields; the schema must too", tc.pointer)
		}
	}
}

func TestDeyeManifestParses(t *testing.T) {
	src, err := os.ReadFile("../modules/deye/deye-reader.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest(src); err != nil {
		t.Fatal(err)
	}
}
