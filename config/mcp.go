package config

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/mcpclient"
	"github.com/igorrius/ganglion/web"
	"github.com/samber/do/v2"
)

// mcpDialTimeout is how long the client of a remote MCP server may take to
// connect to an address.
const mcpDialTimeout = 10 * time.Second

// MCPHub provides the client of the remote MCP servers (R-MCP-1). Its
// requests dial through web.PublicDial, so a server's address can never be a
// private one at run time, whatever the name resolves to later (R-WEB-2).
// No server is set yet: the `mcp:` section of the settings, and the tools
// that change it, are a later change (#260); until then the hub holds none
// and its tools are not offered.
func MCPHub() do.Provider[*mcpclient.Hub] {
	return func(i do.Injector) (*mcpclient.Hub, error) {
		hc := &http.Client{Transport: &http.Transport{
			Proxy: nil, DialContext: web.PublicDial(mcpDialTimeout), TLSHandshakeTimeout: mcpDialTimeout,
			MaxResponseHeaderBytes: 64 << 10, // the server's own timeout bounds a request
		}}
		return mcpclient.NewHub(
			mcpclient.WithHubLogger(do.MustInvoke[*slog.Logger](i)),
			mcpclient.WithHubClock(time.Now),
			mcpclient.WithHubHTTPClient(hc),
			mcpclient.WithHubVersion(buildinfo.Read().Version),
		), nil
	}
}
