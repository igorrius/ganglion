package edge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/edgejoin"
)

// RenewFunc asks the node at address for a new user JWT, signing the request
// with the key in creds (edgejoin.Renew).
type RenewFunc func(ctx context.Context, node string, creds []byte, now time.Time) (edgejoin.Credentials, error)

// TickerFunc makes the channel that ticks every d, and the call that stops it.
type TickerFunc func(d time.Duration) (ticks <-chan time.Time, stop func())

// Renewer keeps an edge's user JWT fresh (R-EDGE-3, ADR-0116): it asks the
// node for a new JWT every Every, and once at start when the one in the file
// expires within two periods. A new JWT goes into the creds file; when its
// permissions differ from the old one's, the leaf link is redialled so it
// uses them. A renewal that fails never stops the edge: it keeps the JWT it
// has and tries again at the next tick.
type Renewer struct {
	// Node is the node's https address (see NodeURL).
	Node string
	// File is the edge's .creds file.
	File string
	// Every is how often a new JWT is asked for.
	Every time.Duration
	// Renew asks the node.
	Renew RenewFunc
	// Redial closes the leaf link so that the next one reads the file.
	Redial func() error
	// Ticker makes the clock of the loop.
	Ticker TickerFunc
	Now    func() time.Time
	Log    *slog.Logger
}

// NewRenewer is a Renewer that asks over the real network, every
// edgejoin.RenewEvery, on the real clock.
func NewRenewer(node, file string, redial func() error, log *slog.Logger) *Renewer {
	hc := &http.Client{Timeout: 30 * time.Second}
	return &Renewer{
		Node: node, File: file, Every: edgejoin.RenewEvery, Redial: redial, Log: log, Now: time.Now,
		Renew: func(ctx context.Context, node string, creds []byte, now time.Time) (edgejoin.Credentials, error) {
			return edgejoin.Renew(ctx, hc, node, creds, now)
		},
		Ticker: func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		},
	}
}

// Name implements node.Component.
func (r *Renewer) Name() string { return "edge-renew" }

// Run renews until ctx ends.
func (r *Renewer) Run(ctx context.Context) error {
	if r.dueAtStart() {
		r.renew(ctx)
	}
	ticks, stop := r.Ticker(r.Every)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
			r.renew(ctx)
		}
	}
}

// dueAtStart reports whether the JWT in the file expires within two periods
// (or cannot be read: the renewal then says what is wrong).
func (r *Renewer) dueAtStart() bool {
	raw, err := os.ReadFile(r.File)
	if err != nil {
		return true
	}
	c, err := natsbus.ReadCreds(raw)
	if err != nil {
		return true
	}
	return !c.Expires.IsZero() && c.Expires.Before(r.Now().Add(2*r.Every))
}

// renew does one renewal; its outcome is logged, never returned.
func (r *Renewer) renew(ctx context.Context) {
	old, err := os.ReadFile(r.File)
	if err != nil {
		r.Log.Error("edge: renew: the credentials file cannot be read", "error", err)
		return
	}
	got, err := r.Renew(ctx, r.Node, old, r.Now())
	if err != nil {
		r.failed(err)
		return
	}
	changed := permissionsChanged(old, got.Creds)
	if err := WriteFileAtomic(r.File, got.Creds, 0o600); err != nil {
		r.Log.Error("edge: renew: the credentials file cannot be written", "error", err)
		return
	}
	r.Log.Info("edge: credentials renewed", "expires", got.Expires.UTC().Format(time.RFC3339), "permissions_changed", changed)
	if !changed {
		return
	}
	if err := r.Redial(); err != nil {
		r.Log.Warn("edge: renew: the link could not be redialled; the new permissions apply at its next dial", "error", err)
	}
}

// failed logs a failed renewal: a refusal that says the edge is revoked or
// unknown is an error to act on; a network or server trouble is a warning.
func (r *Renewer) failed(err error) {
	var ref *edgejoin.RefusedError
	if errors.As(err, &ref) && (ref.Status == http.StatusForbidden || ref.Status == http.StatusUnauthorized) {
		r.Log.Error("edge: renew: the node refuses this edge; it keeps the credentials it has", "error", err)
		return
	}
	r.Log.Warn("edge: renew failed; trying again at the next tick", "error", err)
}

// permissionsChanged reports whether the two creds files hold JWTs with
// different permission lists. A file that cannot be read counts as changed.
func permissionsChanged(old, now []byte) bool {
	a, err1 := natsbus.ReadCreds(old)
	b, err2 := natsbus.ReadCreds(now)
	if err1 != nil || err2 != nil {
		return true
	}
	same := func(x, y []string) bool {
		x, y = slices.Clone(x), slices.Clone(y)
		slices.Sort(x)
		slices.Sort(y)
		return slices.Equal(x, y)
	}
	return !same(a.Permissions.Publish, b.Permissions.Publish) || !same(a.Permissions.Subscribe, b.Permissions.Subscribe)
}

// NodeURL is the node's https address from the bus address of edge.url:
// wss://host/bus is https://host, ws://host/bus is http://host (a test
// node). Any other scheme has no address to ask.
func NodeURL(bus string) (string, error) {
	u, err := url.Parse(bus)
	if err != nil || u.Hostname() == "" {
		return "", errors.New("edge: the bus address is not an address")
	}
	switch u.Scheme {
	case "wss":
		u.Scheme = "https"
	case "ws":
		u.Scheme = "http"
	default:
		return "", fmt.Errorf("edge: the %s bus address has no https address to renew at", u.Scheme)
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/bus")
	u.RawQuery, u.Fragment, u.User = "", "", nil
	return u.String(), nil
}

// WriteFileAtomic writes data to path through a temporary file in the same
// directory, with mode perm, so a reader (the leaf link's dial) sees the old
// file or the new one, never half of it. The directory is made, 0700, when it
// is missing.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }() // a no-op after the rename
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
