// Package web reads the web for the agent (spec 7.11, ADR-0035):
// web.fetch's fetcher, with its guards against reaching the node's own
// network, and the search backends.
//
// Remote content leaves this package only as a Page's text. Every error
// is a short fixed text (a status code, a content type, a limit), never a
// response body or an address a server sent, so a page cannot speak to
// the model through an error (R-WEB-1, ADR-0031).
package web

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/internal/urlnorm"
)

// Errors of a fetch; callers decide on them with errors.Is.
var (
	ErrNotAllowed       = errors.New("web.fetch: only http and https addresses without credentials can be fetched")
	ErrPrivateAddress   = errors.New("web.fetch: the address is not public (loopback, private, link-local or metadata)")
	ErrOutsideGrant     = errors.New("web.fetch: the address is outside the hosts granted to this task")
	ErrTooManyRedirects = errors.New("web.fetch: too many redirects")
	ErrTimeout          = errors.New("web.fetch: timed out")
	ErrNotFound         = errors.New("web.fetch: the host name does not resolve")
	ErrRequest          = errors.New("web.fetch: the request failed")
	ErrStatus           = errors.New("web.fetch: the server answered")
	ErrTooLarge         = errors.New("web.fetch: the response is too large")
	ErrContentType      = errors.New("web.fetch: unsupported content type")
	ErrConvert          = errors.New("web.fetch: the page could not be converted to text")
)

// FetchConfig bounds one fetch. Zero means zero: a zero MaxRedirects
// follows no redirect.
type FetchConfig struct {
	MaxBytes     int64         // the largest response body read
	MaxHTMLBytes int64         // the largest HTML page; html2md's memory limit is sized for it (ADR-0035)
	Timeout      time.Duration // the request and the body, redirects included; not the conversion
	DialTimeout  time.Duration // connecting to one address of a host
	MaxRedirects int           // redirects followed
	MaxChars     int           // runes of text returned; the rest is cut
}

// DefaultFetchConfig returns 2 MiB (1 MiB of HTML), 15 s (5 s to
// connect to one address), 5 redirects and 20,000 characters.
func DefaultFetchConfig() FetchConfig {
	return FetchConfig{MaxBytes: 2 << 20, MaxHTMLBytes: 1 << 20, Timeout: 15 * time.Second, DialTimeout: 5 * time.Second, MaxRedirects: 5, MaxChars: 20_000}
}

// Converter turns a page's HTML into its title and Markdown: the html2md
// module (ModuleConverter).
type Converter interface {
	Convert(ctx context.Context, url, html string) (title, md string, err error)
}

// Page is a fetched page as text.
type Page struct {
	URL         string // the address read, after redirects, normalised
	Title       string // HTML only
	Text        string // Markdown for HTML; JSON and plain text as they came
	ContentType string // the media type, such as "text/html"
	Truncated   bool   // Text was cut at MaxChars
}

// Scope tells which hosts (with a non-default port, as urlnorm writes
// them) a fetch may reach, redirects included.
type Scope func(host string) bool

// AnyHost is the scope outside a scheduled task: any public host.
func AnyHost(string) bool { return true }

// OnlyHosts is the scope inside a scheduled task's grant: the granted
// hosts only, compared as the policy compares them (R-POL-8).
func OnlyHosts(hosts []string) Scope {
	return func(host string) bool {
		for _, h := range hosts {
			if strings.EqualFold(h, host) {
				return true
			}
		}
		return false
	}
}

// FetchOption changes how a Fetcher reaches the network.
type FetchOption func(*fetchOptions)

type fetchOptions struct {
	resolve Resolver
	connect ConnectFunc
}

// WithResolver resolves host names with r instead of the system resolver.
func WithResolver(r Resolver) FetchOption { return func(o *fetchOptions) { o.resolve = r } }

// WithConnect opens connections to checked addresses with c (tests reach
// local servers this way; the address check still runs before it).
func WithConnect(c ConnectFunc) FetchOption { return func(o *fetchOptions) { o.connect = c } }

// Fetcher is web.fetch's reader. It keeps no cookies and uses no proxy.
type Fetcher struct {
	cfg       FetchConfig
	conv      Converter
	transport *http.Transport
	in        instruments
}

// NewFetcher returns a fetcher with cfg's limits that converts HTML with
// conv.
func NewFetcher(cfg FetchConfig, conv Converter, opts ...FetchOption) *Fetcher {
	o := fetchOptions{resolve: net.DefaultResolver, connect: directConnect}
	for _, opt := range opts {
		opt(&o)
	}
	return &Fetcher{cfg: cfg, conv: conv, in: newInstruments(), transport: &http.Transport{
		Proxy:                  nil, // never a proxy from the environment: it would see every address
		DialContext:            dialer{resolve: o.resolve, connect: o.connect, timeout: cfg.DialTimeout}.dial,
		ForceAttemptHTTP2:      true,
		TLSHandshakeTimeout:    10 * time.Second,
		MaxIdleConns:           8,
		IdleConnTimeout:        30 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
	}}
}

// Fetch reads rawURL within scope: http(s) only, no credentials, public
// addresses only (checked on the resolved address of every connection,
// redirects included), the limits of the config. HTML is converted to
// Markdown; JSON and plain text pass through; any other type is an error
// naming it. The address fetched is exactly the normalised one the
// policy checked (urlnorm), so the two cannot read it differently.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string, scope Scope) (Page, error) {
	p, err := f.fetch(ctx, rawURL, scope)
	f.in.observe(ctx, err)
	return p, err
}

// fetch is one Fetch, before its metrics.
func (f *Fetcher) fetch(ctx context.Context, rawURL string, scope Scope) (Page, error) {
	u, err := target(rawURL, scope)
	if err != nil {
		return Page{}, err
	}
	// The time limit is the server's: the request and the body. Converting
	// the page is the node's own work, bounded by the module's limit, so the
	// first page's compile of html2md is not charged to the server (#207).
	netCtx, cancel := context.WithTimeout(ctx, f.cfg.Timeout)
	defer cancel()
	resp, err := f.get(netCtx, u, scope)
	if err != nil {
		return Page{}, f.requestError(netCtx, err)
	}
	defer func() { _ = resp.Body.Close() }()
	kind, mediaType, body, err := f.read(netCtx, resp)
	if err != nil {
		return Page{}, err
	}
	final, _ := urlnorm.Normalize(resp.Request.URL.String())
	return f.page(ctx, final, kind, mediaType, decode(body, resp.Header.Get("Content-Type"), kind == kindHTML))
}

// target is the normalised address to fetch, checked against the rules
// that do not need the network.
func target(raw string, scope Scope) (*url.URL, error) {
	norm, ok := urlnorm.Normalize(raw)
	if !ok {
		return nil, ErrNotAllowed
	}
	u, err := url.Parse(norm)
	if err != nil || u.User != nil {
		return nil, ErrNotAllowed
	}
	if !scope(u.Host) {
		return nil, ErrOutsideGrant
	}
	return u, nil
}

func (f *Fetcher) get(ctx context.Context, u *url.URL, scope Scope) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, ErrNotAllowed
	}
	req.Header.Set("User-Agent", "ganglion (+https://github.com/igorrius/ganglion)")
	req.Header.Set("Accept", "text/html, application/xhtml+xml, application/json;q=0.9, text/plain;q=0.8")
	client := &http.Client{Transport: f.transport, CheckRedirect: f.redirects(scope)} // no Jar: no cookies
	return client.Do(req)
}

// redirects checks each redirect as the first address is checked; the
// connection to it is checked by the dialer.
func (f *Fetcher) redirects(scope Scope) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > f.cfg.MaxRedirects {
			return ErrTooManyRedirects
		}
		if _, err := target(req.URL.String(), scope); err != nil {
			return err
		}
		req.Header.Del("Referer") // the first address may carry a token in its query
		return nil
	}
}

// requestError turns a failed request into a fixed text: the URL a
// redirect named and the server's words are left out.
func (f *Fetcher) requestError(ctx context.Context, err error) error {
	for _, e := range []error{ErrNotAllowed, ErrPrivateAddress, ErrOutsideGrant, ErrTooManyRedirects} {
		if errors.Is(err, e) {
			return e
		}
	}
	var netErr net.Error
	var dnsErr *net.DNSError
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.As(err, &netErr) && netErr.Timeout():
		return fmt.Errorf("%w after %v", ErrTimeout, f.cfg.Timeout)
	case errors.Is(ctx.Err(), context.Canceled):
		return fmt.Errorf("web.fetch: %w", context.Canceled)
	case errors.As(err, &dnsErr):
		return ErrNotFound
	default:
		return ErrRequest
	}
}

// read checks the status and the type and reads the body within the size
// limit.
func (f *Fetcher) read(ctx context.Context, resp *http.Response) (kind, string, []byte, error) {
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return 0, "", nil, fmt.Errorf("%w %d", ErrStatus, resp.StatusCode)
	}
	header := resp.Header.Get("Content-Type")
	mediaType := parseMediaType(header)
	if header != "" && kinds[mediaType] == 0 && !strings.HasSuffix(mediaType, "+json") {
		return 0, "", nil, unsupported(mediaType)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, f.cfg.MaxBytes+1))
	if err != nil {
		return 0, "", nil, f.requestError(ctx, err)
	}
	if int64(len(body)) > f.cfg.MaxBytes {
		return 0, "", nil, fmt.Errorf("%w (over %s)", ErrTooLarge, size(f.cfg.MaxBytes))
	}
	f.in.bytes.Record(ctx, int64(len(body)))
	if header == "" {
		mediaType = parseMediaType(http.DetectContentType(body))
	}
	k := kinds[mediaType]
	if k == 0 && strings.HasSuffix(mediaType, "+json") {
		k = kindText
	}
	if k == 0 {
		return 0, "", nil, unsupported(mediaType)
	}
	return k, mediaType, body, nil
}

// page is the fetched text: HTML through the converter, the rest as it is,
// cut at MaxChars.
func (f *Fetcher) page(ctx context.Context, final string, k kind, mediaType, body string) (Page, error) {
	p := Page{URL: final, ContentType: mediaType, Text: body}
	if k == kindHTML {
		// The bound is on the decoded text html2md gets: a windows-1251
		// page doubles in UTF-8.
		if int64(len(body)) > f.cfg.MaxHTMLBytes {
			return Page{}, fmt.Errorf("%w (an HTML page over %s)", ErrTooLarge, size(f.cfg.MaxHTMLBytes))
		}
		title, md, err := f.conv.Convert(ctx, final, body)
		if err != nil {
			return Page{}, ErrConvert
		}
		p.Title, p.Text = title, md
	}
	if cut := text.Truncate(p.Text, f.cfg.MaxChars); cut != p.Text {
		p.Text, p.Truncated = cut, true
	}
	return p, nil
}

// kind is what a fetch does with a media type.
type kind int

const (
	kindHTML kind = iota + 1
	kindText
)

// kinds are the media types a fetch reads; any "+json" type is text too.
var kinds = map[string]kind{
	"text/html":             kindHTML,
	"application/xhtml+xml": kindHTML,
	"application/json":      kindText,
	"text/plain":            kindText,
}

func parseMediaType(header string) string {
	mt, _, err := mime.ParseMediaType(header)
	if err != nil {
		return ""
	}
	return mt
}

// unsupported names the type, cut short: a type is a token without
// spaces, but a server chooses it.
func unsupported(mediaType string) error {
	if mediaType == "" {
		mediaType = "unknown"
	}
	return fmt.Errorf("%w %q", ErrContentType, text.Truncate(mediaType, 64))
}

// size writes a byte count in KiB or MiB.
func size(n int64) string {
	if n >= 1<<20 && n%(1<<20) == 0 {
		return fmt.Sprintf("%d MiB", n>>20)
	}
	if n >= 1<<10 && n%(1<<10) == 0 {
		return fmt.Sprintf("%d KiB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}
