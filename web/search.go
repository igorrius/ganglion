package web

import (
	"context"
	"errors"
	"html"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Errors of a search; their texts are fixed, like a fetch's.
var (
	// ErrSearch is a search the backend did not answer.
	ErrSearch = errors.New("web.search: the search failed")
	// ErrBlocked is a key-free backend that answered with a bot check
	// instead of results.
	ErrBlocked = errors.New("web.search: the search engine asked for a bot check; try later, or set BRAVE_API_KEY")
)

// Hit is one search result.
type Hit struct {
	Title   string
	URL     string
	Snippet string
}

// Searcher is a search backend (spec R-WEB-3, ADR-0035).
type Searcher interface {
	Search(ctx context.Context, q string, n int) ([]Hit, error)
}

// SearchOption changes where a backend sends its requests (tests).
type SearchOption func(*searchOptions)

type searchOptions struct {
	base   string
	client *http.Client
}

// WithSearchBase sends requests to base instead of the backend's own
// address.
func WithSearchBase(base string) SearchOption {
	return func(o *searchOptions) { o.base = strings.TrimSuffix(base, "/") }
}

// WithSearchClient makes requests with c.
func WithSearchClient(c *http.Client) SearchOption { return func(o *searchOptions) { o.client = c } }

func newSearchOptions(base string, opts []SearchOption) searchOptions {
	o := searchOptions{base: base, client: &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{Proxy: nil}}}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// maxSearchBody bounds a backend's answer.
const maxSearchBody = 1 << 20

var tags = regexp.MustCompile(`<[^>]*>`)

// plain is a fragment of HTML as text: tags dropped, entities decoded,
// whitespace collapsed.
func plain(s string) string {
	return strings.Join(strings.Fields(html.UnescapeString(tags.ReplaceAllString(s, ""))), " ")
}
