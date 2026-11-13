package vault_test

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
)

// env is a secrets.Env over a map of variables and the real file system.
func env(vars map[string]string) secrets.Env {
	return secrets.Env{Getenv: func(k string) string { return vars[k] }, ReadFile: os.ReadFile}
}

// keyText is a key every byte of which is b, and its base64.
func keyText(b byte) (vault.Key, string) {
	var k vault.Key
	for i := range k {
		k[i] = b
	}
	return k, base64.StdEncoding.EncodeToString(k[:])
}

func TestKeyPathIsBesideTheDatabase(t *testing.T) {
	if got := vault.KeyPath("/data/ganglion.db"); got != "/data/master.key" {
		t.Fatalf("KeyPath = %q", got)
	}
}

func TestLoadKeyMakesTheFileOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), vault.KeyFile)
	first, created, err := vault.LoadKey(env(nil), path)
	if err != nil || !created {
		t.Fatalf("LoadKey = created %v, %v", created, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() != vault.KeySize || info.Mode().Perm() != 0o600 {
		t.Fatalf("the key file: %v, %v", info, err)
	}
	second, created, err := vault.LoadKey(env(nil), path)
	if err != nil || created || second != first {
		t.Fatalf("the second LoadKey = same %v, created %v, %v", second == first, created, err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), "*")); len(left) != 1 {
		t.Fatalf("files beside the key: %v", left)
	}
}

func TestTheFileLoadKeyMakesIsPrivateUnderAnyUmask(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)
	path := filepath.Join(t.TempDir(), vault.KeyFile)
	if _, _, err := vault.LoadKey(env(nil), path); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the key file: %v, %v", info, err)
	}
}

func TestLoadKeyTakesTheOperatorsKey(t *testing.T) {
	want, text := keyText(7)
	dir := t.TempDir()
	path := filepath.Join(dir, vault.KeyFile)
	got, created, err := vault.LoadKey(env(map[string]string{vault.KeyEnv: " " + text + "\n"}), path)
	if err != nil || created || got != want {
		t.Fatalf("LoadKey = same %v, created %v, %v", got == want, created, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a key file was made beside the operator's key: %v", err)
	}
}

func TestLoadKeyTakesTheOperatorsFile(t *testing.T) {
	want, text := keyText(9)
	file := filepath.Join(t.TempDir(), "key.b64")
	if err := os.WriteFile(file, []byte(text+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, created, err := vault.LoadKey(env(map[string]string{vault.KeyEnv + "_FILE": file}), "")
	if err != nil || created || got != want {
		t.Fatalf("LoadKey = same %v, created %v, %v", got == want, created, err)
	}
}

func TestLoadKeyReadsAFileOfAnyMode(t *testing.T) {
	want, _ := keyText(3)
	path := filepath.Join(t.TempDir(), vault.KeyFile)
	if err := os.WriteFile(path, want[:], 0o644); err != nil {
		t.Fatal(err)
	}
	got, created, err := vault.LoadKey(env(nil), path)
	if err != nil || created || got != want {
		t.Fatalf("LoadKey = same %v, created %v, %v", got == want, created, err)
	}
}

func TestAKeyThatCannotBeUsedIsErrKeyWithoutItsText(t *testing.T) {
	dir := t.TempDir()
	short := filepath.Join(dir, "short.key")
	if err := os.WriteFile(short, bytes.Repeat([]byte{'k'}, vault.KeySize-1), 0o600); err != nil {
		t.Fatal(err)
	}
	shortText := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{'k'}, vault.KeySize-1))
	for name, tc := range map[string]struct {
		vars  map[string]string
		path  string
		leaks string
	}{
		"base64 of 31 bytes":       {map[string]string{vault.KeyEnv: shortText}, "", shortText},
		"not base64":               {map[string]string{vault.KeyEnv: "not*base64*at*all"}, "", "not*base64"},
		"a key file of 31 bytes":   {nil, short, "kkkk"},
		"a key file is a folder":   {nil, dir, "\x00"},
		"no parent folder":         {nil, filepath.Join(dir, "missing", vault.KeyFile), "\x00"},
		"the operator's file gone": {map[string]string{vault.KeyEnv + "_FILE": filepath.Join(dir, "gone")}, "", "\x00"},
	} {
		_, created, err := vault.LoadKey(env(tc.vars), tc.path)
		switch {
		case !errors.Is(err, vault.ErrKey) || created:
			t.Errorf("%s: created %v, %v", name, created, err)
		case strings.Contains(err.Error(), tc.leaks):
			t.Errorf("%s: the error holds the key's text: %v", name, err)
		}
	}
}

func TestLoadKeyWithoutAPathWritesNothing(t *testing.T) {
	t.Chdir(t.TempDir())
	a, createdA, errA := vault.LoadKey(env(nil), "")
	b, createdB, errB := vault.LoadKey(env(nil), "")
	if errA != nil || errB != nil || createdA || createdB || a == b {
		t.Fatalf("LoadKey twice: differ %v, created %v %v, %v %v", a != b, createdA, createdB, errA, errB)
	}
	if left, _ := filepath.Glob("*"); len(left) != 0 {
		t.Fatalf("files written: %v", left)
	}
}

func TestTwoLoadKeysOnANewPathAgree(t *testing.T) {
	for range 20 {
		path := filepath.Join(t.TempDir(), vault.KeyFile)
		var keys [2]vault.Key
		var errs [2]error
		var wg sync.WaitGroup
		for i := range keys {
			wg.Go(func() { keys[i], _, errs[i] = vault.LoadKey(env(nil), path) })
		}
		wg.Wait()
		if errs[0] != nil || errs[1] != nil || keys[0] != keys[1] {
			t.Fatalf("LoadKey twice at once: same %v, %v, %v", keys[0] == keys[1], errs[0], errs[1])
		}
	}
}
