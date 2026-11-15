package cmd

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func updateRun(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := newRootIO(strings.NewReader(""), &out, &errOut, nil).Run(context.Background(), append([]string{"ganglion", "update"}, args...))
	return out.String(), err
}

func TestImageEnvLevel(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, c := range []struct {
		path string
		want int
		bad  bool
	}{
		{"", 0, false},
		{filepath.Join(dir, "missing"), 0, false},
		{write("one", "1\n"), 1, false},
		{write("seven", " 7 "), 7, false},
		{write("junk", "one"), 0, true},
		{write("neg", "-2"), 0, true},
	} {
		got, err := imageEnvLevel(c.path, os.ReadFile)
		if got != c.want || (err != nil) != c.bad {
			t.Errorf("%q: %d %v", c.path, got, err)
		}
		if imageEnvOr0(c.path, os.ReadFile) != c.want {
			t.Errorf("%q: a node start reads a different level", c.path)
		}
	}
}

// A node that never updated has nothing to roll back to: the command says so
// in the node's words, fails, and makes no releases folder.
func TestUpdateRollbackOfANodeThatNeverUpdated(t *testing.T) {
	clearEnv(t)
	db := filepath.Join(t.TempDir(), "ganglion.db")
	_, err := updateRun(t, "rollback", "--db", db, "--image-env", "")
	if err == nil || !strings.Contains(err.Error(), "no earlier version") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(db), "releases")); err == nil {
		t.Fatal("the releases folder was made")
	}
}

func TestUpdateRefusesAWrongCall(t *testing.T) {
	clearEnv(t)
	db := filepath.Join(t.TempDir(), "ganglion.db")
	for _, args := range [][]string{{"install"}, {"install", "v1.2.3", "v1.2.4"}, {"rollback", "now"}, {"upgrade"}} {
		if _, err := updateRun(t, append(args, "--db", db)...); err == nil || errors.Is(err, nil) {
			t.Errorf("%v: no error", args)
		}
	}
	// A version that is not one is refused before anything is fetched or made.
	_, err := updateRun(t, "install", "1.2", "--db", db)
	if err == nil || !strings.Contains(err.Error(), "not a release version") {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(db), "releases")); err == nil {
		t.Fatal("the releases folder was made")
	}
}
