package intake_test

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/intake"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
)

// published reads intake/published.sha256: version → hex SHA-256.
func published(t *testing.T) map[string]string {
	t.Helper()
	f, err := os.Open("published.sha256")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	out := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		sum, ver, ok := strings.Cut(line, "  ")
		if !ok || len(sum) != 64 {
			t.Fatalf("published.sha256: bad line %q", line)
		}
		out[ver] = sum
	}
	return out
}

func sumOf(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// The release embeds the published v1, and the site's copy is the same file.
func TestThePublishedPagesAreTheEmbeddedAndTheSiteCopies(t *testing.T) {
	pub := published(t)
	if pub["v1"] != sumOf(intake.Page()) {
		t.Fatalf("the embedded page is not the published v1")
	}
	for ver, sum := range pub {
		for _, path := range []string{
			filepath.Join("page", ver, "index.html"),
			filepath.Join("..", "docs", "guide", "intake", ver, "index.html"),
		} {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if sumOf(b) != sum {
				t.Errorf("%s differs from its published hash", path)
			}
		}
	}
}

// The page's Content-Security-Policy allows exactly its own inline script
// and style, and no connection.
func TestThePagePolicyPinsItsScriptAndStyle(t *testing.T) {
	page := string(intake.Page())
	csp := regexp.MustCompile(`http-equiv="Content-Security-Policy" content="([^"]*)"`).FindStringSubmatch(page)
	if csp == nil {
		t.Fatal("no Content-Security-Policy")
	}
	for _, tc := range []struct{ tag, directive string }{{"script", "script-src"}, {"style", "style-src"}} {
		body := regexp.MustCompile(`(?s)<` + tc.tag + `>(.*?)</` + tc.tag + `>`).FindStringSubmatch(page)
		if body == nil {
			t.Fatalf("no <%s>", tc.tag)
		}
		sum := sha256.Sum256([]byte(body[1]))
		want := tc.directive + " 'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "';"
		if !strings.Contains(csp[1], want) {
			t.Errorf("the policy %q lacks %q", csp[1], want)
		}
	}
	for _, d := range []string{"default-src 'none'", "connect-src 'none'", "form-action 'none'", "base-uri 'none'"} {
		if !strings.Contains(csp[1], d) {
			t.Errorf("the policy lacks %q", d)
		}
	}
	if strings.Contains(csp[1], "unsafe") {
		t.Errorf("the policy allows unsafe sources: %q", csp[1])
	}
}

// host serves the page check's answers over TLS.
func host(t *testing.T, status int, body []byte) (*intake.PageChecker, *httptest.Server) {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	cfg := intake.DefaultPageCheckConfig()
	cfg.URL = srv.URL + "/intake/v1/"
	return intake.NewPageChecker(srv.Client(), cfg), srv
}

func TestThePageCheck(t *testing.T) {
	rec := telemetrytest.Install(t)
	changed := append(intake.Page(), '\n')
	big := make([]byte, 65<<10)
	for _, tc := range []struct {
		name   string
		status int
		body   []byte
		want   error
	}{
		{"the same page", http.StatusOK, intake.Page(), nil},
		{"a changed page", http.StatusOK, changed, intake.ErrPageChanged},
		{"an empty page", http.StatusOK, nil, intake.ErrPageChanged},
		{"not found", http.StatusNotFound, intake.Page(), intake.ErrPageUnchecked},
		{"too big", http.StatusOK, big, intake.ErrPageUnchecked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := host(t, tc.status, tc.body)
			if err := c.Check(context.Background()); !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
				t.Fatalf("Check = %v, want %v", err, tc.want)
			}
		})
	}
	for o, n := range map[string]float64{"same": 1, "changed": 2, "unchecked": 2} {
		if got := rec.Value(t, "ganglion.intake.page_checks", map[string]string{"outcome": o}); got != n {
			t.Errorf("page_checks{%s} = %v, want %v", o, got, n)
		}
	}
}

func TestThePageCheckRefusesPlainHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(intake.Page()) }))
	defer srv.Close()
	cfg := intake.DefaultPageCheckConfig()
	cfg.URL = srv.URL
	if err := intake.NewPageChecker(srv.Client(), cfg).Check(context.Background()); !errors.Is(err, intake.ErrPageUnchecked) {
		t.Fatalf("Check = %v", err)
	}
}

// A changed hosted page stops secret requests; a sign-in goes on with a
// warning (R-INT-3).
func TestAChangedHostedPageStopsSecretRequests(t *testing.T) {
	rec := telemetrytest.Install(t)
	c, _ := host(t, http.StatusOK, append(intake.Page(), ' '))
	rg := newRig(t)
	rg.page = c.Check(context.Background())
	var v valueSink
	if _, err := rg.reg.OpenSecret(context.Background(), "k", v.fn); !errors.Is(err, intake.ErrPageChanged) {
		t.Fatalf("OpenSecret = %v, want ErrPageChanged", err)
	}
	if n := rec.Value(t, "ganglion.intake.open", nil); n != 0 {
		t.Fatalf("%v requests open after a refusal", n)
	}
	req, err := rg.reg.OpenOAuth(context.Background(), func(context.Context, intake.OAuthAnswer) error { return nil })
	if err != nil || !errors.Is(req.PageErr, intake.ErrPageChanged) || req.ID == "" {
		t.Fatalf("OpenOAuth = %+v, %v", req, err)
	}
	if !strings.Contains(rg.logs.String(), "intake page failed its check") {
		t.Errorf("no warning in the log: %s", rg.logs.String())
	}
}

// The link's fragment carries what the page reads, and nothing goes in the
// query, which a browser would send to the host.
func TestSecretLink(t *testing.T) {
	rg := newRig(t)
	req, err := rg.reg.OpenSecret(context.Background(), "shop key/ü", (&valueSink{}).fn)
	if err != nil {
		t.Fatal(err)
	}
	link := intake.SecretLink(intake.DefaultPage, req, "api.example.com")
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	if u.RawQuery != "" || u.Scheme+"://"+u.Host+u.Path != intake.DefaultPage {
		t.Fatalf("link %q", link)
	}
	f, err := url.ParseQuery(u.Fragment)
	if err != nil {
		t.Fatal(err)
	}
	pk, err := base64.RawURLEncoding.DecodeString(f.Get("pk"))
	if err != nil || string(pk) != string(req.PublicKey) {
		t.Fatalf("pk %q", f.Get("pk"))
	}
	if f.Get("v") != "1" || f.Get("mode") != "secret" || f.Get("id") != req.ID || f.Get("name") != "shop key/ü" || f.Get("host") != "api.example.com" {
		t.Fatalf("fragment %v", f)
	}
	if strings.Contains(intake.SecretLink(intake.DefaultPage, req, ""), "host=") {
		t.Error("an empty host is in the link")
	}
}

type oauthFixture struct {
	State     string `json:"state"`
	Code      string `json:"code"`
	Iss       string `json:"iss"`
	Line      string `json:"line"`
	LineNoIss string `json:"line_no_iss"`
}

// A gauth1 line made by the page's own script parses to the redirect's
// code and issuer, and hands them to the request's opener.
func TestTheOAuthLineFromThePageParses(t *testing.T) {
	var fx oauthFixture
	readFixture(t, "oauth-v1.json", &fx)
	for _, tc := range []struct{ line, iss string }{{fx.Line, fx.Iss}, {fx.LineNoIss, ""}} {
		l, err := intake.Parse(tc.line)
		if err != nil || l.Kind != intake.KindOAuth || l.ID != fx.State || l.Code != fx.Code || l.Issuer != tc.iss {
			t.Fatalf("Parse(%q) = %+v, %v", tc.line, l, err)
		}
	}
	rg := newRig(t)
	var got intake.OAuthAnswer
	req, err := rg.reg.OpenOAuth(context.Background(), func(_ context.Context, a intake.OAuthAnswer) error { got = a; return nil })
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Replace(fx.Line, fx.State, req.ID, 1)
	if _, o := rg.take(line); o != intake.LineTaken || got.Code != fx.Code || got.Issuer != fx.Iss || got.ID != req.ID {
		t.Fatalf("outcome %s answer %+v", o, got)
	}
}
