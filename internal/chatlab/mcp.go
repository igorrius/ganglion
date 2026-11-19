//go:build lab

package chatlab

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/igorrius/ganglion/mcpclient/mcpclienttest"
)

// authorizeLink finds the sign-in link of an OAuth card in a sent message's
// text or entities.
var authorizeLink = regexp.MustCompile(`https://[^\s"<>)]+/authorize\?[^\s"<>)]+`)

// mcpLab is a scenario's fake MCP server and, for OAuth, its fake
// authorization server.
type mcpLab struct {
	srv *mcpclienttest.Server
	as  *mcpclienttest.AuthServer // nil without OAuth
}

// serveMCP starts the scenario's fakes and returns the HTTP client the node
// reaches them with: MCPURL's host leads to the fake MCP server, any other
// address is dialled as it is (the authorization server is on loopback),
// and httptest's certificate is trusted. Nil and nil for no server.
func serveMCP(owner *program, s *MCPServer) (*mcpLab, *http.Client, error) {
	if s == nil {
		return nil, nil, nil
	}
	raws := make([]json.RawMessage, 0, len(s.Tools))
	for _, t := range s.Tools {
		raws = append(raws, mcpclienttest.Tool(t.Name, t.Description, "", t.ReadOnly))
	}
	l := &mcpLab{srv: mcpclienttest.NewTLS(owner, mcpclienttest.Options{Tools: raws, ListenStream: true})}
	u, err := url.Parse(MCPURL)
	if err != nil {
		return nil, nil, fmt.Errorf("chatlab: %w", err)
	}
	if s.OAuth {
		l.as = mcpclienttest.NewAuthServer(owner, mcpclienttest.AuthOptions{IssParameter: true})
		const path = "/.well-known/oauth-protected-resource/mcp"
		doc, err := json.Marshal(map[string]any{"resource": MCPURL, "authorization_servers": []string{l.as.URL}})
		if err != nil {
			return nil, nil, fmt.Errorf("chatlab: %w", err)
		}
		l.srv.Update(func(o *mcpclienttest.Options) {
			o.Bearer = l.as.Valid
			o.Challenge = `Bearer resource_metadata="https://` + u.Host + path + `"`
			o.Documents = map[string]string{path: string(doc)}
		})
	}
	base, ok := l.srv.Client().Transport.(*http.Transport)
	if !ok {
		return nil, nil, errors.New("chatlab: the fake MCP server's client has no *http.Transport")
	}
	tr := base.Clone()
	target, local := net.JoinHostPort(u.Hostname(), "443"), l.srv.Listener.Addr().String()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if addr == target {
			addr = local
		}
		var d net.Dialer
		return d.DialContext(ctx, network, addr)
	}
	return l, &http.Client{Transport: tr}, nil
}

// SignIn is the owner signing in from the newest sign-in card: the fake
// authorization server takes its link and gives the code, and the owner
// pastes the gauth1 line the intake page shows.
func (n *Node) SignIn(ctx context.Context) error {
	if n.mcp == nil || n.mcp.as == nil {
		return errors.New("chatlab: sign_in needs mcp with oauth")
	}
	link := ""
	for _, m := range n.peer.fake.Sent() {
		if found := authorizeLink.FindString(strings.ReplaceAll(m.Text+m.Entities, "\\u0026", "&")); found != "" {
			link = found
		}
	}
	if link == "" {
		return errors.New("chatlab: no sign-in card is showing")
	}
	signed, err := n.mcp.as.SignIn(link)
	if err != nil {
		return fmt.Errorf("chatlab: sign in: %w", err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	return n.peer.Say(ctx, "gauth1."+signed.State+"."+b64([]byte(signed.Code))+"."+b64([]byte(signed.Issuer)))
}

// deleted counts the messages the bot deleted in the lab's chat.
func (n *Node) deleted() int {
	c := 0
	for _, d := range n.peer.fake.Deleted() {
		if d.ChatID == labChat {
			c++
		}
	}
	return c
}

// modelSaw is everything the scripted model was sent; "" for a real one.
func (n *Node) modelSaw() string {
	if n.scripted == nil {
		return ""
	}
	return fmt.Sprintf("%+v", n.scripted.Requests())
}
