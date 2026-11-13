package secrets_test

import (
	"errors"
	"io/fs"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/secrets"
)

func source(vars, files map[string]string) secrets.Env {
	return secrets.Env{
		Getenv: func(k string) string { return vars[k] },
		ReadFile: func(p string) ([]byte, error) {
			if v, ok := files[p]; ok {
				return []byte(v), nil
			}
			return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
		},
	}
}

func TestSecretFromTheVariableIsTrimmed(t *testing.T) {
	got, err := source(map[string]string{"TOKEN": " 7001:abc\r\n"}, nil).Secret("TOKEN")
	if err != nil || got != "7001:abc" {
		t.Fatalf("Secret = %q, %v", got, err)
	}
}

func TestSecretFromTheFileIsTrimmed(t *testing.T) {
	got, err := source(map[string]string{"TOKEN_FILE": "/run/secrets/tg"}, map[string]string{"/run/secrets/tg": "7001:file\n"}).Secret("TOKEN")
	if err != nil || got != "7001:file" {
		t.Fatalf("Secret = %q, %v", got, err)
	}
}

func TestUnsetSecretIsEmpty(t *testing.T) {
	if got, err := source(nil, nil).Secret("TOKEN"); err != nil || got != "" {
		t.Fatalf("Secret = %q, %v", got, err)
	}
}

func TestSecretErrorsNameTheVariableNeverTheContent(t *testing.T) {
	for name, tc := range map[string]struct {
		vars, files map[string]string
		is          error
	}{
		"missing file": {map[string]string{"TOKEN_FILE": "/missing"}, nil, fs.ErrNotExist},
		"empty file":   {map[string]string{"TOKEN_FILE": "/empty"}, map[string]string{"/empty": "\n"}, nil},
		"both set":     {map[string]string{"TOKEN": "very-secret", "TOKEN_FILE": "/f"}, map[string]string{"/f": "very-secret"}, nil},
	} {
		_, err := source(tc.vars, tc.files).Secret("TOKEN")
		switch {
		case err == nil:
			t.Errorf("%s: no error", name)
		case !strings.Contains(err.Error(), "TOKEN"):
			t.Errorf("%s: %v does not name the variable", name, err)
		case strings.Contains(err.Error(), "very-secret"):
			t.Errorf("%s: %v leaks the secret", name, err)
		case tc.is != nil && !errors.Is(err, tc.is):
			t.Errorf("%s: %v is not %v", name, err, tc.is)
		}
	}
}

func TestModuleScopeNamesTheModule(t *testing.T) {
	if got := secrets.ModuleScope("local/x"); got != "module:local/x" {
		t.Fatalf("ModuleScope = %q", got)
	}
	if secrets.ScopeNode != "node" {
		t.Fatalf("ScopeNode = %q", secrets.ScopeNode)
	}
}

func TestValidName(t *testing.T) {
	for name, want := range map[string]bool{
		"DEYE_APP_ID":            true,
		"wifi-home":              true,
		"a.b":                    true,
		"_x":                     true,
		strings.Repeat("a", 128): true,
		"":                       false,
		"1A":                     false,
		"a b":                    false,
		"a/b":                    false,
		"a:b":                    false,
		"a\n":                    false,
		strings.Repeat("a", 129): false,
	} {
		if got := secrets.ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, got, want)
		}
	}
}

// fixed is a Source of one answer.
type fixed struct {
	value string
	err   error
}

func (f fixed) Secret(string) (string, error) { return f.value, f.err }

// unasked fails the test when it is read.
type unasked struct{ t *testing.T }

func (u unasked) Secret(name string) (string, error) {
	u.t.Errorf("a later source was asked for %s", name)
	return "", nil
}

func TestFirstReadsSourcesInOrder(t *testing.T) {
	broken := errors.New("broken")
	for name, tc := range map[string]struct {
		sources secrets.First
		want    string
		err     error
	}{
		"the second answers when the first is empty": {secrets.First{fixed{}, fixed{value: "b"}}, "b", nil},
		"the first wins":           {secrets.First{fixed{value: "a"}, unasked{t}}, "a", nil},
		"an error ends the search": {secrets.First{fixed{err: broken}, unasked{t}}, "", broken},
		"no sources":               {secrets.First{}, "", nil},
		"nobody has it":            {secrets.First{fixed{}, fixed{}}, "", nil},
	} {
		got, err := tc.sources.Secret("TOKEN")
		if got != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("%s: Secret = %q, %v", name, got, err)
		}
	}
}
