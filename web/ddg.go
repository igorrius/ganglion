package web

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// DuckDuckGo is the key-free backend: DuckDuckGo's HTML results page, an
// unofficial interface that may change or answer with a bot check
// (ErrBlocked) (ADR-0035). The page is read with linear regular
// expressions over at most 1 MiB, not with an HTML parser.
type DuckDuckGo struct {
	o searchOptions
}

// NewDuckDuckGo returns the DuckDuckGo backend.
func NewDuckDuckGo(opts ...SearchOption) *DuckDuckGo {
	return &DuckDuckGo{o: newSearchOptions("https://html.duckduckgo.com", opts)}
}

var (
	ddgResult  = regexp.MustCompile(`(?s)<a\b[^>]*\bclass="result__a"[^>]*>(.*?)</a>`)
	ddgHref    = regexp.MustCompile(`\bhref="([^"]*)"`)
	ddgSnippet = regexp.MustCompile(`(?s)<a\b[^>]*\bclass="result__snippet"[^>]*>(.*?)</a>`)
)

// Search implements Searcher.
func (d *DuckDuckGo) Search(ctx context.Context, q string, n int) ([]Hit, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.o.base+"/html/", strings.NewReader(url.Values{"q": {q}}.Encode()))
	if err != nil {
		return nil, ErrSearch
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "ganglion (+https://github.com/igorrius/ganglion)")
	resp, err := d.o.client.Do(req)
	if err != nil {
		return nil, ErrSearch
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSearchBody))
	if err != nil {
		return nil, ErrSearch
	}
	page := string(body)
	if resp.StatusCode == http.StatusAccepted || strings.Contains(page, "anomaly-modal") {
		return nil, ErrBlocked
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: DuckDuckGo answered %d", ErrSearch, resp.StatusCode)
	}
	return ddgHits(page, n), nil
}

// ddgHits reads up to n results; ads and links that do not lead out of
// DuckDuckGo are skipped.
func ddgHits(page string, n int) []Hit {
	var hits []Hit
	links := ddgResult.FindAllStringSubmatchIndex(page, -1)
	for i, m := range links {
		end := len(page)
		if i+1 < len(links) {
			end = links[i+1][0]
		}
		href := ddgHref.FindStringSubmatch(page[m[0]:m[3]])
		if href == nil {
			continue
		}
		target, ok := ddgTarget(html.UnescapeString(href[1]))
		if !ok {
			continue
		}
		hit := Hit{Title: plain(page[m[2]:m[3]]), URL: target}
		if s := ddgSnippet.FindStringSubmatch(page[m[1]:end]); s != nil {
			hit.Snippet = plain(s[1])
		}
		if hits = append(hits, hit); len(hits) == n {
			break
		}
	}
	return hits
}

// ddgTarget is where a result link leads: a direct http(s) address, or
// the uddg parameter of DuckDuckGo's redirect link. Ads (y.js) and other
// DuckDuckGo links are not results.
func ddgTarget(href string) (string, bool) {
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	if host != "duckduckgo.com" && !strings.HasSuffix(host, ".duckduckgo.com") {
		return href, true
	}
	if u.Path != "/l/" {
		return "", false
	}
	return ddgTarget(u.Query().Get("uddg"))
}
