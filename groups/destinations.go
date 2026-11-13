package groups

import (
	"fmt"
	"strings"

	"github.com/igorrius/ganglion/settings"
)

// DestinationsFunc returns the destinations of the node's file
// (settings.DestinationsFile.Destinations).
type DestinationsFunc func() ([]settings.Destination, error)

// Destinations resolves a destination's name. It asks List on every call,
// so a name pointed elsewhere a moment ago resolves to the new place
// (R-CFG-12).
type Destinations struct {
	List DestinationsFunc
}

// Resolve returns the transport and the conversation of the destination
// called name; ok is false when there is none. An error reading the file
// is returned: it is never read as "no such destination".
func (d Destinations) Resolve(name string) (transport, conv string, ok bool, err error) {
	ds, err := d.List()
	if err != nil {
		return "", "", false, fmt.Errorf("groups: destinations: %w", err)
	}
	for _, dest := range ds {
		if dest.Name == name {
			transport, _, _ = strings.Cut(dest.Conv, ":")
			return transport, dest.Conv, true, nil
		}
	}
	return "", "", false, nil
}
