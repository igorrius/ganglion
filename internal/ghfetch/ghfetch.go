// Package ghfetch is the HTTP client for GitHub's release downloads, shared
// by the module store's source and the core updater (ADR-0041, ADR-0081).
// Only GitHub's hosts are reached, only over https on port 443, only at
// public addresses, with redirects checked against the same hosts and
// every body bounded.
package ghfetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/igorrius/ganglion/web"
)

const maxRedirects = 5

// DefaultHosts are GitHub's API and the hosts its release downloads
// redirect to.
func DefaultHosts() []string {
	return []string{"api.github.com", "github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com"}
}

// StatusError is a response other than 200. A caller that must tell a
// missing release from a failed read looks for it with errors.As.
type StatusError struct {
	Path   string
	Code   int
	Status string
}

func (e *StatusError) Error() string { return fmt.Sprintf("release: %s: %s", e.Path, e.Status) }

// Client fetches from GitHub's hosts.
type Client struct {
	hosts []string
	hc    *http.Client
	plain bool // tests: an httptest server is plain http on a port
}

// Option configures a Client.
type Option func(*Client)

// WithHosts replaces the hosts that may be reached (tests).
func WithHosts(hosts ...string) Option { return func(c *Client) { c.hosts = hosts } }

// WithTransport replaces the transport, keeping the host and redirect
// rules, and lets a test server's plain http through (tests).
func WithTransport(rt http.RoundTripper) Option {
	return func(c *Client) { c.hc.Transport, c.plain = rt, true }
}

// New is a client for GitHub's hosts.
func New(opts ...Option) *Client {
	c := &Client{hosts: DefaultHosts()}
	c.hc = &http.Client{
		Transport: &http.Transport{Proxy: nil, DialContext: web.PublicDial(10 * time.Second), TLSHandshakeTimeout: 10 * time.Second,
			MaxResponseHeaderBytes: 64 << 10},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return errors.New("too many redirects")
			}
			return c.check(req.URL)
		},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Allowed says whether host is one of the hosts that may be reached.
func (c *Client) Allowed(host string) bool { return slices.Contains(c.hosts, strings.ToLower(host)) }

// check refuses a URL that is not https on the default port of one of
// GitHub's hosts.
func (c *Client) check(u *url.URL) error {
	if u.Scheme != "https" || (u.Port() != "" && u.Port() != "443") {
		if !c.plain {
			return fmt.Errorf("release: %s: only https on port 443 is fetched", u.Redacted())
		}
	}
	if !c.Allowed(u.Hostname()) {
		return fmt.Errorf("release: %s is not one of GitHub's hosts", u.Hostname())
	}
	return nil
}

// Get returns the body of raw, failing when it is longer than limit bytes.
func (c *Client) Get(ctx context.Context, raw string, limit int64) ([]byte, error) {
	var b limited
	if _, err := c.Copy(ctx, raw, &b, limit); err != nil {
		return nil, err
	}
	return b.data, nil
}

// limited collects what Copy writes.
type limited struct{ data []byte }

func (l *limited) Write(p []byte) (int, error) { l.data = append(l.data, p...); return len(p), nil }

// Copy streams the body of raw into w and returns the bytes written. A
// body longer than limit bytes is an error; w has then received limit+1
// bytes at most, so the caller discards what it holds.
func (c *Client) Copy(ctx context.Context, raw string, w io.Writer, limit int64) (int64, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return 0, fmt.Errorf("release: %w", err)
	}
	if err := c.check(u); err != nil {
		return 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return 0, fmt.Errorf("release: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, fmt.Errorf("release: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 0, &StatusError{Path: u.Path, Code: resp.StatusCode, Status: resp.Status}
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return n, fmt.Errorf("release: %w", err)
	}
	if n > limit {
		return n, fmt.Errorf("release: %s is over %s", u.Path, sizeText(limit))
	}
	return n, nil
}

func sizeText(n int64) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%d MiB", n>>20)
	}
	return fmt.Sprintf("%d bytes", n)
}
