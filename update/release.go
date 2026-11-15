// Package update moves the node to a newer core release, at its owner's
// command (R-UPD-1…7, ADR-0081). This file verifies a release: the
// project's signature over SHA256SUMS, the sums of its files and its
// release.json.
package update

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"embed"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
)

// The files of a core release besides its binaries.
const (
	SumsFile      = "SHA256SUMS"
	SignatureFile = "SHA256SUMS.sig"
	ManifestFile  = "release.json"
)

// Limits on what a release's small files may hold; the files come from
// outside the process.
const (
	MaxSumsBytes     = 16 << 10
	MaxManifestBytes = 16 << 10
)

// Errors of the verification; each wraps the detail.
var (
	ErrNoKeys       = errors.New("update: no release key")
	ErrBadSignature = errors.New("update: SHA256SUMS is not signed by the project")
	ErrBadSums      = errors.New("update: SHA256SUMS is malformed")
	ErrSumMismatch  = errors.New("update: a file does not match its sum")
	ErrBadManifest  = errors.New("update: release.json is malformed")
)

// BinaryName is the name of the core binary for a platform in a release,
// ganglion-linux-amd64 for one.
func BinaryName(goos, goarch string) string { return "ganglion-" + goos + "-" + goarch }

// Key is a public key of the project that signs releases.
type Key struct {
	Name string // the file's name without .pem
	Pub  ed25519.PublicKey
}

//go:embed keys
var keyFiles embed.FS

// Keys are the release keys embedded in this binary, from update/keys.
func Keys() ([]Key, error) {
	list, err := keyFiles.ReadDir("keys")
	if err != nil {
		return nil, err
	}
	var keys []Key
	for _, e := range list {
		if path.Ext(e.Name()) != ".pem" {
			continue
		}
		data, err := keyFiles.ReadFile("keys/" + e.Name())
		if err != nil {
			return nil, err
		}
		pub, err := ParseKey(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.Name(), err)
		}
		keys = append(keys, Key{Name: strings.TrimSuffix(e.Name(), ".pem"), Pub: pub})
	}
	return keys, nil
}

// ParseKey reads a public Ed25519 key in PEM (PKIX, "PUBLIC KEY"), as
// `openssl pkey -pubout` writes it.
func ParseKey(data []byte) (ed25519.PublicKey, error) {
	b, _ := pem.Decode(data)
	if b == nil || b.Type != "PUBLIC KEY" {
		return nil, errors.New("update: not a PEM public key")
	}
	k, err := x509.ParsePKIXPublicKey(b.Bytes)
	if err != nil {
		return nil, fmt.Errorf("update: %w", err)
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("update: not an Ed25519 key")
	}
	return pub, nil
}

// VerifySums checks that one of keys signed sums: a plain Ed25519
// signature over the file's bytes, which openssl checks too (R-UPD-1). It
// returns the key that did.
func VerifySums(sums, sig []byte, keys []Key) (Key, error) {
	if len(keys) == 0 {
		return Key{}, ErrNoKeys
	}
	if len(sums) > MaxSumsBytes {
		return Key{}, fmt.Errorf("%w: %d bytes", ErrBadSums, len(sums))
	}
	if len(sig) != ed25519.SignatureSize {
		return Key{}, fmt.Errorf("%w: a signature of %d bytes", ErrBadSignature, len(sig))
	}
	for _, k := range keys {
		if ed25519.Verify(k.Pub, sums, sig) {
			return k, nil
		}
	}
	return Key{}, ErrBadSignature
}

// Sums are the SHA-256 sums a release's SHA256SUMS lists, by file name.
type Sums map[string][sha256.Size]byte

// sumsLine is one line of sha256sum's output: the hex sum, two spaces (or
// a space and the binary mark) and a plain file name.
var sumsLine = regexp.MustCompile(`^([0-9a-f]{64}) [ *]([A-Za-z0-9][A-Za-z0-9._-]*)$`)

// ParseSums reads SHA256SUMS strictly: every line is a sum and a plain
// name, no name twice, at least one line.
func ParseSums(data []byte) (Sums, error) {
	if len(data) > MaxSumsBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrBadSums, len(data))
	}
	out := Sums{}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for n := 1; sc.Scan(); n++ {
		m := sumsLine.FindStringSubmatch(sc.Text())
		if m == nil {
			return nil, fmt.Errorf("%w: line %d", ErrBadSums, n)
		}
		if _, dup := out[m[2]]; dup {
			return nil, fmt.Errorf("%w: %s twice", ErrBadSums, m[2])
		}
		var sum [sha256.Size]byte
		_, _ = hex.Decode(sum[:], []byte(m[1])) // the pattern makes it valid
		out[m[2]] = sum
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no file", ErrBadSums)
	}
	return out, nil
}

// Check reads r to its end and compares its sum with the one listed for
// name.
func (s Sums) Check(name string, r io.Reader) error {
	want, ok := s[name]
	if !ok {
		return fmt.Errorf("%w: %s is not listed", ErrSumMismatch, name)
	}
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return err
	}
	if subtle.ConstantTimeCompare(h.Sum(nil), want[:]) != 1 {
		return fmt.Errorf("%w: %s", ErrSumMismatch, name)
	}
	return nil
}

// Manifest is a release's release.json: what the release is and what its
// binary needs of the machine around it.
type Manifest struct {
	// Version is the release's tag, vX.Y.Z.
	Version string `json:"version"`
	// Commit is the full commit the binaries were built from.
	Commit string `json:"commit"`
	// Env is the level of the image's environment the binary needs; it
	// rises when the image must change with the code (a new Rust target, a
	// new entrypoint). A node refuses a release whose Env is above its
	// image's.
	Env int `json:"env"`
}

var (
	versionPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)
	commitPattern  = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// ParseManifest reads release.json. Fields it does not know are ignored,
// so a newer release can add some and still be read by an older node; the
// file is covered by the signed sums either way.
func ParseManifest(data []byte) (Manifest, error) {
	if len(data) > MaxManifestBytes {
		return Manifest{}, fmt.Errorf("%w: %d bytes", ErrBadManifest, len(data))
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("%w: %v", ErrBadManifest, err)
	}
	switch {
	case !versionPattern.MatchString(m.Version):
		return Manifest{}, fmt.Errorf("%w: version %q", ErrBadManifest, m.Version)
	case !commitPattern.MatchString(m.Commit):
		return Manifest{}, fmt.Errorf("%w: commit %q", ErrBadManifest, m.Commit)
	case m.Env < 1:
		return Manifest{}, fmt.Errorf("%w: env %d", ErrBadManifest, m.Env)
	}
	return m, nil
}
