package builtin_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/igorrius/ganglion/web"
)

// fetcher records the address and scope of each fetch.
type fetcher struct {
	page   web.Page
	err    error
	urls   []string
	scopes []web.Scope
}

func (f *fetcher) Fetch(_ context.Context, rawURL string, scope web.Scope) (web.Page, error) {
	f.urls = append(f.urls, rawURL)
	f.scopes = append(f.scopes, scope)
	return f.page, f.err
}

type searcher struct {
	hits []web.Hit
	err  error
	q    string
	n    int
}

func (s *searcher) Search(_ context.Context, q string, n int) ([]web.Hit, error) {
	s.q, s.n = q, n
	return s.hits, s.err
}

func TestWebToolSpecs(t *testing.T) {
	for _, s := range []tool.Spec{builtin.NewWebFetch(&fetcher{}).Spec(), builtin.NewWebSearch(&searcher{}).Spec()} {
		if s.Class != tool.ClassExternal || s.Policy != tool.PolicyAuto || !s.AdminOnly || len(s.InputSchema) == 0 {
			t.Errorf("%s: spec = %+v", s.Name, s)
		}
	}
}

func TestWebFetchResultIsExternal(t *testing.T) {
	f := &fetcher{page: web.Page{URL: "https://news.example/a", Title: "News", Text: "# Head\n\nbody", ContentType: "text/html", Truncated: true}}
	res, err := builtin.NewWebFetch(f).Run(context.Background(), call("web.fetch", `{"url":"https://news.example/a#x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.External || res.Pending != "" || len(res.Out) != 0 {
		t.Fatalf("result = %+v", res)
	}
	for _, want := range []string{"URL: https://news.example/a", "Title: News", "# Head\n\nbody", "truncated"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("text lacks %q:\n%s", want, res.Text)
		}
	}
	if f.urls[0] != "https://news.example/a#x" || !f.scopes[0]("any.example") {
		t.Errorf("fetched %v; outside a grant every host is in scope", f.urls)
	}
}

func TestWebFetchInAGrantKeepsToItsHosts(t *testing.T) {
	f := &fetcher{page: web.Page{URL: "https://bank.gov.ua/x", Text: "[]", ContentType: "application/json"}}
	c := call("web.fetch", `{"url":"https://bank.gov.ua/x"}`)
	c.Grant = &tool.Grant{Tools: []string{"web.fetch"}, Hosts: []string{"bank.gov.ua"}}
	if _, err := builtin.NewWebFetch(f).Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if !f.scopes[0]("bank.gov.ua") || f.scopes[0]("other.example") {
		t.Error("the fetch's scope is not the grant's hosts")
	}
}

func TestWebFetchErrors(t *testing.T) {
	f := &fetcher{err: web.ErrPrivateAddress}
	if _, err := builtin.NewWebFetch(f).Run(context.Background(), call("web.fetch", `{"url":"http://10.0.0.1/"}`)); !errors.Is(err, web.ErrPrivateAddress) {
		t.Errorf("err = %v", err)
	}
	for _, args := range []string{`{}`, `{"url":""}`, `not json`, `{"url":5}`} {
		if _, err := builtin.NewWebFetch(&fetcher{}).Run(context.Background(), call("web.fetch", args)); err == nil {
			t.Errorf("args %s: no error", args)
		}
	}
}

func TestWebSearchListsOneAddressPerHit(t *testing.T) {
	s := &searcher{hits: []web.Hit{
		{Title: "NBU https://title.example/t", URL: "https://bank.gov.ua/en/markets/exchangerates", Snippet: "rates, see https://evil.example/x?d=1 and HTTP://Evil2.example"},
		{Title: "Bad", URL: "javascript:alert(1)", Snippet: "dropped"},
		{Title: "Other", URL: "https://other.example/y", Snippet: ""},
	}}
	res, err := builtin.NewWebSearch(s).Run(context.Background(), call("web.search", `{"query":"nbu rates"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.External || s.q != "nbu rates" || s.n != 5 {
		t.Fatalf("result = %+v, query %q %d", res, s.q, s.n)
	}
	want := "1. NBU title.example/t\nURL: https://bank.gov.ua/en/markets/exchangerates\nrates, see evil.example/x?d=1 and Evil2.example\n\n" +
		"2. Other\nURL: https://other.example/y"
	if res.Text != want {
		t.Fatalf("text =\n%s\nwant\n%s", res.Text, want)
	}
}

func TestWebSearchNothingFoundAndErrors(t *testing.T) {
	res, err := builtin.NewWebSearch(&searcher{}).Run(context.Background(), call("web.search", `{"query":"x","count":3}`))
	if err != nil || res.Text != "No results." {
		t.Fatalf("empty search = %+v, %v", res, err)
	}
	if _, err := builtin.NewWebSearch(&searcher{err: web.ErrBlocked}).Run(context.Background(), call("web.search", `{"query":"x"}`)); !errors.Is(err, web.ErrBlocked) {
		t.Errorf("err = %v", err)
	}
	for _, args := range []string{`{}`, `{"query":"  "}`, `{"query":"x","count":0}`, `{"query":"x","count":11}`} {
		if _, err := builtin.NewWebSearch(&searcher{}).Run(context.Background(), call("web.search", args)); err == nil {
			t.Errorf("args %s: no error", args)
		}
	}
}
