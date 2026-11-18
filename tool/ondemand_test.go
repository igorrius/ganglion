package tool_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/igorrius/ganglion/tool"
)

func onDemand(name string, admin bool) stub {
	return stub{spec: tool.Spec{Name: name, Class: tool.ClassExternal, Policy: tool.PolicyAuto, AdminOnly: admin, OnDemand: true}}
}

// An on-demand tool is left out of Specs and Offered, listed by SessionSpecs
// only for a session that turned it on, and refused by Run elsewhere.
func TestOnDemandTools(t *testing.T) {
	r, err := tool.NewRegistryWith(local{}, []tool.RegistryOption{
		tool.WithDynamic(func() []tool.Handler {
			return []tool.Handler{onDemand("shop.search", true), onDemand("shop.buy", true)}
		}),
	}, named("cells.get", false))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		admin bool
		on    []string
		want  []string
	}{
		{"none on", true, nil, []string{"cells.get"}},
		{"one on", true, []string{"shop.buy"}, []string{"cells.get", "shop.buy"}},
		{"both on", true, []string{"shop.search", "shop.buy", "ghost.tool"}, []string{"cells.get", "shop.buy", "shop.search"}},
		{"on but not an admin", false, []string{"shop.buy"}, []string{"cells.get"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := specNames(r.SessionSpecs(tt.admin, tt.on))
			slices.Sort(got)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("SessionSpecs = %v, want %v", got, tt.want)
			}
		})
	}
	if got := specNames(r.Specs(true)); !slices.Equal(got, []string{"cells.get"}) {
		t.Fatalf("Specs = %v: an on-demand tool must not be listed", got)
	}
	if r.Offered("shop.buy", true) {
		t.Fatal("an on-demand tool is on offer")
	}

	_, err = r.Run(context.Background(), tool.Call{Name: "shop.buy", Admin: true, On: []string{"shop.search"}})
	if !errors.Is(err, tool.ErrUnknown) {
		t.Fatalf("a call of a tool that is not on = %v, want ErrUnknown", err)
	}
	res, err := r.Run(context.Background(), tool.Call{Name: "shop.buy", Admin: true, On: []string{"shop.buy"}})
	if err != nil || res.Text != "ran shop.buy" {
		t.Fatalf("a call of a tool that is on = %q, %v", res.Text, err)
	}
	if _, err := r.Run(context.Background(), tool.Call{Name: "shop.buy", Admin: false, On: []string{"shop.buy"}}); !errors.Is(err, tool.ErrForbidden) {
		t.Fatalf("a non-admin's call = %v, want ErrForbidden", err)
	}
}

// Dynamics lists every source's tools, in the order of the sources.
func TestDynamics(t *testing.T) {
	a := func() []tool.Handler { return []tool.Handler{named("a.one", true), named("a.two", true)} }
	b := func() []tool.Handler { return []tool.Handler{named("b.one", true)} }
	none := func() []tool.Handler { return nil }
	tests := []struct {
		name string
		fs   []tool.DynamicFunc
		want []string
	}{
		{"no sources", nil, nil},
		{"one", []tool.DynamicFunc{a}, []string{"a.one", "a.two"}},
		{"several in order", []tool.DynamicFunc{b, none, a}, []string{"b.one", "a.one", "a.two"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, h := range tool.Dynamics(tt.fs...)() {
				got = append(got, h.Spec().Name)
			}
			if !slices.Equal(got, tt.want) {
				t.Fatalf("Dynamics = %v, want %v", got, tt.want)
			}
		})
	}
}
