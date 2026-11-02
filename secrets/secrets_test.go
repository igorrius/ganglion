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
