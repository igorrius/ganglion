package natsbus

import (
	"errors"
	"fmt"

	"github.com/nats-io/nats-server/v2/server"
)

// RedialLeaf closes the edge's leaf link so that the server dials the core
// again and reads the .creds file anew: the way a user JWT with other
// permissions takes effect without a restart (ADR-0116). It returns at once;
// the link is up again a moment later. A link that is already down is not an
// error: the next dial reads the file anyway.
func (b *Bus) RedialLeaf() error {
	if !b.IsEdge() || b.srv == nil {
		return errors.New("natsbus: only an edge has a leaf link to redial")
	}
	lz, err := b.srv.Leafz(&server.LeafzOptions{})
	if err != nil {
		return fmt.Errorf("natsbus: leaf link: %w", err)
	}
	for _, l := range lz.Leafs {
		if err := b.srv.DisconnectClientByID(l.ID); err != nil {
			return fmt.Errorf("natsbus: close the leaf link: %w", err)
		}
	}
	return nil
}
