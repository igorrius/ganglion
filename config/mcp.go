package config

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/intake"
	"github.com/igorrius/ganglion/internal/buildinfo"
	"github.com/igorrius/ganglion/language"
	"github.com/igorrius/ganglion/mcpadmin"
	"github.com/igorrius/ganglion/mcpclient"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/pending"
	"github.com/igorrius/ganglion/web"
	"github.com/samber/do/v2"
)

// mcpDialTimeout is how long the client of a remote MCP server may take to
// connect to an address.
const mcpDialTimeout = 10 * time.Second

// MCPHTTPClient is the HTTP client of the remote MCP servers and their
// sign-ins: its own type, so the container holds it apart from any other
// client.
type MCPHTTPClient struct{ *http.Client }

// MCPHTTP provides the client of the remote MCP servers and their
// sign-ins: it dials through web.PublicDial, so an address can never be a
// private one at run time, whatever the name resolves to later (R-WEB-2).
func MCPHTTP() do.Provider[MCPHTTPClient] {
	return func(do.Injector) (MCPHTTPClient, error) {
		return MCPHTTPClient{&http.Client{Transport: &http.Transport{
			Proxy: nil, DialContext: web.PublicDial(mcpDialTimeout), TLSHandshakeTimeout: mcpDialTimeout,
			MaxResponseHeaderBytes: 64 << 10, // the server's own timeout bounds a request
		}}}, nil
	}
}

// mcpHTTP is the client of the remote MCP servers.
func mcpHTTP(i do.Injector) *http.Client { return do.MustInvoke[MCPHTTPClient](i).Client }

// intakePageOf is the intake page the node uses: mcp.page of its file
// (R-INT-3); a bootstrap made without a file (a test node) has the
// default.
func intakePageOf(b Bootstrap) string {
	if p := b.Settings.MCP.Page; p != "" {
		return p
	}
	return intake.DefaultPage
}

// MCPHub provides the client of the remote MCP servers (R-MCP-1). The
// servers come from the mcp: section through MCPAdmin, which builds it.
func MCPHub() do.Provider[*mcpclient.Hub] {
	return func(i do.Injector) (*mcpclient.Hub, error) {
		return mcpclient.NewHub(
			mcpclient.WithHubLogger(do.MustInvoke[*slog.Logger](i)),
			mcpclient.WithHubClock(time.Now),
			mcpclient.WithHubHTTPClient(mcpHTTP(i)),
			mcpclient.WithHubVersion(buildinfo.Read().Version),
		), nil
	}
}

// MCPSignIns provides the OAuth sign-in of the servers (R-MCP-3, R-MCP-5):
// registrations and tokens in the secret store's scope mcp:<server>, the
// intake page as the only redirect address.
func MCPSignIns() do.Provider[*mcpclient.OAuth] {
	return func(i do.Injector) (*mcpclient.OAuth, error) {
		return mcpclient.NewOAuth(mcpHTTP(i), mcpadmin.VaultStore{V: do.MustInvoke[*vault.Vault](i)},
			intakePageOf(do.MustInvoke[Bootstrap](i)),
			mcpclient.WithOAuthLogger(do.MustInvoke[*slog.Logger](i)),
			mcpclient.WithOAuthVersion(buildinfo.Read().Version)), nil
	}
}

// MCPAdmin provides the admin's side of the servers (R-MCP-2, R-MCP-3,
// R-MCP-9, R-MCP-10) and applies the mcp: section to the hub, so the
// servers run from the start.
func MCPAdmin() do.Provider[*mcpadmin.Admin] {
	return func(i do.Injector) (*mcpadmin.Admin, error) {
		store := do.MustInvoke[*vault.Vault](i)
		a := mcpadmin.New(mcpadmin.Deps{
			File:     mcpFile(i),
			Hub:      do.MustInvoke[*mcpclient.Hub](i),
			SignIns:  do.MustInvoke[*mcpclient.OAuth](i),
			Keys:     mcpadmin.StoreKeys{Store: mcpadmin.VaultStore{V: store}, Asker: do.MustInvoke[*intake.Asker](i)},
			Requests: do.MustInvoke[*intake.Registry](i),
			Buttons:  do.MustInvoke[*pending.Store](i),
			Enqueue:  do.MustInvoke[*sqlqueue.Queue](i).Enqueue,
			Lang:     do.MustInvoke[*language.Store](i).Resolve,
			Texts:    do.MustInvoke[*texts.Catalog](i),
			IsAdmin:  do.MustInvoke[*access.List](i).IsAdmin,
			Reserved: reservedNames(i),
			Locked:   store.Locked,
			HTTP:     mcpHTTP(i),
		}, mcpadmin.WithLogger(do.MustInvoke[*slog.Logger](i)), mcpadmin.WithVersion(buildinfo.Read().Version))
		return a, a.Apply()
	}
}

// reservedNames lists, at the call, the areas of the registry's tools and
// the short names of the installed modules: a server's name takes none.
// Both are read when a connect asks: the registry is built by then. They
// are read from the root scope: the scope a provider gets carries the
// invoker chain of its build, which a later lookup of the registry would
// read as a circular dependency.
func reservedNames(i do.Injector) mcpadmin.ReservedFunc {
	root := i.RootScope()
	return func() []string {
		var out []string
		for _, s := range do.MustInvoke[*tool.Registry](root).Specs(true) {
			area, _, _ := strings.Cut(s.Name, ".")
			out = append(out, area)
		}
		if mods, err := modulesFile(root).Modules(); err == nil {
			for _, m := range mods {
				out = append(out, modstore.ShortName(m.Name))
			}
		}
		return out
	}
}
