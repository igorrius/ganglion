package mcpadmin

import (
	"context"
	"errors"
	"fmt"

	"github.com/igorrius/ganglion/mcpclient"
)

// nameKey is the name a server's key has in its scope, mcp:<server>
// (R-MCP-5).
const nameKey = "key"

// ErrNoKeys refuses a server that takes a key in a header while the node
// cannot yet ask the admin for one: the secret request it rides on
// (secret.request, #172) is not wired here yet.
var ErrNoKeys = errors.New("mcpadmin: a server that takes a key in a header cannot be connected from the chat yet")

// KeyRequest asks the admin for a server's key (design note, section 6).
type KeyRequest struct {
	Server, Header string
	// Host is the only host the key goes to, which the card names.
	Host string
	// Principal, Transport and Conv are the admin and their private chat.
	Principal, Transport, Conv string
	// Done is called once the key is stored; it lists the tools and sends
	// the tools card.
	Done func(ctx context.Context) error
}

// Keys are the keys of servers with auth: header: the request that asks
// the admin for one, past the model, and the credential that sends it.
type Keys interface {
	Request(ctx context.Context, r KeyRequest) error
	Auth(server, header string) mcpclient.Auth
}

// StoreKeys reads a server's key from the store, scope mcp:<server>. Its
// Request is ErrNoKeys until the secret request is wired.
type StoreKeys struct {
	Store mcpclient.Store
}

// Request refuses: see ErrNoKeys.
func (StoreKeys) Request(context.Context, KeyRequest) error { return ErrNoKeys }

// Auth is the key of server in header.
func (k StoreKeys) Auth(server, header string) mcpclient.Auth {
	return keyAuth{store: k.Store, server: server, header: header}
}

// keyAuth sends a stored key in its header.
type keyAuth struct {
	store          mcpclient.Store
	server, header string
}

// errNoKey is a server whose key is not stored: it needs one entered
// again. It is mcpclient.ErrUnauthorized, so the hub shows it so.
var errNoKey = fmt.Errorf("%w: no key stored", mcpclient.ErrUnauthorized)

// Header implements mcpclient.Auth: the stored key, read each time, so a
// key entered again is used at once.
func (k keyAuth) Header(ctx context.Context) (name, value string, err error) {
	v, err := k.store.Get(ctx, k.server, nameKey)
	if err != nil {
		return "", "", fmt.Errorf("mcpadmin: read the key of %s: %w", k.server, err)
	}
	if v == "" {
		return "", "", errNoKey
	}
	return k.header, v, nil
}

// Rejected implements mcpclient.Auth: a key has nothing to renew.
func (keyAuth) Rejected(context.Context, string) {}
