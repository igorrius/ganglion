package update

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func newKey(t *testing.T, name string) (Key, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return Key{Name: name, Pub: pub}, priv
}

func sumLine(data []byte, name string) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:]) + "  " + name + "\n"
}

func TestEmbeddedKeysParse(t *testing.T) {
	if _, err := Keys(); err != nil {
		t.Fatal(err)
	}
}

func TestVerifySumsAcceptsAnyKeyOfTheList(t *testing.T) {
	old, _ := newKey(t, "old")
	cur, priv := newKey(t, "current")
	sums := []byte(sumLine([]byte("bin"), "ganglion-linux-amd64"))
	k, err := VerifySums(sums, ed25519.Sign(priv, sums), []Key{old, cur})
	if err != nil || k.Name != "current" {
		t.Fatalf("key %q, err %v", k.Name, err)
	}
}

func TestVerifySumsRefusesWhatTheProjectDidNotSign(t *testing.T) {
	ours, priv := newKey(t, "ours")
	_, theirs := newKey(t, "theirs")
	sums := []byte(sumLine([]byte("bin"), "ganglion-linux-amd64"))
	sig := ed25519.Sign(priv, sums)
	tampered := []byte(sumLine([]byte("evil"), "ganglion-linux-amd64"))
	cases := map[string]struct {
		sums, sig []byte
		keys      []Key
		want      error
	}{
		"no keys":          {sums, sig, nil, ErrNoKeys},
		"another key":      {sums, ed25519.Sign(theirs, sums), []Key{ours}, ErrBadSignature},
		"sums changed":     {tampered, sig, []Key{ours}, ErrBadSignature},
		"short signature":  {sums, sig[:10], []Key{ours}, ErrBadSignature},
		"empty signature":  {sums, nil, []Key{ours}, ErrBadSignature},
		"sums over limits": {bytes.Repeat([]byte("a"), MaxSumsBytes+1), sig, []Key{ours}, ErrBadSums},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifySums(c.sums, c.sig, c.keys); !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
		})
	}
}

func TestParseSumsAndCheck(t *testing.T) {
	bin, man := []byte("binary"), []byte(`{"version":"v1.2.3"}`)
	s, err := ParseSums([]byte(sumLine(bin, "ganglion-linux-amd64") + strings.Replace(sumLine(man, "release.json"), "  ", " *", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Check("ganglion-linux-amd64", bytes.NewReader(bin)); err != nil {
		t.Fatal(err)
	}
	if err := s.Check("release.json", bytes.NewReader(man)); err != nil {
		t.Fatal(err)
	}
	if err := s.Check("ganglion-linux-amd64", bytes.NewReader([]byte("other"))); !errors.Is(err, ErrSumMismatch) {
		t.Fatalf("a changed file: %v", err)
	}
	if err := s.Check("ganglion-linux-arm64", bytes.NewReader(bin)); !errors.Is(err, ErrSumMismatch) {
		t.Fatalf("a file not listed: %v", err)
	}
}

func TestParseSumsRefusesAnythingButPlainLines(t *testing.T) {
	good := sumLine([]byte("x"), "ganglion-linux-amd64")
	sum := good[:64]
	cases := map[string]string{
		"empty":           "",
		"a path":          sum + "  ../ganglion\n",
		"a folder":        sum + "  bin/ganglion\n",
		"a hidden name":   sum + "  .ganglion\n",
		"upper-case hex":  strings.ToUpper(sum) + "  ganglion\n",
		"a short sum":     sum[:60] + "  ganglion\n",
		"one space":       sum + " ganglion\n",
		"a name twice":    good + good,
		"a blank line":    good + "\n",
		"a trailing word": sum + "  ganglion extra\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSums([]byte(in)); !errors.Is(err, ErrBadSums) {
				t.Fatalf("err = %v, want ErrBadSums", err)
			}
		})
	}
}

func TestParseManifest(t *testing.T) {
	commit := strings.Repeat("ab", 20)
	m, err := ParseManifest([]byte(`{"version":"v0.2.0","commit":"` + commit + `","env":1,"later":"ignored"}`))
	if err != nil || m != (Manifest{Version: "v0.2.0", Commit: commit, Env: 1}) {
		t.Fatalf("manifest %+v, err %v", m, err)
	}
	bad := map[string]string{
		"not json":     `{`,
		"no version":   `{"commit":"` + commit + `","env":1}`,
		"a bad tag":    `{"version":"0.2.0","commit":"` + commit + `","env":1}`,
		"short commit": `{"version":"v0.2.0","commit":"abc","env":1}`,
		"no env":       `{"version":"v0.2.0","commit":"` + commit + `"}`,
		"over limits":  `{"version":"v0.2.0","pad":"` + strings.Repeat("a", MaxManifestBytes) + `"}`,
	}
	for name, in := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseManifest([]byte(in)); !errors.Is(err, ErrBadManifest) {
				t.Fatalf("err = %v, want ErrBadManifest", err)
			}
		})
	}
}

func TestParseKeyRefusesOtherKeys(t *testing.T) {
	if _, err := ParseKey([]byte("not pem")); err == nil {
		t.Fatal("accepted text")
	}
	priv := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1}})
	if _, err := ParseKey(priv); err == nil {
		t.Fatal("accepted a private key block")
	}
}

// TestOpenSSLSignatureVerifies is the contract with the release workflow
// and with a person checking by hand: openssl signs SHA256SUMS with a raw
// Ed25519 signature, and the node verifies it with the PEM key openssl
// writes.
func TestOpenSSLSignatureVerifies(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("no openssl")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command("openssl", args...).CombinedOutput(); err != nil {
			t.Fatalf("openssl %v: %v\n%s", args, err, out)
		}
	}
	keyPath, pubPath := filepath.Join(dir, "key.pem"), filepath.Join(dir, "pub.pem")
	sumsPath, sigPath := filepath.Join(dir, SumsFile), filepath.Join(dir, SignatureFile)
	run("genpkey", "-algorithm", "ed25519", "-out", keyPath)
	run("pkey", "-in", keyPath, "-pubout", "-out", pubPath)
	if err := os.WriteFile(sumsPath, []byte(sumLine([]byte("bin"), "ganglion-linux-amd64")), 0o600); err != nil {
		t.Fatal(err)
	}
	run("pkeyutl", "-sign", "-inkey", keyPath, "-rawin", "-in", sumsPath, "-out", sigPath)
	pubPEM, _ := os.ReadFile(pubPath)
	pub, err := ParseKey(pubPEM)
	if err != nil {
		t.Fatal(err)
	}
	sums, _ := os.ReadFile(sumsPath)
	sig, _ := os.ReadFile(sigPath)
	if _, err := VerifySums(sums, sig, []Key{{Name: "openssl", Pub: pub}}); err != nil {
		t.Fatal(err)
	}
	// And the other way: a Go key in PEM is one openssl reads.
	k, priv := newKey(t, "go")
	der, _ := x509.MarshalPKIXPublicKey(k.Pub)
	if err := os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sigPath, ed25519.Sign(priv, sums), 0o600); err != nil {
		t.Fatal(err)
	}
	run("pkeyutl", "-verify", "-pubin", "-inkey", pubPath, "-rawin", "-in", sumsPath, "-sigfile", sigPath)
}
