package intake

import (
	"crypto/ecdh"
	"crypto/hpke"
)

// AddSecret inserts a secret request with a given id, name and key, which
// the random reader cannot give a test that needs a fixed ciphertext.
func (r *Registry) AddSecret(id, name string, priv *ecdh.PrivateKey, h SecretFunc) {
	key, err := hpke.NewDHKEMPrivateKey(priv)
	if err != nil {
		panic(err)
	}
	q := &request{kind: KindSecret, name: name, secret: h, key: key, closed: NoClosed}
	q.expires = r.now().Add(r.cfg.TTL)
	r.mu.Lock()
	r.reqs[id] = q
	r.mu.Unlock()
}
