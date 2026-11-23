package natsbus

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"
)

// UserKey is a user's NKey pair, made or read on an edge (ADR-0116): it
// keeps the seed in memory and gives it out only inside a .creds text.
type UserKey struct{ kp nkeys.KeyPair }

// ErrNoUserKey is a .creds text with no user seed in it.
var ErrNoUserKey = errors.New("natsbus: the credentials hold no user key")

// NewUserKey makes a user key pair from crypto/rand.
func NewUserKey() (UserKey, error) {
	kp, err := nkeys.CreateUser()
	if err != nil {
		return UserKey{}, fmt.Errorf("natsbus: make a user key: %w", err)
	}
	return UserKey{kp}, nil
}

// UserKeyFromCreds reads the user key pair out of a .creds text.
func UserKeyFromCreds(creds []byte) (UserKey, error) {
	kp, err := jwt.ParseDecoratedUserNKey(creds)
	if err != nil {
		return UserKey{}, ErrNoUserKey
	}
	if _, err := kp.PublicKey(); err != nil {
		return UserKey{}, ErrNoUserKey
	}
	return UserKey{kp}, nil
}

// PublicKey is the key's public half.
func (k UserKey) PublicKey() (string, error) { return k.kp.PublicKey() }

// Sign signs msg with the key.
func (k UserKey) Sign(msg []byte) ([]byte, error) { return k.kp.Sign(msg) }

// Creds is the .creds text of the key with the user JWT tok.
func (k UserKey) Creds(tok string) ([]byte, error) {
	seed, err := k.kp.Seed()
	if err != nil {
		return nil, fmt.Errorf("natsbus: user seed: %w", err)
	}
	b, err := jwt.FormatUserConfig(tok, seed)
	if err != nil {
		return nil, fmt.Errorf("natsbus: creds: %w", err)
	}
	return b, nil
}

// IsUserKey reports whether pub is a NATS user public key.
func IsUserKey(pub string) bool { return nkeys.IsValidPublicUserKey(pub) }

// VerifyUser checks that sig is the user key pub's signature of msg.
func VerifyUser(pub string, msg, sig []byte) error {
	if !nkeys.IsValidPublicUserKey(pub) {
		return ErrBadUserKey
	}
	kp, err := nkeys.FromPublicKey(pub)
	if err != nil {
		return ErrBadUserKey
	}
	return kp.Verify(msg, sig)
}

// CredsInfo is what the user JWT of a .creds text says.
type CredsInfo struct {
	// Subject is the user's public key.
	Subject string
	// Permissions are the allow lists.
	Permissions UserPermissions
	// Expires is zero for a JWT that never expires.
	Expires time.Time
}

// ReadCreds reads the user JWT of a .creds text without checking who
// signed it: the edge trusts the file it wrote itself.
func ReadCreds(creds []byte) (CredsInfo, error) {
	tok, err := jwt.ParseDecoratedJWT(creds)
	if err != nil {
		return CredsInfo{}, errors.New("natsbus: the credentials hold no JWT")
	}
	return ReadUserJWT(tok)
}

// ReadUserJWT reads a user JWT as ReadCreds does.
func ReadUserJWT(tok string) (CredsInfo, error) {
	uc, err := jwt.DecodeUserClaims(tok)
	if err != nil {
		return CredsInfo{}, errors.New("natsbus: not a user JWT")
	}
	ci := CredsInfo{Subject: uc.Subject, Permissions: UserPermissions{
		Publish: slices.Clone([]string(uc.Pub.Allow)), Subscribe: slices.Clone([]string(uc.Sub.Allow))}}
	if uc.Expires != 0 {
		ci.Expires = time.Unix(uc.Expires, 0)
	}
	return ci, nil
}

// ErrUserRefused is a connection the server refused for the user's
// credentials: not authorized, or revoked.
var ErrUserRefused = errors.New("natsbus: the server refused the user")

// DialUser connects to url as the user of creds and returns the connection's
// close and a channel closed when the server drops it. It is how a test, or
// a check, sees what the server lets a user do.
func DialUser(url string, creds []byte, timeout time.Duration) (closeConn func(), dropped <-chan struct{}, err error) {
	gone := make(chan struct{})
	nc, err := nats.Connect(url, nats.UserCredentialBytes(creds), nats.Timeout(timeout), nats.MaxReconnects(0),
		nats.ClosedHandler(func(*nats.Conn) { close(gone) }))
	if errors.Is(err, nats.ErrAuthorization) || errors.Is(err, nats.ErrAuthRevoked) || errors.Is(err, nats.ErrAuthExpired) {
		return nil, nil, fmt.Errorf("%w: %w", ErrUserRefused, err)
	}
	if err != nil {
		return nil, nil, err
	}
	return nc.Close, gone, nil
}
