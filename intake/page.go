package intake

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed" // the page this release pins
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// pageV1 is version 1 of the intake page, as the project's site publishes
// it at intake/v1/ (R-INT-3). intake/published.sha256 holds its hash, and a
// published version never changes (a CI check).
//
//go:embed page/v1/index.html
var pageV1 []byte

// DefaultPage is the address of the page on the project's site. The
// redirect address of an OAuth sign-in and the base of a secret link.
const DefaultPage = "https://igorrius.github.io/ganglion/intake/v1/"

// Page returns a copy of the page this release embeds.
func Page() []byte { return bytes.Clone(pageV1) }

// PageCheckFunc reports whether the hosted intake page is the one this
// release embeds: nil when it is, ErrPageChanged or ErrPageUnchecked when
// not.
type PageCheckFunc func(ctx context.Context) error

// The ways the hosted page fails its check.
var (
	// ErrPageChanged: the hosted page is not the copy in this release.
	// Handing secrets over stops until it is (R-INT-3).
	ErrPageChanged = errors.New("intake: the intake page has changed")
	// ErrPageUnchecked: the hosted page could not be fetched, so it could
	// not be compared.
	ErrPageUnchecked = errors.New("intake: the intake page could not be checked")
)

// PageOutcome is the result of one check of the hosted page: a metric label.
type PageOutcome string

// Outcomes of a page check.
const (
	PageSame      PageOutcome = "same"
	PageChanged   PageOutcome = "changed"
	PageUnchecked PageOutcome = "unchecked"
)

// PageCheckConfig holds the check's address and limits.
type PageCheckConfig struct {
	URL      string        // the hosted page (mcp.page); https only
	Timeout  time.Duration // the whole fetch
	MaxBytes int64         // a larger answer is not the page
}

// DefaultPageCheckConfig is the project's site, 10 seconds and 64 KB.
func DefaultPageCheckConfig() PageCheckConfig {
	return PageCheckConfig{URL: DefaultPage, Timeout: 10 * time.Second, MaxBytes: 64 << 10}
}

// PageChecker fetches the hosted page and compares it, byte for byte, with
// the copy in this release.
type PageChecker struct {
	client *http.Client
	cfg    PageCheckConfig
	want   [sha256.Size]byte
	tel    pageTelemetry
}

// NewPageChecker returns a checker that fetches through client. The client
// is the caller's to bound to public addresses (web.PublicDial) and to
// verify TLS.
func NewPageChecker(client *http.Client, cfg PageCheckConfig) *PageChecker {
	return &PageChecker{client: client, cfg: cfg, want: sha256.Sum256(pageV1), tel: newPageTelemetry()}
}

// Check fetches the hosted page and compares it with the embedded copy
// (R-INT-3). It returns nil when they are the same, ErrPageChanged when
// they differ, and an error wrapping ErrPageUnchecked when the page could
// not be fetched: a non-https address, a failed request, a status other
// than 200, or an answer over MaxBytes. It is a PageCheckFunc.
func (c *PageChecker) Check(ctx context.Context) error {
	err := c.check(ctx)
	o := PageSame
	switch {
	case errors.Is(err, ErrPageChanged):
		o = PageChanged
	case err != nil:
		o = PageUnchecked
	}
	c.tel.checked(ctx, o)
	return err
}

func (c *PageChecker) check(ctx context.Context) error {
	body, err := c.fetch(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPageUnchecked, err)
	}
	if sha256.Sum256(body) != c.want {
		return ErrPageChanged
	}
	return nil
}

// fetch reads the hosted page, bounded in time and size.
func (c *PageChecker) fetch(ctx context.Context) ([]byte, error) {
	u, err := url.Parse(c.cfg.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("the page address is not https")
	}
	ctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, c.cfg.MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	if int64(len(body)) > c.cfg.MaxBytes {
		return nil, fmt.Errorf("over %d bytes", c.cfg.MaxBytes)
	}
	return body, nil
}

// SecretLink is the address the admin opens to answer the secret request
// r: the page, and after "#" (which a browser never sends to a server) the
// format version, the request id, the secret's name, the host the value is
// for ("" for the node's own use) and the request's public key. page is
// the configured page address (DefaultPage).
func SecretLink(page string, r SecretRequest, host string) string {
	v := url.Values{}
	v.Set("v", "1")
	v.Set("mode", "secret")
	v.Set("id", r.ID)
	v.Set("name", r.Name)
	if host != "" {
		v.Set("host", host)
	}
	v.Set("pk", b64.EncodeToString(r.PublicKey))
	return page + "#" + v.Encode()
}
