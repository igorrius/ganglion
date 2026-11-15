package buildinfo

import (
	"runtime/debug"
	"testing"
	"time"
)

func info(module string, settings ...debug.BuildSetting) *debug.BuildInfo {
	return &debug.BuildInfo{GoVersion: "go1.27.3", Main: debug.Module{Path: "github.com/igorrius/ganglion", Version: module}, Settings: settings}
}

var vcs = []debug.BuildSetting{
	{Key: "GOOS", Value: "linux"}, {Key: "GOARCH", Value: "arm64"},
	{Key: "vcs.revision", Value: "86cda06f1d2e3c4b5a6978877665544332211000"},
	{Key: "vcs.time", Value: "2026-11-02T14:59:02Z"},
	{Key: "vcs.modified", Value: "true"},
}

func TestFromPrefersTheLinkerVersion(t *testing.T) {
	got := from("v0.1.1-5-g86cda06", info("(devel)", vcs...), true)
	want := Info{Version: "v0.1.1-5-g86cda06", Commit: "86cda06f1d2e3c4b5a6978877665544332211000",
		CommitTime: time.Date(2026, 11, 2, 14, 59, 2, 0, time.UTC), Modified: true, Go: "go1.27.3", OS: "linux", Arch: "arm64"}
	if got != want {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestFromFallsBackToTheModuleVersionThenDev(t *testing.T) {
	if got := from("", info("v0.1.2"), true).Version; got != "v0.1.2" {
		t.Errorf("go install …@v0.1.2: %q", got)
	}
	if got := from("", info("(devel)"), true).Version; got != "dev" {
		t.Errorf("local build: %q", got)
	}
	if got := from("", nil, false); got.Version != "dev" || got.Commit != "" {
		t.Errorf("no build info: %+v", got)
	}
}

func TestTheLinkerCommitFillsABuildWithoutVCS(t *testing.T) {
	const c = "0123456789abcdef0123456789abcdef01234567"
	if got := withCommit(from("v0.3.1", info("(devel)"), true), c); got.Commit != c {
		t.Errorf("image build: commit %q", got.Commit)
	}
	if got := withCommit(from("v0.3.1", info("(devel)", vcs...), true), c); got.Commit != vcs[2].Value {
		t.Errorf("a VCS stamp wins: commit %q", got.Commit)
	}
}

func TestShort(t *testing.T) {
	for _, tc := range []struct {
		in   Info
		want string
	}{
		{Info{Version: "v0.1.2", Commit: "86cda06f1d2e", Modified: true}, "v0.1.2 (86cda06, dirty)"},
		{Info{Version: "v0.1.2", Commit: "86cda06f1d2e"}, "v0.1.2 (86cda06)"},
		{Info{Version: "dev"}, "dev"},
	} {
		if got := tc.in.Short(); got != tc.want {
			t.Errorf("Short() = %q, want %q", got, tc.want)
		}
	}
}
