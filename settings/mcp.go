package settings

import (
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The mcp: section (R-MCP-2, design note section 5): the remote MCP
// servers the admin connected and the intake page's address. It applies at
// once (R-CFG-6) and is changed only by its own tools, mcp.connect,
// mcp.rule and /mcp, never by config.propose (R-CFG-14).

// DefaultIntakePage is the intake page on the project's site, the default
// of mcp.page (R-INT-3); intake.DefaultPage is the same address.
const DefaultIntakePage = "https://igorrius.github.io/ganglion/intake/v1/"

// DefaultMCPTimeout is how long one request to a server may take when its
// line names no timeout.
const DefaultMCPTimeout = 30 * time.Second

// MCPAuth is how a server is signed in to. Stored, so the values never
// change.
type MCPAuth string

// The ways of R-MCP-2.
const (
	MCPAuthNone   MCPAuth = "none"
	MCPAuthOAuth  MCPAuth = "oauth"
	MCPAuthHeader MCPAuth = "header"
)

// MCPPolicy is a tool's policy as the file holds it: tool.Policy's values.
type MCPPolicy string

// The policies of R-MCP-2.
const (
	MCPPolicyAuto      MCPPolicy = "auto"
	MCPPolicyConfirm   MCPPolicy = "confirm"
	MCPPolicyForbidden MCPPolicy = "forbidden"
)

// MCP is the remote MCP servers and the intake page.
type MCP struct {
	// Page is the address of the intake page, which the node checks
	// against its own copy before every request (R-INT-3).
	Page string `yaml:"page"`
	// Servers are the connected servers by name.
	Servers map[string]MCPServer `yaml:"servers"`
}

// MCPServer is one connected server as the admin approved it.
type MCPServer struct {
	// URL is the server's Streamable HTTP address, https.
	URL string `yaml:"url"`
	// Auth is none, oauth or header.
	Auth MCPAuth `yaml:"auth"`
	// Header names the header that carries the key, for auth: header; the
	// key itself is in the secret store, scope mcp:<name> (R-MCP-5). It is
	// read only for auth: header.
	Header string `yaml:"header"`
	// Timeout is how long one request may take; DefaultMCPTimeout when
	// the line names none.
	Timeout Duration `yaml:"timeout"`
	// Tools is the policy of each approved tool, by its name without the
	// server's prefix (mcpclient.ToolName).
	Tools map[string]MCPPolicy `yaml:"tools"`
	// Fingerprint pins the approved tool list (R-MCP-7).
	Fingerprint string `yaml:"fingerprint"`
}

// reasonAboveZero refuses a length of time or a count of 0.
const reasonAboveZero = "must be above 0"

// Patterns of the section's names.
var (
	// MCPNameRE is a server's name (R-MCP-2).
	MCPNameRE   = regexp.MustCompile(`^[a-z0-9_]{1,24}$`)
	mcpToolRE   = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	headerRE    = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]{1,64}$")
	fingerRE    = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	headersDeny = []string{"authorization", "content-type", "content-length", "host", "cookie", "accept",
		"mcp-session-id", "mcp-protocol-version", "transfer-encoding", "connection"}
)

// ValidMCPHeader reports whether name may carry a server's key: a header
// token that is not one the client sets itself.
func ValidMCPHeader(name string) bool {
	return headerRE.MatchString(name) && !slices.Contains(headersDeny, strings.ToLower(name))
}

// ValidMCPURL reports whether raw may be a server's address: absolute
// https with a host and no user info. Whether the host is public is the
// dialer's check (web.PublicDial).
func ValidMCPURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == schemeHTTPS && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}

// fill gives each server its default timeout.
func (m *MCP) fill() {
	for name, s := range m.Servers {
		if s.Timeout == 0 {
			s.Timeout = Duration(DefaultMCPTimeout)
			m.Servers[name] = s
		}
	}
}

// check refuses a section the node cannot run. An error names the key and
// never repeats a value.
func (m MCP) check(mods []Module) error {
	if !ValidMCPURL(m.Page) {
		return pathError("mcp.page", "must be an https address with a host")
	}
	for _, name := range slices.Sorted(maps.Keys(m.Servers)) {
		if err := m.Servers[name].check("mcp.servers."+name, name, mods); err != nil {
			return err
		}
	}
	return nil
}

// check refuses a bad server line at path.
func (s MCPServer) check(path, name string, mods []Module) error {
	for _, c := range []struct {
		key, reason string
		ok          bool
	}{
		{"", "the name must be 1 to 24 of a-z, 0-9 and _", MCPNameRE.MatchString(name)},
		{"", "the name is a module's", !moduleNamed(mods, name)},
		{"url", "must be an https address with a host and no user info", ValidMCPURL(s.URL)},
		{"auth", "must be none, oauth or header", slices.Contains([]MCPAuth{MCPAuthNone, MCPAuthOAuth, MCPAuthHeader}, s.Auth)},
		{"header", "must name a header for auth: header", s.Auth != MCPAuthHeader || s.Header != ""},
		{"header", "must be a header name the client does not set itself", s.Header == "" || ValidMCPHeader(s.Header)},
		{"timeout", reasonAboveZero, s.Timeout > 0},
		{"fingerprint", "must be sha256: and 64 hex digits", fingerRE.MatchString(s.Fingerprint)},
	} {
		if !c.ok && c.key == "" {
			return pathError(path, c.reason)
		}
		if !c.ok {
			return pathError(path+"."+c.key, c.reason)
		}
	}
	for _, tool := range slices.Sorted(maps.Keys(s.Tools)) {
		p := s.Tools[tool]
		switch {
		case !mcpToolRE.MatchString(tool):
			return pathError(path+".tools", "a tool's name must be a-z, 0-9 and _")
		case !slices.Contains([]MCPPolicy{MCPPolicyAuto, MCPPolicyConfirm, MCPPolicyForbidden}, p):
			return pathError(path+".tools."+tool, "must be auto, confirm or forbidden")
		}
	}
	return nil
}

// moduleNamed reports whether a module's short name is name: a server's
// tools would share the module's prefix.
func moduleNamed(mods []Module, name string) bool {
	for _, m := range mods {
		if strings.TrimPrefix(m.Name, "local/") == name {
			return true
		}
	}
	return false
}
