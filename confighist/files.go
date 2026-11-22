package confighist

import (
	"context"
	"strconv"

	"github.com/igorrius/ganglion/settings"
)

// The wrapped writers below have the methods of the settings types they
// wrap, so their consumers' interfaces do not change. Their methods take
// no context, so a change is recorded under context.Background(): git
// runs apart from the caller's cancellation anyway (ruling R37), and a
// write must not stay unrecorded because a turn ended.

// ModulesFile is settings.ModulesFile with every change recorded.
type ModulesFile struct {
	File    settings.ModulesFile
	History History
}

// Modules reads the modules: lines.
func (f ModulesFile) Modules() ([]settings.Module, error) { return f.File.Modules() }

// SetModule writes a module's line.
func (f ModulesFile) SetModule(m settings.Module) error {
	return f.record("module "+m.Name+": settings", func() error { return f.File.SetModule(m) })
}

// SetOff turns a module off or on.
func (f ModulesFile) SetOff(name string, off bool, why string) error {
	what := "on"
	if off {
		what = "off"
		if why != "" {
			what += " (" + why + ")"
		}
	}
	return f.record("module "+name+": "+what, func() error { return f.File.SetOff(name, off, why) })
}

// Links reads the links: lines.
func (f ModulesFile) Links() ([]settings.Link, error) { return f.File.Links() }

// SetNotifyTo writes a module's notify_to.
func (f ModulesFile) SetNotifyTo(module string, names []string) error {
	return f.record("module "+module+": notify_to", func() error { return f.File.SetNotifyTo(module, names) })
}

// RemoveModule removes a module's line.
func (f ModulesFile) RemoveModule(name string) error {
	return f.record("module "+name+": removed", func() error { return f.File.RemoveModule(name) })
}

func (f ModulesFile) record(reason string, write func() error) error {
	return f.History.Record(context.Background(), SourceModule, reason, write)
}

// DestinationsFile is settings.DestinationsFile with every change recorded.
type DestinationsFile struct {
	File    settings.DestinationsFile
	History History
}

// Destinations reads the destinations.
func (f DestinationsFile) Destinations() ([]settings.Destination, error) {
	return f.File.Destinations()
}

// SetDestination writes a destination.
func (f DestinationsFile) SetDestination(d settings.Destination) error {
	return f.record("destination "+d.Name+": set", func() error { return f.File.SetDestination(d) })
}

// RemoveDestination removes a destination.
func (f DestinationsFile) RemoveDestination(name string) error {
	return f.record("destination "+name+": removed", func() error { return f.File.RemoveDestination(name) })
}

// SetNotifyTo writes a module's notify_to.
func (f DestinationsFile) SetNotifyTo(module string, names []string) error {
	return f.History.Record(context.Background(), SourceModule, "module "+module+": notify_to",
		func() error { return f.File.SetNotifyTo(module, names) })
}

func (f DestinationsFile) record(reason string, write func() error) error {
	return f.History.Record(context.Background(), SourceDestination, reason, write)
}

// TelegramFile is settings.TelegramFile with every change recorded.
type TelegramFile struct {
	File    settings.TelegramFile
	History History
}

// Groups reads the connected groups.
func (f TelegramFile) Groups() ([]settings.Group, error) { return f.File.Groups() }

// SetGroup writes a group's line.
func (f TelegramFile) SetGroup(g settings.Group) error {
	return f.record("group "+chatOf(g.Chat)+": connected", func() error { return f.File.SetGroup(g) })
}

// RemoveGroup removes a group's line.
func (f TelegramFile) RemoveGroup(chat int64) error {
	return f.record("group "+chatOf(chat)+": disconnected", func() error { return f.File.RemoveGroup(chat) })
}

func (f TelegramFile) record(reason string, write func() error) error {
	return f.History.Record(context.Background(), SourceGroup, reason, write)
}

// chatOf names a group in a reason by its chat id. Its title is written by
// its members, and config.history shows reasons to the model.
func chatOf(chat int64) string { return strconv.FormatInt(chat, 10) }

// MCPFile is settings.MCPFile with every change recorded.
type MCPFile struct {
	File    settings.MCPFile
	History History
}

// MCP reads the mcp: section.
func (f MCPFile) MCP() (settings.MCP, error) { return f.File.MCP() }

// SetServer writes a server's line; why says what changed ("connected",
// "approved again").
func (f MCPFile) SetServer(name string, s settings.MCPServer, why string) error {
	return f.record("mcp server "+name+": "+why, func() error { return f.File.SetServer(name, s) })
}

// SetPolicies writes the policies of several tools in one change.
func (f MCPFile) SetPolicies(server string, policies map[string]settings.MCPPolicy) error {
	return f.record("mcp server "+server+": rules of "+strconv.Itoa(len(policies))+" tools",
		func() error { return f.File.SetPolicies(server, policies) })
}

// RemoveServer removes a server's line.
func (f MCPFile) RemoveServer(name string) error {
	return f.record("mcp server "+name+": disconnected", func() error { return f.File.RemoveServer(name) })
}

func (f MCPFile) record(reason string, write func() error) error {
	return f.History.Record(context.Background(), SourceMCP, reason, write)
}

// LinksFile is settings.LinksFile with every change recorded.
type LinksFile struct {
	File    settings.LinksFile
	History History
}

// Links reads the links: lines.
func (f LinksFile) Links() ([]settings.Link, error) { return f.File.Links() }

// AddLink writes a link.
func (f LinksFile) AddLink(l settings.Link) error {
	return f.record("link "+l.String()+": added", func() error { return f.File.AddLink(l) })
}

// RemoveLink removes a link.
func (f LinksFile) RemoveLink(l settings.Link) error {
	return f.record("link "+l.String()+": removed", func() error { return f.File.RemoveLink(l) })
}

func (f LinksFile) record(reason string, write func() error) error {
	return f.History.Record(context.Background(), SourceLink, reason, write)
}
