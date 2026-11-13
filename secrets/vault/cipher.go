package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"

	"github.com/igorrius/ganglion/secrets"
)

// aadVersion starts the additional data of every sealed value. A change of
// the format takes a new version, so an old row never opens as a new one.
const aadVersion = "ganglion-secret-v1"

// newAEAD is AES-256-GCM on key.
func newAEAD(key Key) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// bound is the additional data that ties a sealed value to its scope and
// name: a row moved to another scope or name in the database does not open.
func bound(scope secrets.Scope, name string) []byte {
	return []byte(aadVersion + "\x00" + string(scope) + "\x00" + name)
}

// seal encrypts plaintext for scope and name under a new random nonce.
func seal(aead cipher.AEAD, scope secrets.Scope, name string, plaintext []byte) (nonce, ciphertext []byte, err error) {
	nonce = make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, aead.Seal(nil, nonce, plaintext, bound(scope, name)), nil
}

// open decrypts a value sealed for scope and name. It fails for another
// key, another scope or name, and a changed byte.
func open(aead cipher.AEAD, scope secrets.Scope, name string, nonce, ciphertext []byte) ([]byte, error) {
	if len(nonce) != aead.NonceSize() {
		return nil, errNonce
	}
	return aead.Open(nil, nonce, ciphertext, bound(scope, name))
}
