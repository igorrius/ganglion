package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/update"
)

// release writes a signed release into a folder and returns it with its key.
func release(t *testing.T, tag string) (string, []update.Key, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	dir := t.TempDir()
	files := map[string][]byte{
		"ganglion-linux-amd64": []byte("amd64"),
		"ganglion-linux-arm64": []byte("arm64"),
		update.ManifestFile:    []byte(`{"version":"` + tag + `","commit":"` + strings.Repeat("0", 40) + `","env":1}`),
	}
	var sums strings.Builder
	for _, n := range []string{"ganglion-linux-amd64", "ganglion-linux-arm64", update.ManifestFile} {
		s := sha256.Sum256(files[n])
		sums.WriteString(hex.EncodeToString(s[:]) + "  " + n + "\n")
		write(t, dir, n, files[n])
	}
	write(t, dir, update.SumsFile, []byte(sums.String()))
	write(t, dir, update.SignatureFile, ed25519.Sign(priv, []byte(sums.String())))
	return dir, []update.Key{{Name: "test", Pub: pub}}, priv
}

func write(t *testing.T, dir, name string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCheckAcceptsASignedRelease(t *testing.T) {
	dir, keys, _ := release(t, "v0.2.0")
	m, k, err := checkWith(dir, "v0.2.0", keys)
	if err != nil || m.Version != "v0.2.0" || k.Name != "test" {
		t.Fatalf("manifest %+v, key %q, err %v", m, k.Name, err)
	}
}

func TestCheckRefusesABrokenRelease(t *testing.T) {
	cases := map[string]func(t *testing.T, dir string, priv ed25519.PrivateKey){
		"a binary replaced": func(t *testing.T, dir string, _ ed25519.PrivateKey) {
			write(t, dir, "ganglion-linux-arm64", []byte("evil"))
		},
		"a binary missing": func(_ *testing.T, dir string, _ ed25519.PrivateKey) {
			_ = os.Remove(filepath.Join(dir, "ganglion-linux-amd64"))
		},
		"sums re-signed by another key": func(t *testing.T, dir string, _ ed25519.PrivateKey) {
			_, other, _ := ed25519.GenerateKey(rand.Reader)
			sums, _ := os.ReadFile(filepath.Join(dir, update.SumsFile))
			write(t, dir, update.SignatureFile, ed25519.Sign(other, sums))
		},
		"a platform not listed": func(t *testing.T, dir string, priv ed25519.PrivateKey) {
			sums, _ := os.ReadFile(filepath.Join(dir, update.SumsFile))
			var keep []string
			for _, l := range strings.SplitAfter(string(sums), "\n") {
				if !strings.Contains(l, "arm64") {
					keep = append(keep, l)
				}
			}
			s := strings.Join(keep, "")
			write(t, dir, update.SumsFile, []byte(s))
			write(t, dir, update.SignatureFile, ed25519.Sign(priv, []byte(s)))
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			dir, keys, priv := release(t, "v0.2.0")
			change(t, dir, priv)
			if _, _, err := checkWith(dir, "v0.2.0", keys); err == nil {
				t.Fatal("accepted")
			}
		})
	}
	t.Run("another tag", func(t *testing.T) {
		dir, keys, _ := release(t, "v0.1.9")
		if _, _, err := checkWith(dir, "v0.2.0", keys); err == nil {
			t.Fatal("accepted a release that names another tag")
		}
	})
}
