// Package vault is the node's encrypted secret store (ADR-0077): secrets
// as rows of the node's database, each sealed with AES-256-GCM under the
// master key and bound to its scope and name. The key is the operator's
// (a variable or its file) or a file the node makes beside the database
// at its first start. No function here returns a value to a person: the
// store is read by the code that hands a secret to a module or a
// transport.
package vault

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/igorrius/ganglion/secrets"
)

const (
	// KeyEnv is the variable of the operator's key: standard base64 of
	// KeySize bytes, or its _FILE.
	KeyEnv = "GANGLION_MASTER_KEY"
	// KeyFile is the key's file beside the database when KeyEnv is not set.
	KeyFile = "master.key"
	// KeySize is the key's length in bytes (AES-256).
	KeySize = 32
)

// Key is the store's master key.
type Key [KeySize]byte

// ErrKey is a key that cannot be used: not 32 bytes, not base64, a file
// that cannot be read or made. The error names the variable or the path,
// never the bytes.
var ErrKey = errors.New("vault: the master key cannot be used")

// KeyPath is master.key beside the database at db.
func KeyPath(db string) string { return filepath.Join(filepath.Dir(db), KeyFile) }

// NewKey is a random key (crypto/rand).
func NewKey() (Key, error) {
	var k Key
	if _, err := rand.Read(k[:]); err != nil {
		return Key{}, fmt.Errorf("%w: %w", ErrKey, err)
	}
	return k, nil
}

// LoadKey returns the operator's key when src has KeyEnv or KeyEnv_FILE
// (base64, surrounding whitespace ignored). Otherwise it reads the 32 raw
// bytes of the file at path; when that file does not exist it makes one
// with a new key, mode 0600, and created is true. A file that appeared
// meanwhile is read instead; an existing file is never rewritten. path ""
// with no operator key returns a new key that is written nowhere (a test
// node).
func LoadKey(src secrets.Source, path string) (key Key, created bool, err error) {
	text, err := src.Secret(KeyEnv)
	switch {
	case err != nil:
		// Env's errors name the variable or the path, never the content.
		return Key{}, false, fmt.Errorf("%w: %w", ErrKey, err)
	case text != "":
		key, err = decodeKey(text)
		return key, false, err
	case path == "":
		key, err = NewKey()
		return key, false, err
	}
	return fileKey(path)
}

// decodeKey is the operator's key from its base64 text.
func decodeKey(text string) (Key, error) {
	raw, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		// The decoder's error holds an offset; it is left out with the text.
		return Key{}, fmt.Errorf("%w: %s is not standard base64", ErrKey, KeyEnv)
	}
	if len(raw) != KeySize {
		return Key{}, fmt.Errorf("%w: %s holds %d bytes, want %d", ErrKey, KeyEnv, len(raw), KeySize)
	}
	return Key(raw), nil
}

// fileKey reads the key file at path, making it when it does not exist.
func fileKey(path string) (Key, bool, error) {
	key, err := readKey(path)
	if !errors.Is(err, fs.ErrNotExist) {
		return key, false, err
	}
	key, err = NewKey()
	if err != nil {
		return Key{}, false, err
	}
	switch err = publish(path, key); {
	case errors.Is(err, fs.ErrExist):
		// Another process made the file between the read and the write.
		key, err = readKey(path)
		return key, false, err
	case err != nil:
		return Key{}, false, fmt.Errorf("%w: %w", ErrKey, err)
	}
	return key, true, nil
}

// readKey is the key in the file at path.
func readKey(path string) (Key, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		// os errors name the path, never the content.
		return Key{}, fmt.Errorf("%w: %w", ErrKey, err)
	}
	if len(raw) != KeySize {
		return Key{}, fmt.Errorf("%w: %s holds %d bytes, want %d", ErrKey, path, len(raw), KeySize)
	}
	return Key(raw), nil
}

// publish writes key to a new file at path, mode 0600, and fails with
// fs.ErrExist when the file is already there. The bytes are written to a
// temporary file first and the name appears by a hard link, so a reader
// never sees a key file half written. A file system without hard links
// gets the file written in place.
func publish(path string, key Key) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), KeyFile+".*") // mode 0600
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if err := writeSynced(tmp, key[:]); err != nil {
		return err
	}
	if err := os.Link(tmp.Name(), path); err == nil || errors.Is(err, fs.ErrExist) {
		return err
	}
	return writeNew(path, key)
}

// writeNew creates the file at path exclusively and writes key into it.
func writeNew(path string, key Key) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	return writeSynced(f, key[:])
}

// writeSynced writes b to f, flushes it to the disk and closes f.
func writeSynced(f *os.File, b []byte) error {
	_, err := f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}
