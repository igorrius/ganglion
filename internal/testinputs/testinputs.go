// Package testinputs is for a test that starts go build or go list. It tells go
// test which sources that child process reads, as go test sees only what the test
// itself opens: without this, a change to them would reuse the cached pass.
package testinputs

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

// Build records the sources of a build of pattern as inputs of the calling test.
// env is added to the child's environment, as for a build with another GOOS.
//
// It stats and never opens a directory: go test would hash an opened directory's
// whole listing, and the root's holds .git, whose time differs on every run. A
// new file still shows, as it changes the time of the directory it is in.
func Build(t testing.TB, pattern string, env ...string) {
	t.Helper()
	files, err := sources(pattern, env)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if _, err := os.Stat(f); err != nil { // go test records the call
			t.Fatal(err)
		}
	}
}

// listFormat prints, one path a line, what a build of a package of this module
// reads: its directory, its Go files, those a build tag leaves out (the tag is
// what a boundary test checks), its embedded files, and the module's root files.
const listFormat = `{{if and .Module .Module.Main}}{{$d := .Dir -}}
{{$d}}
{{.Module.Dir}}
{{.Module.GoMod}}
{{.Module.Dir}}/go.sum
{{range .GoFiles}}{{$d}}/{{.}}
{{end}}{{range .IgnoredGoFiles}}{{$d}}/{{.}}
{{end}}{{range .EmbedFiles}}{{$d}}/{{.}}
{{end}}{{end}}`

// sources lists what a build of pattern reads, sorted and unique.
func sources(pattern string, env []string) ([]string, error) {
	cmd := exec.Command("go", "list", "-deps", "-f", listFormat, pattern)
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			return nil, fmt.Errorf("testinputs: go list %s: %w: %s", pattern, err, exit.Stderr)
		}
		return nil, fmt.Errorf("testinputs: go list %s: %w", pattern, err)
	}
	files := strings.Fields(string(out))
	if len(files) == 0 {
		return nil, fmt.Errorf("testinputs: go list %s: no package of this module", pattern)
	}
	slices.Sort(files)
	return slices.Compact(files), nil
}
