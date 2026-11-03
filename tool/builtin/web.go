package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/igorrius/ganglion/internal/urlnorm"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/web"
)

// Fetcher reads a page within a scope (web.Fetcher).
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string, scope web.Scope) (web.Page, error)
}

// WebFetch is the web.fetch tool (R-WEB-1, R-WEB-2): a page as text,
// returned as outside content, which taints the session.
type WebFetch struct {
	fetcher Fetcher
}

// NewWebFetch returns web.fetch over f.
func NewWebFetch(f Fetcher) *WebFetch { return &WebFetch{fetcher: f} }

// Spec describes web.fetch. It runs without a press only on an address
// from the owner's messages or a search (the policy's URL rule), and in a
// scheduled task only on the grant's hosts.
func (*WebFetch) Spec() tool.Spec {
	return tool.Spec{
		Name: "web.fetch",
		Description: "Read a web page or a JSON API by its http(s) address. HTML comes back as Markdown of the main content, " +
			"JSON and plain text as they are, cut at 20,000 characters. An address that is not in the owner's messages " +
			"or in search results needs the owner's confirmation.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"url":{"type":"string","description":"the http or https address"}},` +
			`"required":["url"],"additionalProperties":false}`),
		Class:     tool.ClassExternal,
		Policy:    tool.PolicyAuto,
		AdminOnly: true,
	}
}

// Run fetches the address; inside a scheduled task's grant every
// redirect must stay on the granted hosts too.
func (w *WebFetch) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	var a struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil || strings.TrimSpace(a.URL) == "" {
		return tool.Result{}, errors.New("web.fetch: arguments: want {\"url\": \"https://…\"}")
	}
	scope := web.AnyHost
	if c.Grant != nil {
		scope = web.OnlyHosts(c.Grant.Hosts)
	}
	page, err := w.fetcher.Fetch(ctx, a.URL, scope)
	if errors.Is(err, web.ErrOutsideGrant) {
		// A redirect off the granted hosts: the policy sends the same
		// notice as for a call outside the grant.
		return tool.Result{}, fmt.Errorf("%w: %w", err, tool.ErrOutOfGrant)
	}
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: pageText(page), External: true}, nil
}

// questionFetch starts web.fetch's question; the address follows in a
// code span, where Markdown changes nothing.
const questionFetch = "Open this page?\n"

// errUserinfo refuses an address with a user name or password before
// the host, which the fetcher refuses too: asking would be pointless.
var errUserinfo = errors.New("web.fetch: an address with a user name or password is not fetched")

// errBacktick refuses an address a code span cannot hold.
var errBacktick = errors.New("web.fetch: the address holds a backtick, which the question cannot show; write it as %60")

// Question implements tool.Questioner: the owner sees the normalised
// address, the one the fetcher reads, whole (rulings R34, R39), with an
// internationalised host in its ASCII form (urlnorm), so a look-alike
// host shows as what it is. An address that is not http(s), has a user
// name or password, holds a backtick, is longer than a
// question shows or holds something the redaction would hide is refused
// without a question.
func (*WebFetch) Question(_ context.Context, c tool.Call) (string, error) {
	var a struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return "", errors.New("web.fetch: arguments: want {\"url\": \"https://…\"}")
	}
	norm, ok := urlnorm.Normalize(a.URL)
	if !ok {
		return "", errors.New("web.fetch: want an http or https address")
	}
	if u, err := url.Parse(norm); err != nil || u.User != nil {
		return "", errUserinfo
	}
	if strings.Contains(norm, "`") {
		return "", errBacktick
	}
	return whole("web.fetch: the address", questionFetch+"`"+norm+"`")
}

// pageText is the page with its address, title and type on top.
func pageText(p web.Page) string {
	var b strings.Builder
	b.WriteString("URL: " + p.URL + "\n")
	if p.Title != "" {
		b.WriteString("Title: " + p.Title + "\n")
	}
	b.WriteString("Content-Type: " + p.ContentType + "\n\n" + p.Text)
	if p.Truncated {
		b.WriteString("\n\n[the page was truncated]")
	}
	return b.String()
}

// WebSearch is the web.search tool (R-WEB-3): results as outside content.
// The address of each hit counts as known for a later web.fetch; the
// agent reads it from the "URL:" line, so addresses in titles and
// snippets lose their scheme and do not count.
type WebSearch struct {
	searcher web.Searcher
}

// NewWebSearch returns web.search over s.
func NewWebSearch(s web.Searcher) *WebSearch { return &WebSearch{searcher: s} }

// defaultHits and maxHits bound the results of one search.
const (
	defaultHits = 5
	maxHits     = 10
)

// Spec describes web.search.
func (*WebSearch) Spec() tool.Spec {
	return tool.Spec{
		Name:        "web.search",
		Description: "Search the web. Returns titles, addresses and snippets; the addresses can then be read with web.fetch.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"query":{"type":"string","description":"what to search for"},` +
			`"count":{"type":"integer","minimum":1,"maximum":` + strconv.Itoa(maxHits) + `,"description":"how many results, 5 if not given"}},` +
			`"required":["query"],"additionalProperties":false}`),
		Class:     tool.ClassExternal,
		Policy:    tool.PolicyAuto,
		AdminOnly: true,
	}
}

// Run searches and lists the hits.
func (w *WebSearch) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	var a struct {
		Query string `json:"query"`
		Count *int   `json:"count"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil || strings.TrimSpace(a.Query) == "" {
		return tool.Result{}, errors.New("web.search: arguments: want {\"query\": \"…\"}")
	}
	n := defaultHits
	if a.Count != nil {
		if *a.Count < 1 || *a.Count > maxHits {
			return tool.Result{}, fmt.Errorf("web.search: count must be 1 to %d", maxHits)
		}
		n = *a.Count
	}
	hits, err := w.searcher.Search(ctx, a.Query, n)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: hitsText(hits), External: true}, nil
}

// hitsText numbers the hits with one "URL:" line each. Titles and
// snippets are defanged, and a hit whose address the agent would not
// read back as exactly itself is left out, so a result makes no other
// address known (ruling R19).
func hitsText(hits []web.Hit) string {
	var parts []string
	for _, h := range hits {
		u, ok := urlnorm.Normalize(h.URL)
		if !ok || !urlnorm.Exact(u) {
			continue
		}
		part := strconv.Itoa(len(parts)+1) + ". " + urlnorm.Defang(h.Title) + "\nURL: " + u
		if s := urlnorm.Defang(h.Snippet); s != "" {
			part += "\n" + s
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "No results."
	}
	return strings.Join(parts, "\n\n")
}
