package mcpclient

import "context"

// Auth gives the header a request to the server carries: the OAuth token
// (R-MCP-6) or a key. An empty name means no header. The client sends the
// header only to the server's origin and drops it on a redirect to another
// (R-MCP-5). An error an Auth returns must not carry the credential: it
// never reaches a message, but a caller that unwraps it may log it.
type Auth interface {
	Header(ctx context.Context) (name, value string, err error)
}

// NoAuth sends no header.
type NoAuth struct{}

// Header implements Auth: there is none.
func (NoAuth) Header(context.Context) (name, value string, err error) { return "", "", nil }
