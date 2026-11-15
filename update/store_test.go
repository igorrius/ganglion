package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const platform = "linux/amd64"

var testNow = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// fakeSource serves one release per tag, built by build; fetched counts
// the downloads.
type fakeSource struct {
	build   func(tag string) map[string][]byte // file name -> content, in the folder's names
	getErr  error
	fetched int
}

func (f *fakeSource) Get(_ context.Context, tag string) (Release, error) {
	if f.getErr != nil {
		return Release{}, f.getErr
	}
	return Release{Tag: tag}, nil
}

func (f *fakeSource) Fetch(_ context.Context, r Release, dir, _ string) error {
	f.fetched++
	for name, data := range f.build(r.Tag) {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return err
		}
	}
	return nil
}

type releaseOpts struct {
	key         ed25519.PrivateKey
	manifestTag string // release.json's version when not the tag
	env         int
	tamper      bool // the binary changes after the sums were made
}

func hexSum(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// signed builds the files of a release the way the release workflow does.
func signed(tag string, o releaseOpts) map[string][]byte {
	bin := []byte("binary of " + tag)
	mt := o.manifestTag
	if mt == "" {
		mt = tag
	}
	env := o.env
	if env == 0 {
		env = 1
	}
	manifest := []byte(fmt.Sprintf(`{"version":%q,"commit":%q,"env":%d}`, mt, strings.Repeat("a", 40), env))
	sums := []byte(hexSum(bin) + "  " + BinaryName("linux", "amd64") + "\n" + hexSum(manifest) + "  " + ManifestFile + "\n")
	if o.tamper {
		bin = []byte("another binary")
	}
	return map[string][]byte{BinaryFile: bin, ManifestFile: manifest, SumsFile: sums, SignatureFile: ed25519.Sign(o.key, sums)}
}

type kit struct {
	t      *testing.T
	store  *Store
	key    ed25519.PrivateKey
	src    *fakeSource
	opts   releaseOpts
	copied []string
}

func newKit(t *testing.T, opts ...StoreOption) *kit {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k := &kit{t: t, key: priv, opts: releaseOpts{key: priv}}
	k.src = &fakeSource{build: func(tag string) map[string][]byte { return signed(tag, k.opts) }}
	all := append([]StoreOption{WithKeys([]Key{{Name: "test", Pub: pub}}), WithPlatform(platform),
		WithClock(func() time.Time { return testNow }),
		WithCopyDB(func(_ context.Context, dst string) error {
			k.copied = append(k.copied, dst)
			return os.WriteFile(dst, []byte("db"), 0o600)
		})}, opts...)
	k.store, err = Open(filepath.Join(t.TempDir(), "releases"), all...)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func (k *kit) install(version, running string) (Installed, error) {
	k.t.Helper()
	r, err := ParseVersion(running)
	if err != nil {
		k.t.Fatal(err)
	}
	return k.store.Install(context.Background(), k.src, version, r)
}

func (k *kit) stateBytes() []byte {
	b, err := os.ReadFile(filepath.Join(k.store.Dir(), StateFile))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		k.t.Fatal(err)
	}
	return b
}

// refused asserts an error and that the state file is byte-for-byte what
// it was and no folder of the version appeared.
func (k *kit) refused(want error, version string, run func() error) {
	k.t.Helper()
	before := k.stateBytes()
	err := run()
	if !errors.Is(err, want) {
		k.t.Fatalf("%v, want %v", err, want)
	}
	if after := k.stateBytes(); !bytes.Equal(before, after) {
		k.t.Fatalf("state changed:\n%s\n->\n%s", before, after)
	}
	if _, err := os.Stat(filepath.Join(k.store.Dir(), version)); version != "" && err == nil {
		k.t.Fatalf("the folder of %s is in place", version)
	}
	left, _ := filepath.Glob(filepath.Join(k.store.Dir(), incomingFx+"*"))
	if len(left) != 0 {
		k.t.Fatalf("staging left behind: %v", left)
	}
}

func TestInstallActivatesAVerifiedRelease(t *testing.T) {
	k := newKit(t)
	got, err := k.install("v0.3.0", "v0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "v0.3.0" || got.Previous != "" || got.Key != "test" || got.Manifest.Env != 1 {
		t.Fatalf("installed %+v", got)
	}
	st, err := k.store.State()
	if err != nil {
		t.Fatal(err)
	}
	want := State{Active: "v0.3.0", Pending: &Pending{Version: "v0.3.0", Since: testNow}}
	if st.Active != want.Active || st.Previous != "" || st.Pending == nil || *st.Pending != *want.Pending || st.Fallback != nil {
		t.Fatalf("state %+v", st)
	}
	fi, err := os.Stat(k.store.BinaryPath("v0.3.0"))
	if err != nil || fi.Mode().Perm() != 0o500 {
		t.Fatalf("binary: %v %v", fi, err)
	}
	if b, err := os.ReadFile(filepath.Join(k.store.Dir(), "v0.3.0", BeforeDB)); err != nil || string(b) != "db" {
		t.Fatalf("before.db: %q %v", b, err)
	}
	for _, f := range []string{SumsFile, SignatureFile, ManifestFile} {
		if _, err := os.Stat(filepath.Join(k.store.Dir(), "v0.3.0", f)); err != nil {
			t.Error(err)
		}
	}
}

func TestInstallKeepsOnlyActiveAndPreviousFolders(t *testing.T) {
	k := newKit(t)
	for _, v := range []string{"v0.3.0", "v0.4.0", "v0.5.0"} {
		if _, err := k.install(v, "v0.2.1"); err != nil {
			t.Fatal(err)
		}
	}
	st, _ := k.store.State()
	if st.Active != "v0.5.0" || st.Previous != "v0.4.0" {
		t.Fatalf("state %+v", st)
	}
	if _, err := os.Stat(filepath.Join(k.store.Dir(), "v0.3.0")); err == nil {
		t.Fatal("an old release is still on disk")
	}
	for _, v := range []string{"v0.4.0", "v0.5.0"} {
		if _, err := os.Stat(k.store.BinaryPath(v)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInstallRefusals(t *testing.T) {
	cases := map[string]struct {
		version, running string
		opts             func(*kit)
		image            int
		want             error
	}{
		"signed by another key": {"v0.3.0", "v0.2.1", func(k *kit) {
			_, other, _ := ed25519.GenerateKey(rand.Reader)
			k.opts.key = other
		}, 0, ErrBadSignature},
		"binary changed after the sums":      {"v0.3.0", "v0.2.1", func(k *kit) { k.opts.tamper = true }, 0, ErrSumMismatch},
		"release.json names another version": {"v0.3.0", "v0.2.1", func(k *kit) { k.opts.manifestTag = "v0.9.9" }, 0, ErrBadManifest},
		"an older version":                   {"v0.1.0", "v0.2.1", nil, 0, ErrNotNewer},
		"the same version":                   {"v0.2.1", "v0.2.1", nil, 0, ErrNotNewer},
		"a downgrade by a pre-release tag":   {"v0.2.1-rc.1", "v0.2.1", nil, 0, ErrNotNewer},
		"not a version":                      {"../x", "v0.2.1", nil, 0, ErrBadVersion},
		"the image is too old":               {"v0.3.0", "v0.2.1", func(k *kit) { k.opts.env = 3 }, 2, ErrEnv},
		"not found":                          {"v0.3.0", "v0.2.1", func(k *kit) { k.src.getErr = ErrNotFound }, 0, ErrNotFound},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			k := newKit(t, WithImageEnv(c.image))
			if _, err := k.install("v0.2.5", "v0.2.1"); err != nil { // a state to protect
				t.Fatal(err)
			}
			if c.opts != nil {
				c.opts(k)
			}
			k.refused(c.want, c.version, func() error { _, err := k.install(c.version, c.running); return err })
		})
	}
}

func TestInstallWithoutAnImageSkipsTheEnvCheck(t *testing.T) {
	k := newKit(t)
	k.opts.env = 99
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
}

func TestInstallRefusesAnInstalledActiveVersion(t *testing.T) {
	k := newKit(t)
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	before := k.stateBytes()
	if _, err := k.install("v0.3.0", "v0.2.1"); !errors.Is(err, ErrNotNewer) {
		t.Fatalf("%v", err)
	}
	if !bytes.Equal(before, k.stateBytes()) {
		t.Fatal("state changed")
	}
}

func TestAFailedDatabaseCopyLeavesNothingInPlace(t *testing.T) {
	boom := errors.New("disk full")
	k := newKit(t, WithCopyDB(func(context.Context, string) error { return boom }))
	k.refused(boom, "v0.3.0", func() error { _, err := k.install("v0.3.0", "v0.2.1"); return err })
}

func TestASecondInstallWhileTheLockIsHeldIsBusy(t *testing.T) {
	k := newKit(t)
	unlock, err := lockDir(filepath.Join(k.store.Dir(), LockFile))
	if err != nil {
		t.Fatal(err)
	}
	k.refused(ErrBusy, "v0.3.0", func() error { _, err := k.install("v0.3.0", "v0.2.1"); return err })
	if k.src.fetched != 0 {
		t.Fatal("a busy install downloaded")
	}
	if _, err := k.store.Rollback(context.Background()); !errors.Is(err, ErrBusy) {
		t.Fatalf("rollback: %v", err)
	}
	unlock()
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
}

func TestStateWithAnUnknownFieldOrABadVersionIsRefused(t *testing.T) {
	for name, content := range map[string]string{
		"unknown field":  `{"active":"v0.3.0","previous":"","confirmed":"","extra":1}`,
		"path as active": `{"active":"../x","previous":"","confirmed":""}`,
		"bad pending":    `{"active":"","previous":"","confirmed":"","pending":{"version":"v1/../../x","attempts":0,"since":"2026-11-15T12:00:00Z"}}`,
		"bad fallback":   `{"active":"","previous":"","confirmed":"","fallback":{"from":"v1.0.0","to":"/etc","restored_db":false,"at":"2026-11-15T12:00:00Z"}}`,
		"trailing data":  `{"active":"","previous":"","confirmed":""} {}`,
		"not json":       `nope`,
	} {
		t.Run(name, func(t *testing.T) {
			k := newKit(t)
			if err := os.WriteFile(filepath.Join(k.store.Dir(), StateFile), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := k.store.State(); !errors.Is(err, ErrBadState) {
				t.Fatalf("State: %v", err)
			}
			k.refused(ErrBadState, "v0.3.0", func() error { _, err := k.install("v0.3.0", "v0.2.1"); return err })
			k.refused(ErrBadState, "v0.3.0", func() error { _, err := k.store.Rollback(context.Background()); return err })
		})
	}
}

func TestStateRoundTripsWithTheDocumentedShape(t *testing.T) {
	k := newKit(t)
	in := `{"active":"v0.3.0","previous":"v0.2.1","pending":{"version":"v0.3.0","attempts":1,"since":"2026-11-15T12:00:00Z"},` +
		`"fallback":{"from":"v0.3.0","to":"v0.2.1","restored_db":true,"at":"2026-11-15T12:00:00Z"},"confirmed":""}`
	if err := os.WriteFile(filepath.Join(k.store.Dir(), StateFile), []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := k.store.State()
	if err != nil || st.Pending.Attempts != 1 || !st.Fallback.RestoredDB || st.Previous != "v0.2.1" {
		t.Fatalf("%+v %v", st, err)
	}
}

func TestRollbackSwapsActiveAndPrevious(t *testing.T) {
	k := newKit(t)
	k.refused(ErrNoPrevious, "", func() error { _, err := k.store.Rollback(context.Background()); return err })
	for _, v := range []string{"v0.3.0", "v0.4.0"} {
		if _, err := k.install(v, "v0.2.1"); err != nil {
			t.Fatal(err)
		}
	}
	to, err := k.store.Rollback(context.Background())
	if err != nil || to != "v0.3.0" {
		t.Fatalf("%q %v", to, err)
	}
	st, _ := k.store.State()
	if st.Active != "v0.3.0" || st.Previous != "v0.4.0" || st.Pending == nil || *st.Pending != (Pending{Version: "v0.3.0", Since: testNow}) {
		t.Fatalf("state %+v", st)
	}
	// Back to the floor: the image's binary confirms at once, no pending.
	k2 := newKit(t)
	if _, err := k2.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	to, err = k2.store.Rollback(context.Background())
	if err != nil || to != "" {
		t.Fatalf("%q %v", to, err)
	}
	st, _ = k2.store.State()
	if st.Active != "" || st.Previous != "v0.3.0" || st.Pending != nil {
		t.Fatalf("state %+v", st)
	}
	if to, err = k2.store.Rollback(context.Background()); err != nil || to != "v0.3.0" {
		t.Fatalf("forward again: %q %v", to, err)
	}
}

func TestRollbackWithTheTargetGoneIsRefused(t *testing.T) {
	k := newKit(t)
	for _, v := range []string{"v0.3.0", "v0.4.0"} {
		if _, err := k.install(v, "v0.2.1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(filepath.Join(k.store.Dir(), "v0.3.0")); err != nil {
		t.Fatal(err)
	}
	k.refused(ErrNoPrevious, "", func() error { _, err := k.store.Rollback(context.Background()); return err })
}

func TestWriteStateLeavesNoTempFile(t *testing.T) {
	k := newKit(t)
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	left, _ := filepath.Glob(filepath.Join(k.store.Dir(), ".state-*"))
	if len(left) != 0 {
		t.Fatalf("%v", left)
	}
	fi, err := os.Stat(filepath.Join(k.store.Dir(), StateFile))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", fi, err)
	}
	if fi, err := os.Stat(k.store.Dir()); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir %v %v", fi, err)
	}
}

// A state that cannot be written after the folder is in place removes the
// folder, unless the state still names it as active or previous.
func TestPlaceKeepsTheFolderTheStateNames(t *testing.T) {
	for _, tc := range []struct {
		version string
		kept    bool
	}{{"v0.3.0", true}, {"v0.4.0", true}, {"v0.5.0", false}} {
		t.Run(tc.version, func(t *testing.T) {
			k := newKit(t)
			incoming := filepath.Join(k.store.Dir(), incomingFx+"x")
			if err := os.Mkdir(incoming, 0o700); err != nil {
				t.Fatal(err)
			}
			// A bad confirmed version makes the state unwritable.
			st := State{Active: "v0.4.0", Previous: "v0.3.0", Confirmed: "junk"}
			err := k.store.place(st, incoming, &Installed{Version: tc.version})
			if !errors.Is(err, ErrBadState) {
				t.Fatalf("%v", err)
			}
			_, serr := os.Stat(filepath.Join(k.store.Dir(), tc.version))
			if kept := serr == nil; kept != tc.kept {
				t.Fatalf("folder kept = %v, want %v", kept, tc.kept)
			}
		})
	}
}
