// Package release is the source of module updates: the project's GitHub
// Releases, one tag per module version (html2md/v0.3.0) with the .wasm,
// the manifest and a SHA256SUMS file (ADR-0041). Only GitHub's hosts are
// reached, only at public addresses, with bounded sizes and time.
package release

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/igorrius/ganglion/internal/ghfetch"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
)

// Limits of a download.
const (
	MaxFileBytes = 8 << 20 // a .wasm, a manifest or SHA256SUMS
	maxListBytes = 4 << 20 // the releases list
)

// ProjectRepo is where ganglion's own module releases are published.
const ProjectRepo = "igorrius/ganglion"

// GitHub finds and fetches a repository's module releases.
type GitHub struct {
	repo string
	api  string
	opts []ghfetch.Option
	c    *ghfetch.Client
}

// Option configures a GitHub source.
type Option func(*GitHub)

// WithAPI sets the API's base URL (tests).
func WithAPI(u string) Option { return func(g *GitHub) { g.api = strings.TrimSuffix(u, "/") } }

// WithHosts replaces the hosts that may be reached (tests).
func WithHosts(hosts ...string) Option {
	return func(g *GitHub) { g.opts = append(g.opts, ghfetch.WithHosts(hosts...)) }
}

// WithTransport replaces the transport, keeping the host and redirect
// rules, and lets a test server's plain http through (tests).
func WithTransport(rt http.RoundTripper) Option {
	return func(g *GitHub) { g.opts = append(g.opts, ghfetch.WithTransport(rt)) }
}

// NewGitHub is the source for repo ("owner/name").
func NewGitHub(repo string, opts ...Option) *GitHub {
	g := &GitHub{repo: repo, api: "https://api.github.com"}
	for _, o := range opts {
		o(g)
	}
	g.c = ghfetch.New(g.opts...)
	return g
}

func (g *GitHub) get(ctx context.Context, raw string, limit int64) ([]byte, error) {
	return g.c.Get(ctx, raw, limit)
}

type ghRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

func (r ghRelease) asset(name string) string {
	for _, a := range r.Assets {
		if a.Name == name {
			return a.URL
		}
	}
	return ""
}

// semver reads "<module>/vMAJOR.MINOR.PATCH"; ok is false for anything else.
func semver(tag, module string) (v [3]int, s string, ok bool) {
	rest, found := strings.CutPrefix(tag, module+"/v")
	parts := strings.Split(rest, ".")
	if !found || len(parts) != 3 {
		return v, "", false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || strconv.Itoa(n) != p {
			return v, "", false
		}
		v[i] = n
	}
	return v, rest, true
}

// Latest is the newest stable release of module whose manifest speaks
// abi; newer releases for another ABI are passed over.
func (g *GitHub) Latest(ctx context.Context, module string, abi int) (modstore.Release, error) {
	body, err := g.get(ctx, g.api+"/repos/"+g.repo+"/releases?per_page=100", maxListBytes)
	if err != nil {
		return modstore.Release{}, err
	}
	var all []ghRelease
	if err := json.Unmarshal(body, &all); err != nil {
		return modstore.Release{}, fmt.Errorf("release: the list: %w", err)
	}
	type cand struct {
		v   [3]int
		s   string
		rel ghRelease
	}
	var cands []cand
	for _, r := range all {
		if v, s, ok := semver(r.Tag, module); ok && !r.Draft && !r.Prerelease {
			cands = append(cands, cand{v, s, r})
		}
	}
	slices.SortFunc(cands, func(a, b cand) int { return slices.Compare(b.v[:], a.v[:]) })
	for _, c := range cands {
		rel, err := g.describe(ctx, module, c.s, c.rel)
		if errors.Is(err, errIncomplete) {
			continue // an incomplete release is passed over, not the end of the search
		}
		if err != nil {
			return modstore.Release{}, err // a failed read must not select an older release
		}
		manifest, err := g.get(ctx, rel.ManifestURL, MaxFileBytes)
		if err != nil {
			return modstore.Release{}, err
		}
		if m, err := runner.ParseManifest(manifest); err == nil && m.ABI == abi {
			return rel, nil
		}
	}
	return modstore.Release{}, fmt.Errorf("release: no release of %s for ABI %d", module, abi)
}

// errIncomplete is a release published without all its files.
var errIncomplete = errors.New("release: incomplete")

// describe reads a release's sums. Only errIncomplete says the release itself
// is short of something; any other error is a read that failed.
func (g *GitHub) describe(ctx context.Context, module, version string, r ghRelease) (modstore.Release, error) {
	rel := modstore.Release{Module: module, Version: version, Tag: r.Tag,
		WasmURL: r.asset(module + ".wasm"), ManifestURL: r.asset(module + ".yaml")}
	sumsURL := r.asset("SHA256SUMS")
	if rel.WasmURL == "" || rel.ManifestURL == "" || sumsURL == "" {
		return modstore.Release{}, fmt.Errorf("%w: %s lacks %s.wasm, %s.yaml or SHA256SUMS", errIncomplete, r.Tag, module, module)
	}
	sums, err := g.get(ctx, sumsURL, MaxFileBytes)
	if err != nil {
		return modstore.Release{}, err
	}
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 2 {
			continue
		}
		switch strings.TrimPrefix(f[1], "*") {
		case module + ".wasm":
			rel.SHA256 = strings.ToLower(f[0])
		case module + ".yaml":
			rel.ManifestSHA256 = strings.ToLower(f[0])
		}
	}
	if rel.SHA256 == "" || rel.ManifestSHA256 == "" {
		return modstore.Release{}, fmt.Errorf("%w: %s's SHA256SUMS lacks a file", errIncomplete, r.Tag)
	}
	return rel, nil
}

// Fetch downloads the release's module and manifest; the store checks
// them against the sums.
func (g *GitHub) Fetch(ctx context.Context, r modstore.Release) (wasm, manifest []byte, err error) {
	if wasm, err = g.get(ctx, r.WasmURL, MaxFileBytes); err != nil {
		return nil, nil, err
	}
	if manifest, err = g.get(ctx, r.ManifestURL, MaxFileBytes); err != nil {
		return nil, nil, err
	}
	return wasm, manifest, nil
}
