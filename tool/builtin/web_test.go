package builtin_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/internal/urlnorm"
	"github.com/igorrius/ganglion/policy"
	"github.com/igorrius/ganglion/router"
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

func TestWebSearchNamesNoOtherAddress(t *testing.T) {
	s := &searcher{hits: []web.Hit{
		{Title: "a", URL: "https://ok.example/", Snippet: "xhttps://evil.example/a and _http://evil.example/b"},
		{Title: "b", URL: "https://a.example/?q=a https://evil.example/x"},
		{Title: "c", URL: `https://b.example/?q=">https://evil.example/y`},
	}}
	res, err := builtin.NewWebSearch(s).Run(context.Background(), call("web.search", `{"query":"q"}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := urlnorm.Find(res.Text); len(got) != 1 || got[0] != "https://ok.example/" {
		t.Fatalf("addresses in the result: %q\n%s", got, res.Text)
	}
}

// A redirect outside a task's grant is refused by the fetcher after the
// policy let the call through; it is still an out-of-grant call, with the
// same notice to the admins as a policy denial (M3b final review).
func TestWebFetchRedirectOutsideTheGrantAlerts(t *testing.T) {
	f := &fetcher{err: web.ErrOutsideGrant}
	reg, err := tool.NewRegistry(router.Local{}, builtin.NewWebFetch(f))
	if err != nil {
		t.Fatal(err)
	}
	admins := alert.Admins{List: func(context.Context) ([]string, error) { return []string{"telegram:user:42"}, nil }, Private: private}
	pol := policy.New(policy.DefaultConfig(), reg, &asker{}, policy.WithAlerts(admins),
		policy.WithClock(func() time.Time { return time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC) }, time.UTC))
	c := call("web.fetch", `{"url":"https://bank.gov.ua/x"}`)
	c.Conv = tool.TaskConvPrefix + "j1"
	c.Grant = &tool.Grant{Tools: []string{"web.fetch"}, Hosts: []string{"bank.gov.ua"}, ReplyTo: "telegram:42"}
	res, err := tool.Chain(reg.Run, pol.Middleware())(context.Background(), c)
	if !errors.Is(err, tool.ErrOutOfGrant) || !errors.Is(err, web.ErrOutsideGrant) {
		t.Fatalf("err %v", err)
	}
	if len(res.Out) != 1 || !strings.HasPrefix(res.Out[0].IdemKey, "alert:grant:j1:2026-11-03:") || res.Out[0].Conv != "telegram:42" {
		t.Fatalf("out %+v, want the grant alert", res.Out)
	}
}

// web.fetch's question shows the normalised address it will fetch, in a
// code span so Markdown cannot change it; an address the question cannot
// show exactly is refused without a question (rulings R34, R39).
func TestWebFetchQuestionShowsTheAddress(t *testing.T) {
	ask, isQ := tool.Handler(builtin.NewWebFetch(&fetcher{})).(tool.Questioner)
	if !isQ {
		t.Fatal("web.fetch words no question")
	}
	got, err := ask.Question(context.Background(), call("web.fetch", `{"url":"HTTPS://Bank.Example:443/rates?valcode=EUR&json#top"}`))
	want := "Open this page?\n`https://bank.example/rates?valcode=EUR&json`"
	if err != nil || got != want {
		t.Fatalf("Question = %q, %v; want %q", got, err, want)
	}
	long := "https://a.example/" + strings.Repeat("a", tool.MaxQuestionRunes)
	for name, args := range map[string]string{
		"too long":       `{"url":"` + long + `"}`,
		"not an address": `{"url":"ftp://a.example/"}`,
		"no address":     `{}`,
		"a backtick":     "{\"url\":\"https://a.example/?q=a`b\"}",
		"a secret in it": `{"url":"https://a.example/?token=sk-ant-api03-abcdefghijklmnopqrstuvwxyz0123456789"}`,
	} {
		if q, err := ask.Question(context.Background(), call("web.fetch", args)); err == nil {
			t.Errorf("%s: asked %q", name, q)
		}
	}
}
