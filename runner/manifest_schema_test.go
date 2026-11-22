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
		{"/properties/ports", schemas.YAMLFields(Ports{})},
		{"/$defs/portCall", schemas.YAMLFields(PortCall{})},
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

// The reader template (R-RDR-1) parses, with its three cells, its status
// tool's contract and its required setting.
func TestTheReaderTemplatesManifestParses(t *testing.T) {
	src, err := os.ReadFile("../examples/modules/reader-template/manifest.yaml")
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []string{"value", "online", "sample"} {
		if _, ok := m.Cells.Report[c]; !ok {
			t.Errorf("cell %s is not declared", c)
		}
	}
	if len(m.Triggers) != 2 || m.Triggers[1].Tool != "status" || m.Triggers[1].Output == nil {
		t.Errorf("triggers = %+v, want cron and the status tool with an output", m.Triggers)
	}
	if !m.Config["station"].Required {
		t.Error("station is not required")
	}
}
