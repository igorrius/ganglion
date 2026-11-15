// Package buildinfo says what this binary is: its version, the commit it
// was built from, and the toolchain. The version command, the start log
// line and the telemetry resource all read it (ADR-0029).
package buildinfo

import (
	"runtime"
	"runtime/debug"
	"time"
)

// version and commit are set at build time:
// -ldflags "-X github.com/igorrius/ganglion/internal/buildinfo.version=$(git describe)".
// commit is for builds without the repository's history, such as the image,
// whose context has no .git: Go stamps no VCS information there.
var version, commit string

// Info describes the running binary.
type Info struct {
	Version    string    `json:"version"`
	Commit     string    `json:"commit,omitempty"`
	CommitTime time.Time `json:"commit_time,omitzero"`
	Modified   bool      `json:"modified"` // built from a checkout with uncommitted changes
	Go         string    `json:"go"`
	OS         string    `json:"os"`
	Arch       string    `json:"arch"`
}

// Read returns the running binary's Info.
func Read() Info {
	bi, ok := debug.ReadBuildInfo()
	return withCommit(from(version, bi, ok), commit)
}

// from builds Info from the linker version and Go's build information.
// The version is the linker's when set, else the module version Go records
// for `go install …@vX.Y.Z`, else "dev". Commit, time and the dirty flag
// are the VCS stamps Go writes into every build from a checkout.
func from(ldVersion string, bi *debug.BuildInfo, ok bool) Info {
	in := Info{Version: ldVersion, Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH}
	if !ok || bi == nil {
		if in.Version == "" {
			in.Version = "dev"
		}
		return in
	}
	if in.Version == "" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		in.Version = bi.Main.Version
	}
	if in.Version == "" {
		in.Version = "dev"
	}
	if bi.GoVersion != "" {
		in.Go = bi.GoVersion
	}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			in.Commit = s.Value
		case "vcs.time":
			in.CommitTime, _ = time.Parse(time.RFC3339, s.Value)
		case "vcs.modified":
			in.Modified = s.Value == "true"
		case "GOOS":
			in.OS = s.Value
		case "GOARCH":
			in.Arch = s.Value
		}
	}
	return in
}

// withCommit fills in the linker's commit when Go stamped none.
func withCommit(in Info, ldCommit string) Info {
	if in.Commit == "" {
		in.Commit = ldCommit
	}
	return in
}

// Short is the version with the short commit: "v0.1.2 (86cda06, dirty)".
func (in Info) Short() string {
	if in.Commit == "" {
		return in.Version
	}
	c := in.Commit[:min(7, len(in.Commit))]
	if in.Modified {
		c += ", dirty"
	}
	return in.Version + " (" + c + ")"
}
