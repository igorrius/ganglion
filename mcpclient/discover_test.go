package mcpclient

import (
	"maps"
	"net/url"
	"slices"
	"testing"
)

func TestBearerParams(t *testing.T) {
	tests := []struct {
		name   string
		values []string
		want   map[string]string
	}{
		{"quoted", []string{`Bearer resource_metadata="https://a.example.com/x", scope="read write"`},
			map[string]string{"resource_metadata": "https://a.example.com/x", "scope": "read write"}},
		{"another scheme first", []string{`Basic realm="x", Bearer error="invalid_token", resource_metadata=https://a.example.com/m`},
			map[string]string{"error": "invalid_token", "resource_metadata": "https://a.example.com/m"}},
		{"token68 of another scheme", []string{`Negotiate abc==, Bearer scope="a"`}, map[string]string{"scope": "a"}},
		{"escapes and case", []string{`bearer Scope="a\"b"`}, map[string]string{"scope": `a"b`}},
		{"two headers", []string{`Basic realm="x"`, `Bearer scope="s"`}, map[string]string{"scope": "s"}},
		{"none", []string{`Basic realm="x"`}, map[string]string{}},
		{"unterminated", []string{`Bearer scope="abc`}, map[string]string{"scope": "abc"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bearerParams(tt.values); !maps.Equal(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCandidates(t *testing.T) {
	u, _ := url.Parse("https://mcp.example.com/v1/mcp")
	if got := resourceCandidates(u); !slices.Equal(got, []string{
		"https://mcp.example.com/.well-known/oauth-protected-resource/v1/mcp",
		"https://mcp.example.com/.well-known/oauth-protected-resource"}) {
		t.Fatalf("resource = %v", got)
	}
	iss, _ := url.Parse("https://auth.example.com/tenant")
	if got := authCandidates(iss); !slices.Equal(got, []string{
		"https://auth.example.com/.well-known/oauth-authorization-server/tenant",
		"https://auth.example.com/.well-known/openid-configuration/tenant",
		"https://auth.example.com/tenant/.well-known/openid-configuration"}) {
		t.Fatalf("auth = %v", got)
	}
}

func TestPathUnder(t *testing.T) {
	for _, tt := range []struct {
		path, base string
		want       bool
	}{
		{"/mcp", "", true}, {"/mcp", "/", true}, {"/mcp", "/mcp", true}, {"/mcp/", "/mcp", true},
		{"/mcp/x", "/mcp", true}, {"/mcpx", "/mcp", false}, {"/mc", "/mcp", false}, {"/", "/mcp", false},
	} {
		if got := pathUnder(tt.path, tt.base); got != tt.want {
			t.Errorf("pathUnder(%q, %q) = %v", tt.path, tt.base, got)
		}
	}
}

func TestCheckAddress(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://a.example.com/x": true, "http://a.example.com/x": false, "https://u:p@a.example.com/": false,
		"/relative": false, "ftp://a.example.com/": false, "https://A.Example.com:443/x#f": true,
	} {
		if _, err := checkAddress(raw); (err == nil) != ok {
			t.Errorf("checkAddress(%q) = %v", raw, err)
		}
	}
}
