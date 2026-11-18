package mcpadmin

import (
	"context"
	"errors"
	"fmt"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/intake"
	"github.com/igorrius/ganglion/mcpclient"
)

// KeyRequest asks the admin for a server's key (design note, section 6).
type KeyRequest struct {
	Server, Header string
	// Host is the only host the key goes to, which the card names.
	Host string
	// Principal, Transport and Conv are the admin and their private chat.
	Principal, Transport, Conv string
	// Key is the asking tool call's key: a rerun of the call sends no
	// second card (tool.Call.Key).
	Key string
	// Tainted says the session that asked holds outside content.
	Tainted bool
	// Done is called once the key is stored; it lists the tools and sends
	// the tools card.
	Done func(ctx context.Context)
}

// Keys are the keys of servers with auth: header: the request that asks
// the admin for one, past the model, and the credential that sends it.
type Keys interface {
	Request(ctx context.Context, r KeyRequest) error
	Auth(server, header string) mcpclient.Auth
}

// SecretAsker opens a secret request with its card (intake.Asker).
type SecretAsker interface {
	Ask(ctx context.Context, k intake.SecretAsk, done intake.SecretDoneFunc) (intake.Asked, error)
}

// StoreKeys asks for a server's key through the secret requests and reads
// it from the store. The key lives in the server's scope, mcp:<server>,
// under the header's name, so a Disconnect, which removes the scope,
// removes it too (R-MCP-5). The agent never sees it: the admin types it on
// the intake page and the node stores it (R-INT-2).
type StoreKeys struct {
	Store mcpclient.Store
	Asker SecretAsker
}

// Request opens the secret request for the key; its card names the server,
// the header and the one host the key goes to. Done runs once the value is
// stored, never when the request fails or expires.
func (k StoreKeys) Request(ctx context.Context, r KeyRequest) error {
	_, err := k.Asker.Ask(ctx, intake.SecretAsk{Scope: Scope(r.Server), Name: r.Header, Asking: intake.AskingServer,
		Who: r.Server, Hosts: []string{r.Host}, Admin: r.Principal, Transport: r.Transport, Conv: r.Conv,
		Key: r.Key, Tainted: r.Tainted},
		func(ctx context.Context, c event.SecretClosed) {
			if c.Outcome == event.SecretSet {
				r.Done(ctx)
			}
		})
	return err
}

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
	v, err := k.store.Get(ctx, k.server, k.header)
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

// keyWords says, for the model, why the key's request was not opened.
func keyWords(err error) string {
	switch {
	case errors.Is(err, intake.ErrPageChanged), errors.Is(err, intake.ErrPageUnchecked):
		return "the intake page failed the node's check, so handing secrets over is stopped; the owner was told"
	case errors.Is(err, intake.ErrTooMany):
		return "too many requests are open; try again later"
	}
	return "the request could not be sent to the owner"
}
