package mcpclient

import "context"

// Auth gives the header a request to the server carries: the OAuth token
// (R-MCP-6) or a key. An empty name means no header. The client sends the
// header only to the server's origin and drops it on a redirect to another
// (R-MCP-5). An error an Auth returns must not carry the credential: it
// never reaches a message, but a caller that unwraps it may log it.
type Auth interface {
	Header(ctx context.Context) (name, value string, err error)
	// Rejected tells the Auth that the server answered 401 to a request
	// that carried its header: the credential no longer works. An OAuth
	// Auth refreshes the token once before the next request; a key has
	// nothing to do.
	Rejected(ctx context.Context)
}

// NoAuth sends no header.
type NoAuth struct{}

// Header implements Auth: there is none.
func (NoAuth) Header(context.Context) (name, value string, err error) { return "", "", nil }

// Rejected implements Auth: there is nothing to renew.
func (NoAuth) Rejected(context.Context) {}
