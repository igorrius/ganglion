package settings_test

import (
	"strings"
	"testing"

	"github.com/igorrius/ganglion/settings"
)

// A module line names the edge it runs on; the core's name and no name both
// mean this node (R-EDGE-6).
func TestModuleNodeNamesTheEdge(t *testing.T) {
	for src, want := range map[string]string{
		"modules:\n  - name: local/a\n    node: home\n": "home",
		"modules:\n  - name: local/a\n    node: core\n": "",
		"modules:\n  - name: local/a\n":                 "",
	} {
		f, err := settings.Parse([]byte(src))
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if got := f.Modules[0].OnEdge(); got != want {
			t.Errorf("%q: OnEdge() = %q, want %q", src, got, want)
		}
	}
	for _, node := range []string{"a.b", `"x y"`} {
		_, err := settings.Parse([]byte("modules:\n  - name: local/a\n    node: " + node + "\n"))
		if err == nil || !strings.Contains(err.Error(), "modules[0].node") {
			t.Errorf("node %s: err = %v", node, err)
		}
	}
}
