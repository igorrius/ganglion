package bus

import (
	"errors"
	"testing"
)

func TestSubjects(t *testing.T) {
	for name, tc := range map[string]struct {
		got  func() (string, error)
		want string
	}{
		"notify":    {func() (string, error) { return Notify("reader") }, "g.notify.reader"},
		"event":     {func() (string, error) { return Event("n1", "reader", "reading") }, "g.ev.n1.reader.reading"},
		"call":      {func() (string, error) { return Call("n1", "reader", "read") }, "g.rpc.n1.reader.read"},
		"telemetry": {func() (string, error) { return Telemetry("n1") }, "g.tel.n1"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := tc.got()
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}

func TestDeliveryKeys(t *testing.T) {
	for name, tc := range map[string]struct {
		got  func() (string, error)
		want string
	}{
		"desired":   {func() (string, error) { return Desired("home", "probe") }, "desired.home.probe"},
		"actual":    {func() (string, error) { return Actual("home", "probe") }, "actual.home.probe"},
		"of edge":   {func() (string, error) { return DesiredOf("home") }, "desired.home.*"},
		"of module": {func() (string, error) { return DesiredOfModule("probe") }, "desired.*.probe"},
		"object":    {func() (string, error) { return ModuleObject("probe", "ab12", PartWasm) }, "probe.ab12.wasm"},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := tc.got()
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	for _, bad := range []func() (string, error){
		func() (string, error) { return Desired("core", "probe") },
		func() (string, error) { return Desired("home", "a.b") },
		func() (string, error) { return Actual("h>", "probe") },
		func() (string, error) { return DesiredOf("*") },
	} {
		if k, err := bad(); !errors.Is(err, ErrBadToken) {
			t.Errorf("a bad key %q was made: %v", k, err)
		}
	}
	if e, m, ok := ParseDesired("desired.home.probe"); !ok || e != "home" || m != "probe" {
		t.Errorf("ParseDesired = %q %q %v", e, m, ok)
	}
	for _, k := range []string{"actual.home.probe", "desired.home", "desired.core.probe", "desired.home.probe.x"} {
		if _, _, ok := ParseDesired(k); ok {
			t.Errorf("ParseDesired(%q) took it", k)
		}
	}
}

func TestBadTokensAreRefused(t *testing.T) {
	for _, tok := range []string{"", "a.b", "a*", ">", "a b", "a\tb"} {
		if _, err := Notify(tok); !errors.Is(err, ErrBadToken) {
			t.Errorf("Notify(%q) error = %v, want ErrBadToken", tok, err)
		}
	}
	if _, err := Event("n", "m", "p.q"); !errors.Is(err, ErrBadToken) {
		t.Errorf("a dotted port was accepted: %v", err)
	}
}
