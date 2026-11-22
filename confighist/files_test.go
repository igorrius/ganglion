package confighist_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/confighist"
	"github.com/igorrius/ganglion/settings"
)

func TestWrappersRecordTheirSource(t *testing.T) {
	ctx := context.Background()
	file, repo := setup(t, "")
	h := confighist.New(file, repo, quiet())
	mods := confighist.ModulesFile{File: settings.ModulesFile{Path: file}, History: h}
	dests := confighist.DestinationsFile{File: settings.DestinationsFile{Path: file}, History: h}
	tg := confighist.TelegramFile{File: settings.TelegramFile{Path: file}, History: h}
	mcp := confighist.MCPFile{File: settings.MCPFile{Path: file}, History: h}
	lnk := confighist.LinksFile{File: settings.LinksFile{Path: file}, History: h}
	shop := settings.MCPServer{URL: "https://mcp.example.com/mcp", Auth: settings.MCPAuthNone, Timeout: settings.Duration(time.Minute),
		Tools: map[string]settings.MCPPolicy{"search": settings.MCPPolicyAuto}, Fingerprint: "sha256:" + strings.Repeat("0", 64)}

	steps := []struct {
		do     func() error
		source confighist.Source
		reason string
	}{
		{func() error { return mods.SetModule(settings.Module{Name: "local/demo-reader"}) }, confighist.SourceModule, "module local/demo-reader: settings"},
		{func() error { return mods.SetOff("local/demo-reader", true, "crashloop") }, confighist.SourceModule, "module local/demo-reader: off (crashloop)"},
		{func() error { return mods.SetOff("local/demo-reader", false, "") }, confighist.SourceModule, "module local/demo-reader: on"},
		{func() error {
			return dests.SetDestination(settings.Destination{Name: "home", Conv: "telegram:-1001234567890"})
		}, confighist.SourceDestination, "destination home: set"},
		{func() error { return mods.SetNotifyTo("local/demo-reader", []string{"home"}) }, confighist.SourceModule, "module local/demo-reader: notify_to"},
		{func() error { return dests.SetNotifyTo("local/demo-reader", nil) }, confighist.SourceModule, "module local/demo-reader: notify_to"},
		{func() error { return dests.RemoveDestination("home") }, confighist.SourceDestination, "destination home: removed"},
		{func() error { return tg.SetGroup(settings.Group{Chat: -1001234567890, Name: "family", Role: "family"}) }, confighist.SourceGroup, "group -1001234567890: connected"},
		{func() error { return tg.RemoveGroup(-1001234567890) }, confighist.SourceGroup, "group -1001234567890: disconnected"},
		{func() error { return mods.RemoveModule("local/demo-reader") }, confighist.SourceModule, "module local/demo-reader: removed"},
		{func() error { return mcp.SetServer("shop", shop, "connected") }, confighist.SourceMCP, "mcp server shop: connected"},
		{func() error {
			return mcp.SetPolicies("shop", map[string]settings.MCPPolicy{"search": settings.MCPPolicyConfirm})
		}, confighist.SourceMCP, "mcp server shop: rules of 1 tools"},
		{func() error { return mcp.RemoveServer("shop") }, confighist.SourceMCP, "mcp server shop: disconnected"},
		{func() error { return lnk.AddLink(settings.Link{From: "a.x", To: "b.y"}) }, confighist.SourceLink, "link a.x → b.y: added"},
		{func() error { return lnk.RemoveLink(settings.Link{From: "a.x", To: "b.y"}) }, confighist.SourceLink, "link a.x → b.y: removed"},
	}
	for i, s := range steps {
		if err := s.do(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		cs, err := h.Changes(ctx, 1)
		if err != nil || len(cs) != 1 || cs[0].Source != s.source || cs[0].Reason != s.reason {
			t.Fatalf("step %d: Changes = %+v, %v; want %s %q", i, cs, err, s.source, s.reason)
		}
	}
	before, _ := h.Changes(ctx, 100)
	if _, err := mods.Modules(); err != nil {
		t.Fatal(err)
	}
	if _, err := dests.Destinations(); err != nil {
		t.Fatal(err)
	}
	if _, err := tg.Groups(); err != nil {
		t.Fatal(err)
	}
	if after, _ := h.Changes(ctx, 100); len(after) != len(before) {
		t.Fatal("a read was recorded")
	}
}
