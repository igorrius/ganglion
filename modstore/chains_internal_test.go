package modstore

import (
	"slices"
	"testing"
)

func TestLoopFromStartsAtTheModule(t *testing.T) {
	cases := []struct {
		module string
		loop   []string
		want   []string
	}{
		{"b", []string{"a", "b", "a"}, []string{"b", "a", "b"}},
		{"c", []string{"a", "b", "c", "a"}, []string{"c", "a", "b", "c"}},
		{"a", []string{"a", "b", "a"}, []string{"a", "b", "a"}},
		{"x", []string{"a", "b", "a"}, []string{"a", "b", "a"}},
		{"a", []string{"a", "a"}, []string{"a", "a"}},
	}
	for _, c := range cases {
		if got := loopFrom(c.module, c.loop); !slices.Equal(got, c.want) {
			t.Errorf("loopFrom(%q, %v) = %v, want %v", c.module, c.loop, got, c.want)
		}
	}
}
