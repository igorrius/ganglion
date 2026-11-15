package release

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/modstore"
)

func hexsum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// fakeGitHub serves a releases list and their assets; files maps an asset
// path to its content.
type fakeGitHub struct {
	srv      *httptest.Server
	releases []map[string]any
	files    map[string][]byte
}

func newFake(t *testing.T) *fakeGitHub {
	f := &fakeGitHub{files: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/repos/igorrius/ganglion/releases" {
			_ = json.NewEncoder(w).Encode(f.releases)
			return
		}
		if b, ok := f.files[r.URL.Path]; ok {
			_, _ = w.Write(b)
			return
		}
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "http://evil.example/html2md.wasm", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

// add publishes a release of html2md with a manifest of the given ABI.
func (f *fakeGitHub) add(tag string, abi int, draft, pre bool) {
	wasm := []byte("wasm of " + tag)
	manifest := []byte(fmt.Sprintf("name: html2md\nabi: %d\n", abi))
	base := "/dl/" + tag + "/"
	f.files[base+"html2md.wasm"], f.files[base+"html2md.yaml"] = wasm, manifest
	f.files[base+"SHA256SUMS"] = []byte(hexsum(wasm) + "  html2md.wasm\n" + hexsum(manifest) + "  html2md.yaml\n")
	var assets []map[string]string
	for _, n := range []string{"html2md.wasm", "html2md.yaml", "SHA256SUMS"} {
		assets = append(assets, map[string]string{"name": n, "browser_download_url": f.srv.URL + base + n})
	}
	f.releases = append(f.releases, map[string]any{"tag_name": tag, "draft": draft, "prerelease": pre, "assets": assets})
}

func (f *fakeGitHub) client() *GitHub {
	u, _ := url.Parse(f.srv.URL)
	return NewGitHub("igorrius/ganglion", WithAPI(f.srv.URL), WithHosts(u.Hostname()), WithTransport(f.srv.Client().Transport))
}

func TestLatestPicksTheNewestStableRelease(t *testing.T) {
	f := newFake(t)
	for _, tag := range []string{"html2md/v0.1.0", "html2md/v0.10.0", "html2md/v0.3.0", "demo/v9.0.0", "v1.0.0", "html2md/not-semver"} {
		f.add(tag, 1, false, false)
	}
	f.add("html2md/v0.11.0", 1, true, false) // a draft
	f.add("html2md/v0.12.0", 1, false, true) // a pre-release
	r, err := f.client().Latest(context.Background(), "html2md", 1)
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "0.10.0" || r.Tag != "html2md/v0.10.0" || r.SHA256 != hexsum([]byte("wasm of html2md/v0.10.0")) || r.ManifestSHA256 == "" {
		t.Fatalf("release %+v", r)
	}
	wasm, manifest, err := f.client().Fetch(context.Background(), r)
	if err != nil || string(wasm) != "wasm of html2md/v0.10.0" || !strings.Contains(string(manifest), "abi: 1") {
		t.Fatalf("fetch %q %q %v", wasm, manifest, err)
	}
}

// The newest release whose ABI this core speaks: a newer one for a newer
// core is passed over.
func TestLatestSkipsAnABIThisCoreDoesNotSpeak(t *testing.T) {
	f := newFake(t)
	f.add("html2md/v0.2.0", 1, false, false)
	f.add("html2md/v0.3.0", 2, false, false)
	r, err := f.client().Latest(context.Background(), "html2md", 1)
	if err != nil || r.Version != "0.2.0" {
		t.Fatalf("release %+v, %v", r, err)
	}
	if _, err := f.client().Latest(context.Background(), "demo", 1); err == nil {
		t.Fatal("a module with no release was found")
	}
}

// Review focus 2: an asset on another host, or a redirect to one, is
// refused.
func TestRedirectOffGitHubRefused(t *testing.T) {
	f := newFake(t)
	f.add("html2md/v0.2.0", 1, false, false)
	g := f.client()
	r, err := g.Latest(context.Background(), "html2md", 1)
	if err != nil {
		t.Fatal(err)
	}
	r.WasmURL = f.srv.URL + "/redirect"
	if _, _, err := g.Fetch(context.Background(), r); err == nil || !strings.Contains(err.Error(), "evil.example") {
		t.Fatalf("a redirect off the hosts: %v", err)
	}
	r.WasmURL = "https://evil.example/html2md.wasm"
	if _, _, err := g.Fetch(context.Background(), r); err == nil || !strings.Contains(err.Error(), "evil.example") {
		t.Fatalf("an asset on another host: %v", err)
	}
}

func TestFetchRefusesAnOversizedFile(t *testing.T) {
	f := newFake(t)
	f.add("html2md/v0.2.0", 1, false, false)
	f.files["/dl/html2md/v0.2.0/html2md.wasm"] = make([]byte, MaxFileBytes+1)
	g := f.client()
	r, err := g.Latest(context.Background(), "html2md", 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := g.Fetch(context.Background(), r); err == nil || !strings.Contains(err.Error(), "MiB") {
		t.Fatalf("an oversized file: %v", err)
	}
}

func TestDefaultHostsAreGitHubs(t *testing.T) {
	g := NewGitHub("igorrius/ganglion")
	for _, h := range []string{"api.github.com", "github.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com"} {
		if !g.c.Allowed(h) {
			t.Errorf("%s not allowed", h)
		}
	}
	if g.c.Allowed("evil.example") || g.c.Allowed("github.com.evil.example") {
		t.Error("another host allowed")
	}
}

// Only https on the default port is fetched.
func TestPlainHTTPRefused(t *testing.T) {
	g := NewGitHub("igorrius/ganglion")
	for _, u := range []string{"http://github.com/x", "https://github.com:8443/x"} {
		if _, _, err := g.Fetch(context.Background(), modstore.Release{WasmURL: u, ManifestURL: u}); err == nil || !strings.Contains(err.Error(), "https") {
			t.Errorf("%s: %v, want a refusal of anything but https on 443", u, err)
		}
	}
}

// A newer release that lacks an asset is passed over, not the end of the
// search.
// Final review: a read that fails is no incomplete release. It must not
// select an older one, which would then install as an update.
func TestFailedReadOfTheNewestSumsEndsTheSearch(t *testing.T) {
	for name, path := range map[string]func(f *fakeGitHub){
		"not found": func(f *fakeGitHub) { delete(f.files, "/dl/html2md/v0.4.0/SHA256SUMS") },
		"redirect off GitHub": func(f *fakeGitHub) {
			assets := f.releases[1]["assets"].([]map[string]string)
			assets[2]["browser_download_url"] = f.srv.URL + "/redirect"
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.add("html2md/v0.3.0", 1, false, false)
			f.add("html2md/v0.4.0", 1, false, false)
			path(f)
			if r, err := f.client().Latest(context.Background(), "html2md", 1); err == nil {
				t.Fatalf("selected %s although the newest release could not be read", r.Version)
			}
		})
	}
}

// A sums file that does not name both files is an incomplete release.
func TestReleaseWithIncompleteSumsSkipped(t *testing.T) {
	f := newFake(t)
	f.add("html2md/v0.3.0", 1, false, false)
	f.add("html2md/v0.4.0", 1, false, false)
	f.files["/dl/html2md/v0.4.0/SHA256SUMS"] = []byte("abc  html2md.wasm\n")
	if r, err := f.client().Latest(context.Background(), "html2md", 1); err != nil || r.Version != "0.3.0" {
		t.Fatalf("release %+v, %v", r, err)
	}
}

func TestReleaseWithoutAssetsSkipped(t *testing.T) {
	f := newFake(t)
	f.add("html2md/v0.2.0", 1, false, false)
	f.releases = append(f.releases, map[string]any{"tag_name": "html2md/v0.3.0", "assets": []map[string]string{}})
	if r, err := f.client().Latest(context.Background(), "html2md", 1); err != nil || r.Version != "0.2.0" {
		t.Fatalf("release %+v, %v", r, err)
	}
}
