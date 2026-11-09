package host

import "testing"

func TestParseMeminfo(t *testing.T) {
	in := []byte("MemTotal:       16303472 kB\nMemFree:  1000 kB\nMemAvailable:    9512340 kB\n")
	total, avail, ok := parseMeminfo(in)
	if !ok || total != 16303472*1024 || avail != 9512340*1024 {
		t.Fatalf("got %d %d %v", total, avail, ok)
	}
	if _, _, ok := parseMeminfo([]byte("MemTotal: x kB\n")); ok {
		t.Fatal("a broken file parsed")
	}
}

func TestParseLoadavg(t *testing.T) {
	load, ok := parseLoadavg([]byte("0.52 0.61 0.70 2/1234 98765\n"))
	if !ok || load != [3]float64{0.52, 0.61, 0.70} {
		t.Fatalf("got %v %v", load, ok)
	}
}

func TestParseUptime(t *testing.T) {
	up, ok := parseUptime([]byte("273600.25 1000000.00\n"))
	if !ok || up.Hours() < 75.9 || up.Hours() > 76.1 {
		t.Fatalf("got %v %v", up, ok)
	}
}
