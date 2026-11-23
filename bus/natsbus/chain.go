package natsbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/igorrius/ganglion/secrets"
	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

// The names of the chain's entries in the node's scope of the secret store
// (ADR-0109, ADR-0113). Each seed is made on first need and never rewritten.
const (
	SeedOperator = "bus.operator"
	SeedAccount  = "bus.account"
	SeedSigning  = "bus.signing"
	SeedSystem   = "bus.system"
	SeedCore     = "bus.core"
	// RevokedName holds the revoked user keys as JSON: public key → Unix
	// seconds of the revocation.
	RevokedName = "bus.revoked"
)

// AccountName is the name in the claims of the network's account.
const AccountName = "NET"

// systemAccountName is the name in the claims of the system account.
const systemAccountName = "SYS"

// MaxUserTTL is the longest a user JWT an edge gets may live; the node
// issues a new one before it runs out.
const MaxUserTTL = 90 * 24 * time.Hour

// The chain's errors; each names no key material.
var (
	// ErrStoreLocked is a secret store whose master key does not fit: the
	// chain cannot be read, and a new one must not be made over it.
	ErrStoreLocked = errors.New("natsbus: the secret store is locked; the bus needs the master key the store was made with")
	// ErrBadUserKey is a public key that is not a NATS user key.
	ErrBadUserKey = errors.New("natsbus: not a user public key")
	// ErrRevoked is a user key that was revoked: it never gets a JWT again.
	ErrRevoked = errors.New("natsbus: the user key is revoked")
	// ErrBadTTL is a user JWT lifetime outside (0, MaxUserTTL].
	ErrBadTTL = errors.New("natsbus: a user JWT lives more than 0 and at most 90 days")
	// ErrCoreKey is an attempt to revoke the core's own user.
	ErrCoreKey = errors.New("natsbus: the core's own user cannot be revoked")
)

// SeedStore is the part of the secret store the chain needs:
// vault.Vault fits it.
type SeedStore interface {
	Get(ctx context.Context, scope secrets.Scope, name string) (string, error)
	Set(ctx context.Context, scope secrets.Scope, name, value, by string) error
}

// LockedFunc reports whether the secret store is locked.
type LockedFunc func() bool

// UserPermissions are what a user may publish and subscribe to: subjects,
// wildcards allowed. An empty list allows nothing.
type UserPermissions struct {
	Publish   []string
	Subscribe []string
}

// Chain is the node's NATS trust chain: the operator, the account NET with
// its signing key, the system account and the core's user (R-EDGE-1). Its
// seeds live in the secret store; Chain keeps the key pairs in memory and
// never returns a seed.
type Chain struct {
	store                         SeedStore
	now                           func() time.Time
	op, acc, signing, sys, core   nkeys.KeyPair
	opPub, accPub, sysPub, sigPub string
	corePub                       string

	mu      sync.Mutex
	revoked map[string]int64 // user public key → Unix seconds
}

// seedKind is a stored seed's name and the kind of key it holds.
type seedKind struct {
	name   string
	create func() (nkeys.KeyPair, error)
	prefix nkeys.PrefixByte
}

// LoadChain reads the chain from store, making each seed that is missing.
// A locked store is ErrStoreLocked: nothing is read and nothing is made.
// A stored seed of the wrong kind is an error that names the entry.
func LoadChain(ctx context.Context, store SeedStore, locked LockedFunc, now func() time.Time) (*Chain, error) {
	if locked() {
		return nil, ErrStoreLocked
	}
	c := &Chain{store: store, now: now, revoked: map[string]int64{}}
	for _, s := range []struct {
		kind seedKind
		dst  *nkeys.KeyPair
	}{
		{seedKind{SeedOperator, nkeys.CreateOperator, nkeys.PrefixByteOperator}, &c.op},
		{seedKind{SeedAccount, nkeys.CreateAccount, nkeys.PrefixByteAccount}, &c.acc},
		{seedKind{SeedSigning, nkeys.CreateAccount, nkeys.PrefixByteAccount}, &c.signing},
		{seedKind{SeedSystem, nkeys.CreateAccount, nkeys.PrefixByteAccount}, &c.sys},
		{seedKind{SeedCore, nkeys.CreateUser, nkeys.PrefixByteUser}, &c.core},
	} {
		kp, err := loadSeed(ctx, store, s.kind)
		if err != nil {
			return nil, err
		}
		*s.dst = kp
	}
	var err error
	pubs := []*string{&c.opPub, &c.accPub, &c.sigPub, &c.sysPub, &c.corePub}
	for i, kp := range []nkeys.KeyPair{c.op, c.acc, c.signing, c.sys, c.core} {
		if *pubs[i], err = kp.PublicKey(); err != nil {
			return nil, fmt.Errorf("natsbus: chain: %w", err)
		}
	}
	if err := c.loadRevoked(ctx); err != nil {
		return nil, err
	}
	return c, nil
}

// loadSeed reads one seed, or makes and stores it when there is none.
func loadSeed(ctx context.Context, store SeedStore, k seedKind) (nkeys.KeyPair, error) {
	v, err := store.Get(ctx, secrets.ScopeNode, k.name)
	if err != nil {
		return nil, fmt.Errorf("natsbus: chain: %w", err)
	}
	if v != "" {
		kp, err := nkeys.FromSeed([]byte(v))
		if err != nil {
			return nil, fmt.Errorf("natsbus: chain: %s is not a seed", k.name)
		}
		if p, _, err := nkeys.DecodeSeed([]byte(v)); err != nil || p != k.prefix {
			return nil, fmt.Errorf("natsbus: chain: %s holds a seed of the wrong kind", k.name)
		}
		return kp, nil
	}
	kp, err := k.create()
	if err != nil {
		return nil, fmt.Errorf("natsbus: chain: make %s: %w", k.name, err)
	}
	seed, err := kp.Seed()
	if err != nil {
		return nil, fmt.Errorf("natsbus: chain: make %s: %w", k.name, err)
	}
	if err := store.Set(ctx, secrets.ScopeNode, k.name, string(seed), "node"); err != nil {
		return nil, fmt.Errorf("natsbus: chain: %w", err)
	}
	return kp, nil
}

// loadRevoked reads the revocation list.
func (c *Chain) loadRevoked(ctx context.Context) error {
	v, err := c.store.Get(ctx, secrets.ScopeNode, RevokedName)
	if err != nil {
		return fmt.Errorf("natsbus: chain: %w", err)
	}
	if v == "" {
		return nil
	}
	if err := json.Unmarshal([]byte(v), &c.revoked); err != nil {
		return fmt.Errorf("natsbus: chain: %s is not a revocation list", RevokedName)
	}
	for k := range c.revoked {
		if !nkeys.IsValidPublicUserKey(k) {
			return fmt.Errorf("natsbus: chain: %s lists a key that is not a user key", RevokedName)
		}
	}
	return nil
}

// OperatorPublicKey is the operator's public key: what an edge trusts.
func (c *Chain) OperatorPublicKey() string { return c.opPub }

// AccountPublicKey is the public key of the account NET.
func (c *Chain) AccountPublicKey() string { return c.accPub }

// SystemAccountPublicKey is the public key of the system account.
func (c *Chain) SystemAccountPublicKey() string { return c.sysPub }

// CorePublicKey is the public key of the core's own user.
func (c *Chain) CorePublicKey() string { return c.corePub }

// OperatorJWT is the operator's self-signed claims, naming the system
// account: the text an external server's owner puts in its configuration.
func (c *Chain) OperatorJWT() (string, error) {
	oc := jwt.NewOperatorClaims(c.opPub)
	oc.Name = "ganglion"
	oc.SystemAccount = c.sysPub
	s, err := oc.Encode(c.op)
	if err != nil {
		return "", fmt.Errorf("natsbus: operator claims: %w", err)
	}
	return s, nil
}

// accountClaims are the claims of NET: JetStream on, the signing key that
// issues users, and the revoked user keys.
func (c *Chain) accountClaims() *jwt.AccountClaims {
	ac := jwt.NewAccountClaims(c.accPub)
	ac.Name = AccountName
	ac.SigningKeys.Add(c.sigPub)
	ac.Limits.JetStreamLimits = jwt.JetStreamLimits{MemoryStorage: -1, DiskStorage: -1, Streams: -1, Consumer: -1}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, at := range c.revoked {
		ac.RevokeAt(k, time.Unix(at, 0))
	}
	return ac
}

// AccountJWT is the claims of NET signed by the operator.
func (c *Chain) AccountJWT() (string, error) {
	s, err := c.accountClaims().Encode(c.op)
	if err != nil {
		return "", fmt.Errorf("natsbus: account claims: %w", err)
	}
	return s, nil
}

// SystemAccountJWT is the claims of the system account signed by the
// operator.
func (c *Chain) SystemAccountJWT() (string, error) {
	ac := jwt.NewAccountClaims(c.sysPub)
	ac.Name = systemAccountName
	s, err := ac.Encode(c.op)
	if err != nil {
		return "", fmt.Errorf("natsbus: system account claims: %w", err)
	}
	return s, nil
}

// IssueUser signs a JWT for the user key pub in NET with the signing key:
// named name, allowed perms, valid for ttl. A key that is not a user key,
// the core's own key, or a revoked key gets none.
func (c *Chain) IssueUser(name, pub string, perms UserPermissions, ttl time.Duration) (string, error) {
	switch {
	case !nkeys.IsValidPublicUserKey(pub):
		return "", ErrBadUserKey
	case pub == c.corePub:
		return "", ErrCoreKey
	case c.Revoked(pub):
		return "", ErrRevoked
	case ttl <= 0 || ttl > MaxUserTTL:
		return "", ErrBadTTL
	}
	uc := jwt.NewUserClaims(pub)
	uc.Name = name
	uc.IssuerAccount = c.accPub
	uc.Expires = c.now().Add(ttl).Unix()
	uc.Pub.Allow = slices.Clone(jwt.StringList(perms.Publish))
	uc.Sub.Allow = slices.Clone(jwt.StringList(perms.Subscribe))
	// Without an allow list a NATS user may do anything; an empty list must
	// allow nothing, so deny all where nothing is allowed.
	if len(uc.Pub.Allow) == 0 {
		uc.Pub.Deny.Add(">")
	}
	if len(uc.Sub.Allow) == 0 {
		uc.Sub.Deny.Add(">")
	}
	s, err := uc.Encode(c.signing)
	if err != nil {
		return "", fmt.Errorf("natsbus: user claims: %w", err)
	}
	return s, nil
}

// coreCreds is the .creds text of the core's user: all of NET, no expiry,
// issued by the signing key. It stays in memory.
func (c *Chain) coreCreds() ([]byte, error) {
	uc := jwt.NewUserClaims(c.corePub)
	uc.Name = "core"
	uc.IssuerAccount = c.accPub
	return c.creds(uc, c.core, c.signing)
}

// systemCreds is the .creds text of a system-account user made for one
// connection: a key pair nobody keeps, valid for a short time.
func (c *Chain) systemCreds() ([]byte, error) {
	kp, err := nkeys.CreateUser()
	if err != nil {
		return nil, fmt.Errorf("natsbus: system user: %w", err)
	}
	pub, err := kp.PublicKey()
	if err != nil {
		return nil, fmt.Errorf("natsbus: system user: %w", err)
	}
	uc := jwt.NewUserClaims(pub)
	uc.Name = "ganglion-push"
	uc.Expires = c.now().Add(10 * time.Minute).Unix()
	return c.creds(uc, kp, c.sys)
}

// creds signs uc with signer and formats it with the user's seed.
func (c *Chain) creds(uc *jwt.UserClaims, user, signer nkeys.KeyPair) ([]byte, error) {
	tok, err := uc.Encode(signer)
	if err != nil {
		return nil, fmt.Errorf("natsbus: user claims: %w", err)
	}
	seed, err := user.Seed()
	if err != nil {
		return nil, fmt.Errorf("natsbus: user seed: %w", err)
	}
	b, err := jwt.FormatUserConfig(tok, seed)
	if err != nil {
		return nil, fmt.Errorf("natsbus: creds: %w", err)
	}
	return b, nil
}

// Revoked reports whether the user key pub was revoked.
func (c *Chain) Revoked(pub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.revoked[pub]
	return ok
}

// revoke adds pub to the revocation list and stores it; false when it was
// there already. The account claims carry it from the next AccountJWT on.
func (c *Chain) revoke(ctx context.Context, pub string) (bool, error) {
	switch {
	case !nkeys.IsValidPublicUserKey(pub):
		return false, ErrBadUserKey
	case pub == c.corePub:
		return false, ErrCoreKey
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.revoked[pub]; ok {
		return false, nil
	}
	next := make(map[string]int64, len(c.revoked)+1)
	maps.Copy(next, c.revoked)
	next[pub] = c.now().Unix()
	b, err := json.Marshal(next)
	if err != nil {
		return false, fmt.Errorf("natsbus: revoke: %w", err)
	}
	if err := c.store.Set(ctx, secrets.ScopeNode, RevokedName, string(b), "node"); err != nil {
		return false, fmt.Errorf("natsbus: revoke: %w", err)
	}
	c.revoked = next
	return true, nil
}
