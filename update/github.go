package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/igorrius/ganglion/internal/ghfetch"
)

// Errors of finding and fetching a release.
var (
	// ErrFetch is a network failure; the cause is for the log, never for
	// the owner's eyes (R-UPD-7).
	ErrFetch = errors.New("update: the release could not be fetched")
	// ErrNotFound is a tag with no published release.
	ErrNotFound = errors.New("update: no such release")
	// ErrIncomplete is a release published without one of its files.
	ErrIncomplete = errors.New("update: the release lacks a file")
	// ErrNoPlatform is a release with no binary for this platform.
	ErrNoPlatform = errors.New("update: the release has no binary for this platform")
)

// Where the project's core releases are, and the limits of a download.
const (
	// Repo is the repository whose releases are core releases.
	Repo = "igorrius/ganglion"
	// BinaryFile is the name a release's binary has in its folder.
	BinaryFile = "ganglion"

	// MaxBinaryBytes bounds a core binary; MaxSignatureBytes its signature.
	MaxBinaryBytes    = 256 << 20
	MaxSignatureBytes = 1 << 10

	maxListBytes = 4 << 20
)

// Release is a published core release as the node uses it. Only the
// version, the date and a link the node builds itself are kept of what
// the release page says (R-UPD-7): its name and notes are never decoded.
type Release struct {
	Version   Version
	Tag       string
	Published time.Time
	// Page is https://github.com/<repo>/releases/tag/<tag>, built from the
	// tag.
	Page   string
	assets map[string]string // asset name -> download URL
}

// Source finds and downloads the project's core releases from GitHub.
type Source struct {
	repo string
	api  string
	c    *ghfetch.Client
}

// SourceOption configures a Source.
type SourceOption func(*Source)

// WithAPI sets the API's base URL (tests).
func WithAPI(u string) SourceOption { return func(s *Source) { s.api = strings.TrimSuffix(u, "/") } }

// NewSource is the source of the project's core releases over c.
func NewSource(c *ghfetch.Client, opts ...SourceOption) *Source {
	s := &Source{repo: Repo, api: "https://api.github.com", c: c}
	for _, o := range opts {
		o(s)
	}
	return s
}

// ghRelease is the part of GitHub's release object the node reads.
type ghRelease struct {
	Tag        string     `json:"tag_name"`
	Draft      bool       `json:"draft"`
	Prerelease bool       `json:"prerelease"`
	Published  *time.Time `json:"published_at"`
	Assets     []struct {
		Name string `json:"name"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// release turns a GitHub release into a Release; ok is false when its tag
// is not a core version.
func (s *Source) release(r ghRelease) (Release, bool) {
	v, err := ParseVersion(r.Tag)
	if err != nil {
		return Release{}, false
	}
	rel := Release{Version: v, Tag: r.Tag, Page: "https://github.com/" + s.repo + "/releases/tag/" + r.Tag,
		assets: map[string]string{}}
	if r.Published != nil {
		rel.Published = *r.Published
	}
	for _, a := range r.Assets {
		rel.assets[a.Name] = a.URL
	}
	return rel, true
}

func (s *Source) get(ctx context.Context, url string, limit int64) ([]byte, error) {
	b, err := s.c.Get(ctx, url, limit)
	if err != nil {
		var se *ghfetch.StatusError
		if errors.As(err, &se) && se.Code == http.StatusNotFound {
			return nil, fmt.Errorf("%w: %w", ErrNotFound, err)
		}
		return nil, fmt.Errorf("%w: %w", ErrFetch, err)
	}
	return b, nil
}

// Latest is the newest stable release whose tag is a core version; drafts,
// pre-releases and module tags (html2md/v0.1.0) are passed over.
func (s *Source) Latest(ctx context.Context) (Release, error) {
	body, err := s.get(ctx, s.api+"/repos/"+s.repo+"/releases?per_page=100", maxListBytes)
	if err != nil {
		return Release{}, err
	}
	var all []ghRelease
	if err := json.Unmarshal(body, &all); err != nil {
		return Release{}, fmt.Errorf("%w: the list: %w", ErrFetch, err)
	}
	var best Release
	for _, g := range all {
		rel, ok := s.release(g)
		if !ok || g.Draft || g.Prerelease || len(rel.Version.Pre) > 0 {
			continue
		}
		if best.Version.IsZero() || Compare(rel.Version, best.Version) > 0 {
			best = rel
		}
	}
	if best.Version.IsZero() {
		return Release{}, fmt.Errorf("%w: no stable release", ErrNotFound)
	}
	return best, nil
}

// Get is the release of tag, a pre-release included; a draft is refused.
func (s *Source) Get(ctx context.Context, tag string) (Release, error) {
	if _, err := ParseVersion(tag); err != nil {
		return Release{}, fmt.Errorf("%w: %w", ErrNotFound, err)
	}
	body, err := s.get(ctx, s.api+"/repos/"+s.repo+"/releases/tags/"+tag, maxListBytes)
	if err != nil {
		return Release{}, err
	}
	var g ghRelease
	if err := json.Unmarshal(body, &g); err != nil {
		return Release{}, fmt.Errorf("%w: the release: %w", ErrFetch, err)
	}
	rel, ok := s.release(g)
	if !ok || g.Tag != tag || g.Draft {
		return Release{}, fmt.Errorf("%w: %s", ErrNotFound, tag)
	}
	return rel, nil
}

// Fetcher is what Store.Install reads a release from.
type Fetcher interface {
	// Get is the release of a tag.
	Get(ctx context.Context, tag string) (Release, error)
	// Fetch downloads the release's files for platform ("linux/amd64")
	// into dir: SHA256SUMS, its signature, release.json and the binary as
	// BinaryFile.
	Fetch(ctx context.Context, r Release, dir, platform string) error
}

// small is a release file that is copied into memory first.
type small struct {
	name  string
	limit int64
}

// Fetch implements Fetcher. A missing asset is ErrIncomplete, a missing
// binary ErrNoPlatform; the store verifies what arrives.
func (s *Source) Fetch(ctx context.Context, r Release, dir, platform string) error {
	goos, goarch, ok := strings.Cut(platform, "/")
	if !ok || goos == "" || goarch == "" {
		return fmt.Errorf("%w: %q", ErrNoPlatform, platform)
	}
	bin := BinaryName(goos, goarch)
	files := []small{{SumsFile, MaxSumsBytes}, {SignatureFile, MaxSignatureBytes}, {ManifestFile, MaxManifestBytes}}
	for _, f := range files {
		if r.assets[f.name] == "" {
			return fmt.Errorf("%w: %s lacks %s", ErrIncomplete, r.Tag, f.name)
		}
	}
	if r.assets[bin] == "" {
		return fmt.Errorf("%w: %s", ErrNoPlatform, bin)
	}
	for _, f := range files {
		if err := s.download(ctx, r.assets[f.name], filepath.Join(dir, f.name), f.limit, 0o600); err != nil {
			return err
		}
	}
	return s.download(ctx, r.assets[bin], filepath.Join(dir, BinaryFile), MaxBinaryBytes, 0o600)
}

// download streams url into a new file at path, at most limit bytes.
func (s *Source) download(ctx context.Context, url, path string, limit int64, mode os.FileMode) (err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	defer func() {
		if cerr := f.Close(); err == nil && cerr != nil {
			err = fmt.Errorf("update: %w", cerr)
		}
	}()
	if _, err := s.c.Copy(ctx, url, f, limit); err != nil {
		return fmt.Errorf("%w: %w", ErrFetch, err)
	}
	return nil
}
