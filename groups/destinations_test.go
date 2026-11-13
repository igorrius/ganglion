package groups_test

import (
	"errors"
	"testing"

	"github.com/igorrius/ganglion/groups"
	"github.com/igorrius/ganglion/settings"
)

// A name resolves to its conversation and the transport the key names,
// from the list as it is at the call.
func TestDestinationsResolve(t *testing.T) {
	list := []settings.Destination{{Name: "family-k7", Conv: "telegram:-1001/45"}}
	var fail error
	d := groups.Destinations{List: func() ([]settings.Destination, error) { return list, fail }}
	transport, conv, ok, err := d.Resolve("family-k7")
	if err != nil || !ok || transport != "telegram" || conv != "telegram:-1001/45" {
		t.Fatalf("Resolve = %q %q %v %v", transport, conv, ok, err)
	}
	if _, _, ok, err := d.Resolve("nowhere"); ok || err != nil {
		t.Fatalf("an unknown name: %v %v", ok, err)
	}
	list[0].Conv = "telegram:-1001/8"
	if _, conv, _, _ := d.Resolve("family-k7"); conv != "telegram:-1001/8" {
		t.Fatalf("a name pointed elsewhere resolves to %q", conv)
	}
	fail = errors.New("the file is broken")
	if _, _, ok, err := d.Resolve("family-k7"); ok || err == nil {
		t.Fatalf("a broken file: %v %v; an error must not read as an answer", ok, err)
	}
}
