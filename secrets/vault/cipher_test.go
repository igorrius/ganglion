package vault

import (
	"bytes"
	"testing"
)

func TestOpenTakesTheSameKeyScopeAndName(t *testing.T) {
	aead, err := newAEAD(Key{1})
	if err != nil {
		t.Fatal(err)
	}
	nonce, sealed, err := seal(aead, "module:local/a", "TOKEN", []byte("value"))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := open(aead, "module:local/a", "TOKEN", nonce, sealed); err != nil || string(got) != "value" {
		t.Fatalf("open = %q, %v", got, err)
	}
	other, err := newAEAD(Key{2})
	if err != nil {
		t.Fatal(err)
	}
	flipped := bytes.Clone(sealed)
	flipped[0] ^= 1
	for name, try := range map[string]func() ([]byte, error){
		"another key":    func() ([]byte, error) { return open(other, "module:local/a", "TOKEN", nonce, sealed) },
		"another scope":  func() ([]byte, error) { return open(aead, "module:local/b", "TOKEN", nonce, sealed) },
		"another name":   func() ([]byte, error) { return open(aead, "module:local/a", "TOKEN2", nonce, sealed) },
		"a flipped byte": func() ([]byte, error) { return open(aead, "module:local/a", "TOKEN", nonce, flipped) },
		"a short nonce":  func() ([]byte, error) { return open(aead, "module:local/a", "TOKEN", nonce[:4], sealed) },
		"scope and name": func() ([]byte, error) { return open(aead, "module:local/aT", "OKEN", nonce, sealed) },
	} {
		if got, err := try(); err == nil {
			t.Errorf("%s: opened as %q", name, got)
		}
	}
}
