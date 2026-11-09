package host

import (
	"strings"
	"testing"
	"time"
)

func TestRenderSaysWhatItKnows(t *testing.T) {
	r := Report{
		Machine: Machine{MemTotal: 16 << 30, MemAvailable: 9 << 30, Load: [3]float64{0.5, 0.6, 0.7}, CPUs: 8, Uptime: 76 * time.Hour, OK: true},
		RSS:     45 << 20, Peak: 48 << 20,
		Disks:  []DiskSpace{{Path: "/", Free: 400 << 30, Total: 900 << 30, OK: true}},
		Places: []Usage{{Place: Place{Name: PlaceClaude, MaxAge: 24 * time.Hour}, Bytes: 1 << 20, Entries: 3}},
	}
	got := Render(r)
	for _, want := range []string{"16.0 GB total", "9.0 GB available", "45 MB", "0.50 0.60 0.70", "8 CPUs",
		"/: 400.0 GB free of 900.0 GB", "claude work folder: 1 MB in 3 files, cleaned after 24h"} {
		if !strings.Contains(got, want) {
			t.Errorf("render lacks %q:\n%s", want, got)
		}
	}
}

func TestRenderOfAnOSWithoutProc(t *testing.T) {
	got := Render(Report{Disks: []DiskSpace{{Path: "/"}}})
	if !strings.Contains(got, "Machine memory and load: not known here") || !strings.Contains(got, "/: not known") {
		t.Fatalf("render = %q", got)
	}
}

func TestStatsMergesTheTempPrefixes(t *testing.T) {
	dir := t.TempDir()
	ps := []Place{{Name: PlaceTemp, Path: dir, Prefix: "a-"}, {Name: PlaceTemp, Path: dir, Prefix: "b-"}}
	s := NewStats(ps, dir, func(s *Stats) {
		s.machine = func() Machine { return Machine{} }
		s.memory = func() (int64, int64, bool) { return 10, 20, true }
		s.disk = func(p string) DiskSpace { return DiskSpace{Path: p} }
	})
	r := s.Read(t.Context())
	if len(r.Places) != 1 || r.RSS != 10 || r.Peak != 20 || len(r.Disks) != 2 {
		t.Fatalf("report %+v", r)
	}
}
