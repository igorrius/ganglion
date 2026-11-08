package proc

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

var start = time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)

func scope() status.Scope {
	return status.Scope{Now: start.Add(76 * time.Hour), Location: time.UTC}
}

// lines asks src and returns its lines by text key.
func lines(t *testing.T, src *Source) map[texts.Key]status.Check {
	t.Helper()
	out := map[texts.Key]status.Check{}
	cs, err := src.Status(t.Context(), scope())
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cs {
		if c.Section != status.SectionNode {
			t.Errorf("%s in %s", c.Line, c.Section)
		}
		out[c.Line] = c
	}
	return out
}

func noMemory() (int64, int64, bool) { return 0, 0, false }

func TestVersionLine(t *testing.T) {
	built := time.Date(2026, 11, 7, 21, 52, 0, 0, time.UTC)
	for name, tc := range map[string]struct {
		in   buildinfo.Info
		key  texts.Key
		vars texts.Vars
	}{
		"commit": {buildinfo.Info{Version: "v0.1.0", Commit: "4ab9e56abcdef", CommitTime: built, Go: "go1.27.3", OS: "linux", Arch: "arm64"},
			texts.StatusVersion, texts.Vars{"version": "v0.1.0", "commit": "4ab9e56", "date": "2026-11-07", "go": "go1.27.3", "platform": "linux/arm64"}},
		"modified": {buildinfo.Info{Version: "dev", Commit: "4ab9e56", CommitTime: built, Modified: true, Go: "go1.27.3", OS: "linux", Arch: "amd64"},
			texts.StatusVersionModified, texts.Vars{"version": "dev", "commit": "4ab9e56", "date": "2026-11-07", "go": "go1.27.3", "platform": "linux/amd64"}},
		"no vcs": {buildinfo.Info{Version: "dev", Go: "go1.27.3", OS: "linux", Arch: "amd64"},
			texts.StatusVersionUnknown, texts.Vars{"version": "dev", "go": "go1.27.3", "platform": "linux/amd64"}},
	} {
		src := New(start, Files{}, WithBuild(tc.in), WithMemory(noMemory))
		c, ok := lines(t, src)[tc.key]
		if !ok || c.State != status.OK {
			t.Errorf("%s: no %s line", name, tc.key)
			continue
		}
		for k, want := range tc.vars {
			if c.Vars[k] != want {
				t.Errorf("%s: %s = %q, want %q", name, k, c.Vars[k], want)
			}
		}
	}
}

func TestUptime(t *testing.T) {
	c := lines(t, New(start, Files{}, WithMemory(noMemory)))[texts.StatusUptime]
	if c.Spans["uptime"] != 76*time.Hour {
		t.Errorf("uptime %v", c.Spans["uptime"])
	}
}

func TestMemory(t *testing.T) {
	mem := func() (int64, int64, bool) { return 95 << 20, 99 << 20, true }
	none := func() int64 { return math.MaxInt64 }
	c := lines(t, New(start, Files{}, WithMemory(mem), WithLimit(none)))[texts.StatusMemoryNoLimit]
	if c.State != status.OK || c.Vars["rss"] != "95 MB" || c.Vars["peak"] != "99 MB" {
		t.Errorf("no limit: %+v", c)
	}
	c = lines(t, New(start, Files{}, WithMemory(mem), WithLimit(func() int64 { return 100 << 20 })))[texts.StatusMemory]
	if c.State != status.Warn || c.Reason != texts.StatusReasonMemoryHigh || c.Vars["limit"] != "100 MB" {
		t.Errorf("near the limit: %+v", c)
	}
	c = lines(t, New(start, Files{}, WithMemory(mem), WithLimit(func() int64 { return 200 << 20 })))[texts.StatusMemory]
	if c.State != status.OK || c.Reason != "" {
		t.Errorf("under the limit: %+v", c)
	}
	got := lines(t, New(start, Files{}, WithMemory(noMemory)))
	if _, ok := got[texts.StatusMemory]; ok {
		t.Error("a memory line without a reading")
	}
	if _, ok := got[texts.StatusMemoryNoLimit]; ok {
		t.Error("a memory line without a reading")
	}
}

func TestFiles(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "ganglion.db")
	write(t, db, 3000)
	write(t, db+"-wal", 2000)
	logs := filepath.Join(dir, "logs")
	if err := os.Mkdir(logs, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(logs, "a.log"), 1024)
	write(t, filepath.Join(logs, "b.log"), 1024)
	c := lines(t, New(start, Files{DB: db, Logs: logs}, WithMemory(noMemory)))[texts.StatusFiles]
	if c.Vars["db"] != "4 KB" || c.Vars["logs"] != "2 KB" {
		t.Errorf("files %+v", c.Vars)
	}
	c = lines(t, New(start, Files{DB: db}, WithMemory(noMemory)))[texts.StatusFilesNoLogs]
	if c.Vars["db"] != "4 KB" {
		t.Errorf("no logs %+v", c)
	}
}

func write(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, n), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadsProcStatus(t *testing.T) {
	rss, peak, ok := parseStatus([]byte("Name:\tganglion\nVmHWM:\t  101376 kB\nVmRSS:\t   46080 kB\n"))
	if !ok || rss != 46080<<10 || peak != 101376<<10 {
		t.Errorf("rss %d peak %d ok %v", rss, peak, ok)
	}
	if _, _, ok := parseStatus([]byte("Name:\tganglion\n")); ok {
		t.Error("read a status without the fields")
	}
}
