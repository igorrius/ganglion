package node

import (
	"slices"
	"testing"
)

func TestParseRoles(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []Role
	}{
		{"all three", "core,edge,worker", []Role{Core, Edge, Worker}},
		{"single", "edge", []Role{Edge}},
		{"canonical order", "worker,core", []Role{Core, Worker}},
		{"spaces and duplicates", " core , edge,core ", []Role{Core, Edge}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseRoles(tt.in)
			if err != nil {
				t.Fatalf("ParseRoles(%q) error: %v", tt.in, err)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("ParseRoles(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseRolesRejects(t *testing.T) {
	for _, in := range []string{"", " , ", "core,brain", "Core"} {
		if got, err := ParseRoles(in); err == nil {
			t.Errorf("ParseRoles(%q) = %v, want error", in, got)
		}
	}
}
