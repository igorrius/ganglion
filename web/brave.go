package web

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// Brave is the Brave Search API, enabled by BRAVE_API_KEY. The key
// travels in a header and never appears in an error.
type Brave struct {
	key string
	o   searchOptions
}

// NewBrave returns the Brave backend with key.
func NewBrave(key string, opts ...SearchOption) *Brave {
	return &Brave{key: key, o: newSearchOptions("https://api.search.brave.com", opts)}
}

// Search implements Searcher.
func (b *Brave) Search(ctx context.Context, q string, n int) ([]Hit, error) {
	u := b.o.base + "/res/v1/web/search?" + url.Values{"q": {q}, "count": {strconv.Itoa(n)}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, ErrSearch
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", b.key)
	resp, err := b.o.client.Do(req)
	if err != nil {
		return nil, ErrSearch
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: Brave answered %d", ErrSearch, resp.StatusCode)
	}
	var body struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxSearchBody)).Decode(&body); err != nil {
		return nil, fmt.Errorf("%w: Brave's answer did not parse", ErrSearch)
	}
	hits := make([]Hit, 0, len(body.Web.Results))
	for _, r := range body.Web.Results {
		hits = append(hits, Hit{Title: plain(r.Title), URL: r.URL, Snippet: plain(r.Description)})
	}
	return hits[:min(len(hits), n)], nil
}
