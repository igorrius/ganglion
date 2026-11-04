package chatlab

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestOnlyTheConnectorKnowsTelegram keeps Telegram behind Peer, so another
// channel means one more connector and the same scenarios.
func TestOnlyTheConnectorKnowsTelegram(t *testing.T) {
	allowed := []string{"telegram.go", "node.go"}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || slices.Contains(allowed, f) {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), f, src, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range parsed.Imports {
			if strings.Contains(imp.Path.Value, "telegram") {
				t.Errorf("%s imports %s", f, imp.Path.Value)
			}
		}
	}
}
