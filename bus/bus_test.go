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
