package settings_test

import (
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/settings"
)

const finger = "sha256:" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func shop() settings.MCPServer {
	return settings.MCPServer{URL: "https://mcp.example.com/mcp", Auth: settings.MCPAuthOAuth,
		Timeout: settings.Duration(settings.DefaultMCPTimeout), Fingerprint: finger,
		Tools: map[string]settings.MCPPolicy{"search": settings.MCPPolicyAuto, "order": settings.MCPPolicyConfirm}}
}

func TestMCPFileSetsChangesAndRemovesAServer(t *testing.T) {
	p := writeFile(t, twoModules)
	f := settings.MCPFile{Path: p}
	if err := f.SetServer("shop", shop()); err != nil {
		t.Fatal(err)
	}
	got := loaded(t, p).MCP
	if s := got.Servers["shop"]; s.URL != shop().URL || s.Tools["order"] != settings.MCPPolicyConfirm ||
		time.Duration(s.Timeout) != settings.DefaultMCPTimeout || got.Page != settings.DefaultIntakePage {
		t.Fatalf("got %+v", got)
	}
	if !strings.Contains(read(t, p), "tz: Europe/Kyiv # home") {
		t.Fatalf("a comment was lost:\n%s", read(t, p))
	}
	if err := f.SetPolicies("shop", map[string]settings.MCPPolicy{"order": settings.MCPPolicyAuto}); err != nil {
		t.Fatal(err)
	}
	if loaded(t, p).MCP.Servers["shop"].Tools["order"] != settings.MCPPolicyAuto {
		t.Fatalf("the policy was not written:\n%s", read(t, p))
	}
	if err := f.SetPolicies("other", map[string]settings.MCPPolicy{"x": settings.MCPPolicyAuto}); err == nil {
		t.Fatal("a policy of a server with no line was written")
	}
	if err := f.RemoveServer("shop"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(read(t, p), "mcp") {
		t.Fatalf("the emptied section stayed:\n%s", read(t, p))
	}
}

func TestMCPFileRefusesABadLine(t *testing.T) {
	p := writeFile(t, twoModules)
	f := settings.MCPFile{Path: p}
	bad := map[string]func(*settings.MCPServer){
		"http":        func(s *settings.MCPServer) { s.URL = "http://mcp.example.com/" },
		"user info":   func(s *settings.MCPServer) { s.URL = "https://u:p@mcp.example.com/" },
		"auth":        func(s *settings.MCPServer) { s.Auth = "basic" },
		"no header":   func(s *settings.MCPServer) { s.Auth = settings.MCPAuthHeader },
		"header":      func(s *settings.MCPServer) { s.Auth, s.Header = settings.MCPAuthHeader, "Authorization" },
		"policy":      func(s *settings.MCPServer) { s.Tools["search"] = "maybe" },
		"tool":        func(s *settings.MCPServer) { s.Tools["Bad-Name"] = settings.MCPPolicyAuto },
		"fingerprint": func(s *settings.MCPServer) { s.Fingerprint = "md5:1" },
	}
	for name, spoil := range bad {
		s := shop()
		spoil(&s)
		if err := f.SetServer("shop", s); err == nil {
			t.Errorf("%s: written", name)
		}
	}
	if err := f.SetServer("demo-reader", shop()); err == nil {
		t.Error("a bad server name was written")
	}
	if err := f.SetServer("other", shop()); err == nil {
		t.Error("a module's short name was written as a server")
	}
	if strings.Contains(read(t, p), "mcp") {
		t.Fatalf("a refused line was written:\n%s", read(t, p))
	}
}
