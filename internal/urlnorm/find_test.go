package urlnorm

import (
	"slices"
	"testing"
)

func TestFind(t *testing.T) {
	got := Find(`see https://x/a. Also (http://y/b), "HTTPS://z/c"! and xhttps://w/d 1http://v/e`)
	want := []string{"https://x/a", "http://y/b", "HTTPS://z/c", "https://w/d", "http://v/e"}
	if !slices.Equal(got, want) {
		t.Fatalf("Find = %q", got)
	}
}

func TestDefang(t *testing.T) {
	for _, in := range []string{"xhttps://evil/a", "_http://evil/b", "hthttps://tps://evil/c", "HTTPS://evil", "a https://b c http://d"} {
		if out := Defang(in); len(Find(out)) != 0 {
			t.Errorf("Defang(%q) = %q still holds an address", in, out)
		}
	}
	if Defang("no address here") != "no address here" {
		t.Error("plain text changed")
	}
}

func TestExact(t *testing.T) {
	for in, want := range map[string]bool{
		"https://a.example/x":                true,
		"https://a.example/?q=a https://e/x": false,
		`https://b.example/?q=">https://e/y`: false,
		"https://a.example/(x)":              false,
		"https://a.example/x.":               false,
	} {
		if Exact(in) != want {
			t.Errorf("Exact(%q) = %v", in, !want)
		}
	}
}
