// Package edgejoin is the core's side of connecting a computer as an edge
// (R-EDGE-2, R-EDGE-3, R-EDGE-5, ADR-0116): the one-time codes, the list of
// joined edges, the user JWT each gets and renews, and Revoke. It speaks
// neither chat nor HTTP; handler.go is the two HTTP routes and client.go is
// the edge's side.
package edgejoin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/secrets"
)

// Bounds of the join (ADR-0116).
const (
	// CodeTTL is how long a code lives.
	CodeTTL = 10 * time.Minute
	// JWTTTL is the life of an edge's user JWT; the edge renews at
	// RenewEvery.
	JWTTTL = 24 * time.Hour
	// RenewEvery is how often an edge asks for a new JWT.
	RenewEvery = time.Hour
	// MaxClockSkew is how far a renewal's time may be from the core's.
	MaxClockSkew = time.Minute
	// maxCodes is how many unused codes live at once.
	maxCodes = 8
	// maxEdges is how many edges the list holds.
	maxEdges = 64
	// FailBurst and FailEvery are the limiter's: five failed attempts, then
	// one more every twelve seconds.
	FailBurst = 5
	FailEvery = 12 * time.Second
)

// EdgesName is the entry of the secret store that holds the joined edges
// (reserved with the chain's, secrets.Reserved).
const EdgesName = "bus.edges"

// The errors. None names a code, a key or a seed.
var (
	// ErrBadCode is a code that is wrong, expired or used: one answer for
	// all, so a stranger learns nothing about which.
	ErrBadCode = errors.New("edgejoin: the code is not valid")
	// ErrLimited is a request refused before it was looked at: too many
	// failed attempts.
	ErrLimited = errors.New("edgejoin: too many attempts")
	// ErrBadKey is a public key that is not an NATS user key, or is the
	// core's own.
	ErrBadKey = errors.New("edgejoin: not a usable user key")
	// ErrRevoked is a revoked key.
	ErrRevoked = errors.New("edgejoin: the key is revoked")
	// ErrKeyTaken is a public key a joined edge holds already.
	ErrKeyTaken = errors.New("edgejoin: a joined edge has this key already")
	// ErrNameTaken is an edge name that a joined edge holds.
	ErrNameTaken = errors.New("edgejoin: an edge has this name already")
	// ErrUnknownEdge is a name or key no joined edge has.
	ErrUnknownEdge = errors.New("edgejoin: no such edge")
	// ErrStale is an approval of a card whose permissions are not those
	// the node would give now.
	ErrStale = errors.New("edgejoin: the permissions changed since the card")
	// ErrTooMany is a node with the most edges it keeps, or the most codes.
	ErrTooMany = errors.New("edgejoin: too many edges or codes")
	// ErrBadSignature is a renewal that the key did not sign, or one whose
	// time is off.
	ErrBadSignature = errors.New("edgejoin: the request is not signed by the key in time")
)

// Chain is what the join takes from the trust chain (natsbus.Chain).
type Chain interface {
	IssueUser(name, pub string, perms natsbus.UserPermissions, ttl time.Duration) (string, error)
	OperatorPublicKey() string
	CorePublicKey() string
	Revoked(pub string) bool
}

// Bus is what Revoke takes from the bus (natsbus.Bus).
type Bus interface {
	Revoke(ctx context.Context, pub string) error
	UnsourceEdge(ctx context.Context, edge string) error
}

// Store is the secret store (vault.Vault).
type Store interface {
	Get(ctx context.Context, scope secrets.Scope, name string) (string, error)
	Set(ctx context.Context, scope secrets.Scope, name, value, by string) error
}

// NeedsFunc is what the links ask of an edge's user (natsbus.EdgeNeeds); the
// composition root reads the node's file. It runs at every card, join and
// renewal, so a change of the links reaches the edge.
type NeedsFunc func(edge string) (natsbus.EdgeNeeds, error)

// Joined is told of every join: the name and the time.
type Joined func(ctx context.Context, e Edge)

// Edge is a joined edge as the list keeps it.
type Edge struct {
	Name   string    `json:"name"`
	Key    string    `json:"key"`
	Joined time.Time `json:"joined"`
	// Digest is the digest of the permissions of the JWT last issued.
	Digest string `json:"digest"`
}

// Proposal is what a card shows: the name and the permissions the edge
// would get, and the digest an approval must carry.
type Proposal struct {
	Name        string
	Permissions natsbus.UserPermissions
	Digest      string
}

// Answer is what a join or a renewal gives the edge.
type Answer struct {
	Name     string    `json:"name"`
	JWT      string    `json:"jwt"`
	Operator string    `json:"operator"`
	BusURL   string    `json:"bus_url,omitempty"`
	Expires  time.Time `json:"expires"`
}

// Config is what the service needs.
type Config struct {
	Chain  Chain
	Bus    Bus
	Store  Store
	Needs  NeedsFunc
	Joined Joined
	// BusURL is the edge's bus address when the node knows its public one;
	// "" leaves it to the edge.
	BusURL string
	Now    func() time.Time
}

type code struct {
	name    string
	digest  string
	expires time.Time
}

// Service is the join. It is safe for concurrent use.
type Service struct {
	cfg Config
	now func() time.Time

	mu    sync.Mutex
	codes map[[32]byte]code // by SHA-256 of the code
	tok   float64
	last  time.Time

	// edgesMu makes every read-change-write of the list one step.
	edgesMu sync.Mutex
}

// New makes the service.
func New(cfg Config) *Service {
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Service{cfg: cfg, now: now, codes: map[[32]byte]code{}, tok: FailBurst, last: now()}
}

// Digest is the digest of permissions: equal permissions have equal ones.
func Digest(p natsbus.UserPermissions) string {
	h := sha256.New()
	for _, l := range [][]string{p.Publish, p.Subscribe} {
		for _, s := range l {
			_, _ = fmt.Fprintf(h, "%d:%s\n", len(s), s)
		}
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (s *Service) permissions(name string) (natsbus.UserPermissions, error) {
	var n natsbus.EdgeNeeds
	if s.cfg.Needs != nil {
		var err error
		if n, err = s.cfg.Needs(name); err != nil {
			return natsbus.UserPermissions{}, err
		}
	}
	return natsbus.EdgePermissions(name, n)
}

// Propose is the card of a new edge: its name checked, and the permissions
// it would get. It makes no code.
func (s *Service) Propose(ctx context.Context, name string) (Proposal, error) {
	if err := bus.CheckEdgeName(name); err != nil {
		return Proposal{}, err
	}
	edges, err := s.Edges(ctx)
	if err != nil {
		return Proposal{}, err
	}
	if slices.ContainsFunc(edges, func(e Edge) bool { return e.Name == name }) {
		return Proposal{}, ErrNameTaken
	}
	p, err := s.permissions(name)
	if err != nil {
		return Proposal{}, err
	}
	return Proposal{Name: name, Permissions: p, Digest: Digest(p)}, nil
}

// Approve makes the one-time code for the card the owner read: it is for
// that name, and for permissions with that digest. The code is returned once
// and kept only as a hash; asking again for the same name replaces it.
func (s *Service) Approve(ctx context.Context, name, digest string) (string, time.Time, error) {
	prop, err := s.Propose(ctx, name)
	if err != nil {
		return "", time.Time{}, err
	}
	if subtle.ConstantTimeCompare([]byte(prop.Digest), []byte(digest)) != 1 {
		return "", time.Time{}, ErrStale
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, fmt.Errorf("edgejoin: code: %w", err)
	}
	plain := formatCode(raw)
	h := hashCode(plain)
	exp := s.now().Add(CodeTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropExpired()
	for k, c := range s.codes {
		if c.name == name {
			delete(s.codes, k)
		}
	}
	if len(s.codes) >= maxCodes {
		return "", time.Time{}, ErrTooMany
	}
	s.codes[h] = code{name: name, digest: prop.Digest, expires: exp}
	return plain, exp, nil
}

// Join is the edge's first request: the code and the public key of the user
// it made. It gives the user JWT. Every attempt takes a token of the
// limiter up front and a join that succeeds gives it back, so parallel
// attempts never pass on one token. A code is used only when the key is
// usable, and is spent once taken, whatever comes after.
func (s *Service) Join(ctx context.Context, plain, pub string) (Answer, error) {
	if !s.reserve() {
		return Answer{}, ErrLimited
	}
	ans, err := s.join(ctx, plain, pub)
	if err == nil {
		s.refund()
	}
	return ans, err
}

func (s *Service) join(ctx context.Context, plain, pub string) (Answer, error) {
	if !natsbus.IsUserKey(pub) || pub == s.cfg.Chain.CorePublicKey() {
		return Answer{}, ErrBadKey
	}
	if s.cfg.Chain.Revoked(pub) {
		return Answer{}, ErrRevoked
	}
	s.edgesMu.Lock()
	defer s.edgesMu.Unlock()
	edges, err := s.readEdges(ctx)
	if err != nil {
		return Answer{}, err
	}
	// One key is one edge: two names under one key would make a renewal
	// pick one and a revoke cut both.
	for _, e := range edges {
		if e.Key == pub {
			return Answer{}, ErrKeyTaken
		}
	}
	c, ok := s.take(plain)
	if !ok {
		return Answer{}, ErrBadCode
	}
	if _, taken := edges[c.name]; taken {
		return Answer{}, ErrNameTaken
	}
	if len(edges) >= maxEdges {
		return Answer{}, ErrTooMany
	}
	perms, err := s.permissions(c.name)
	if err != nil {
		return Answer{}, err
	}
	// The edge gets exactly what the owner read on the card, or nothing.
	if subtle.ConstantTimeCompare([]byte(Digest(perms)), []byte(c.digest)) != 1 {
		return Answer{}, ErrStale
	}
	ans, err := s.issue(c.name, pub, perms)
	if err != nil {
		return Answer{}, err
	}
	e := Edge{Name: c.name, Key: pub, Joined: s.now().UTC(), Digest: Digest(perms)}
	edges[c.name] = e
	if err := s.writeEdges(ctx, edges); err != nil {
		return Answer{}, err
	}
	if s.cfg.Joined != nil {
		s.cfg.Joined(ctx, e)
	}
	return ans, nil
}

// RenewMessage is what an edge signs to renew: the key and the time, in
// Unix seconds.
func RenewMessage(pub string, at int64) []byte { return fmt.Appendf(nil, "renew %s %d", pub, at) }

// Renew gives a joined edge a new JWT with the permissions as they are now.
// The request is signed with the edge's key (sig is base64 of the NATS
// signature) and its time is within MaxClockSkew. The reply goes to whoever
// has the signature, who still cannot use it without the seed. Renewal is
// not under the join's limiter: nothing in it can be guessed (a signature
// needs the seed), and a stranger's bad requests must not hold back a
// joined edge's renewal; the listener's own limits bound the work.
func (s *Service) Renew(ctx context.Context, pub string, at int64, sig string) (Answer, error) {
	raw, err := base64.StdEncoding.DecodeString(sig)
	d := s.now().Sub(time.Unix(at, 0))
	if err != nil || d > MaxClockSkew || d < -MaxClockSkew || natsbus.VerifyUser(pub, RenewMessage(pub, at), raw) != nil {
		return Answer{}, ErrBadSignature
	}
	if s.cfg.Chain.Revoked(pub) {
		return Answer{}, ErrRevoked
	}
	s.edgesMu.Lock()
	defer s.edgesMu.Unlock()
	edges, err := s.readEdges(ctx)
	if err != nil {
		return Answer{}, err
	}
	var e Edge
	found := false
	for _, x := range edges {
		if x.Key == pub {
			e, found = x, true
		}
	}
	if !found {
		return Answer{}, ErrUnknownEdge
	}
	perms, err := s.permissions(e.Name)
	if err != nil {
		return Answer{}, err
	}
	ans, err := s.issue(e.Name, pub, perms)
	if err != nil {
		return Answer{}, err
	}
	if d := Digest(perms); d != e.Digest {
		e.Digest = d
		edges[e.Name] = e
		if err := s.writeEdges(ctx, edges); err != nil {
			return Answer{}, err
		}
	}
	return ans, nil
}

// Revoke puts the edge's key into the account's revocations (the server
// drops its connection and refuses it), stops sourcing its events and takes
// it off the list. Run again after a failure it finishes the rest; an edge
// not on the list is ErrUnknownEdge.
func (s *Service) Revoke(ctx context.Context, name string) error {
	s.edgesMu.Lock()
	defer s.edgesMu.Unlock()
	edges, err := s.readEdges(ctx)
	if err != nil {
		return err
	}
	e, ok := edges[name]
	if !ok {
		return ErrUnknownEdge
	}
	if err := s.cfg.Bus.Revoke(ctx, e.Key); err != nil {
		return fmt.Errorf("edgejoin: revoke %s: %w", name, err)
	}
	if err := s.cfg.Bus.UnsourceEdge(ctx, name); err != nil {
		return fmt.Errorf("edgejoin: revoke %s: %w", name, err)
	}
	delete(edges, name)
	s.dropCodes(name)
	return s.writeEdges(ctx, edges)
}

// Edges are the joined edges, by name.
func (s *Service) Edges(ctx context.Context) ([]Edge, error) {
	s.edgesMu.Lock()
	defer s.edgesMu.Unlock()
	m, err := s.readEdges(ctx)
	if err != nil {
		return nil, err
	}
	out := slices.SortedFunc(maps.Values(m), func(a, b Edge) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

// issue signs the JWT and makes the answer.
func (s *Service) issue(name, pub string, perms natsbus.UserPermissions) (Answer, error) {
	tok, err := s.cfg.Chain.IssueUser(name, pub, perms, JWTTTL)
	if err != nil {
		switch {
		case errors.Is(err, natsbus.ErrRevoked):
			return Answer{}, ErrRevoked
		case errors.Is(err, natsbus.ErrBadUserKey), errors.Is(err, natsbus.ErrCoreKey):
			return Answer{}, ErrBadKey
		}
		return Answer{}, err
	}
	return Answer{Name: name, JWT: tok, Operator: s.cfg.Chain.OperatorPublicKey(), BusURL: s.cfg.BusURL,
		Expires: s.now().Add(JWTTTL).UTC()}, nil
}

func (s *Service) readEdges(ctx context.Context) (map[string]Edge, error) {
	v, err := s.cfg.Store.Get(ctx, secrets.ScopeNode, EdgesName)
	if err != nil {
		return nil, fmt.Errorf("edgejoin: read the edges: %w", err)
	}
	m := map[string]Edge{}
	if v == "" {
		return m, nil
	}
	if err := json.Unmarshal([]byte(v), &m); err != nil {
		return nil, fmt.Errorf("edgejoin: the list of edges is unreadable")
	}
	return m, nil
}

func (s *Service) writeEdges(ctx context.Context, m map[string]Edge) error {
	b, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("edgejoin: write the edges: %w", err)
	}
	if err := s.cfg.Store.Set(ctx, secrets.ScopeNode, EdgesName, string(b), "node"); err != nil {
		return fmt.Errorf("edgejoin: write the edges: %w", err)
	}
	return nil
}

// take removes and returns the live code that plain is.
func (s *Service) take(plain string) (code, bool) {
	h := hashCode(plain)
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.codes[h]
	if !ok {
		return code{}, false
	}
	delete(s.codes, h)
	if !s.now().Before(c.expires) {
		return code{}, false
	}
	return c, true
}

func (s *Service) dropExpired() {
	now := s.now()
	for k, c := range s.codes {
		if !now.Before(c.expires) {
			delete(s.codes, k)
		}
	}
}

func (s *Service) dropCodes(name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, c := range s.codes {
		if c.name == name {
			delete(s.codes, k)
		}
	}
}

// refill adds the tokens the time since the last look earned.
func (s *Service) refill() {
	now := s.now()
	s.tok = min(FailBurst, s.tok+float64(now.Sub(s.last))/float64(FailEvery))
	s.last = now
}

// reserve takes a token for an attempt; false when none is left.
func (s *Service) reserve() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refill()
	if s.tok < 1 {
		return false
	}
	s.tok--
	return true
}

// refund gives back the token of an attempt that succeeded.
func (s *Service) refund() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refill()
	s.tok = min(FailBurst, s.tok+1)
}

// The code's alphabet is Crockford base32: no I, L, O, U.
var enc = base32.NewEncoding("0123456789ABCDEFGHJKMNPQRSTVWXYZ").WithPadding(base32.NoPadding)

// formatCode writes 16 bytes as 26 characters in groups of four, the last
// of two.
func formatCode(raw []byte) string {
	s := enc.EncodeToString(raw)
	var parts []string
	for len(s) > 4 {
		parts = append(parts, s[:4])
		s = s[4:]
	}
	return strings.Join(append(parts, s), "-")
}

// hashCode is the SHA-256 of a code as typed: case and dashes are not part
// of it, so a code read aloud or pasted with spaces still fits. A string of
// another length hashes to something no live code has.
func hashCode(s string) [32]byte {
	s = strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(s))
	return sha256.Sum256([]byte(s))
}
