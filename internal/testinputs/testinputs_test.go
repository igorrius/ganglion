package testinputs

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestSourcesOfTheBinary(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	got, err := sources("../..", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"main.go",        // the package itself
		"cmd",            // a directory: a new file changes it
		"config/lab.go",  // left out of this build by its tag, and the tag is what a test checks
		"go.mod",         // the versions of the dependencies
		"go.sum",         //
		".",              // the root, where a new file of package main would go
		"agent/agent.go", // a package the binary depends on
	} {
		if !slices.Contains(got, filepath.Join(root, want)) {
			t.Errorf("no %s among %d inputs", want, len(got))
		}
	}
	if !slices.IsSorted(got) || len(slices.Compact(slices.Clone(got))) != len(got) {
		t.Error("the inputs are not sorted and unique")
	}
	for _, f := range got {
		if strings.HasSuffix(f, "_test.go") {
			t.Errorf("%s: a build does not read tests", f)
		}
		if f != root && !strings.HasPrefix(f, root+string(filepath.Separator)) {
			t.Errorf("%s: outside the module, where go test does not look", f)
		}
	}
}

// A module's sources are tagged for wasip1, so the environment must reach go list.
func TestSourcesOfAWasmBuild(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	got, err := sources("../../modules/deye/wasm", []string{"GOOS=wasip1", "GOARCH=wasm"})
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "modules/deye/wasm/main.go"); !slices.Contains(got, want) {
		t.Errorf("no %s in %v", want, got)
	}
}

func TestSourcesOfNothingIsAnError(t *testing.T) {
	if _, err := sources("./no-such-package", nil); err == nil {
		t.Fatal("no error for a pattern that matches nothing")
	}
}
