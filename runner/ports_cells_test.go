package runner

import (
	"strings"
	"testing"
)

// cells is reserved like notify: no port of any kind takes the name.
func TestCellsIsAReservedPortName(t *testing.T) {
	for _, kind := range []string{"emits: {cells: number}", "accepts: {cells: number}",
		"serves: {cells: {in: number, out: number}}", "calls: {cells: {in: number, out: number}}"} {
		_, err := ParseManifest([]byte("name: p\nports:\n  " + kind + "\n"))
		if err == nil || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("%s: err = %v, want a reserved name", kind, err)
		}
	}
}
