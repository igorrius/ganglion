package modstore

import (
	"slices"
	"testing"
)

func TestLoopOf(t *testing.T) {
	cases := map[string]struct {
		module  string
		listens []string
		others  map[string][]string
		want    []string
	}{
		"no edges":       {"a", []string{"p.soc"}, nil, nil},
		"nobody listens": {"a", nil, map[string][]string{"b": {"c.x"}}, nil},
		"two modules":    {"a", []string{"b.y"}, map[string][]string{"b": {"a.x"}}, []string{"a", "b", "a"}},
		"three modules":  {"a", []string{"c.z"}, map[string][]string{"b": {"a.x"}, "c": {"b.y"}}, []string{"a", "b", "c", "a"}},
		"chain is fine":  {"a", []string{"b.y"}, map[string][]string{"c": {"a.x"}}, nil},
		"loop elsewhere": {"d", []string{"p.q"}, map[string][]string{"a": {"b.x"}, "b": {"a.y"}}, nil},
		// the module's own old version is not among the others, so it does not
		// close a loop with itself.
		"own old version": {"a", []string{"b.y"}, map[string][]string{"b": {"p.z"}}, nil},
		"prefix is whole": {"a", []string{"ab.y"}, map[string][]string{"ab": {"p.z"}, "b": {"a.x"}}, nil},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := loopOf(tc.module, tc.listens, tc.others); !slices.Equal(got, tc.want) {
				t.Fatalf("loopOf = %v, want %v", got, tc.want)
			}
		})
	}
}
