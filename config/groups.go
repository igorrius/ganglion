package config

import (
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/confighist"
	"github.com/igorrius/ganglion/groups"
	"github.com/igorrius/ganglion/modhost"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/samber/do/v2"
)

// GroupStore provides the requests of group chats that are not connected.
func GroupStore() do.Provider[*groups.Store] {
	return func(i do.Injector) (*groups.Store, error) {
		return groups.New(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB, time.Now)
	}
}

// GroupsFile provides the telegram: section of the node's file, every
// change recorded (R-CFG-13).
func GroupsFile() do.Provider[confighist.TelegramFile] {
	return func(i do.Injector) (confighist.TelegramFile, error) {
		return confighist.TelegramFile{File: settings.TelegramFile{Path: settingsFile(do.MustInvoke[Bootstrap](i))},
			History: do.MustInvoke[confighist.History](i)}, nil
	}
}

// AccessGroups provides what the access filter knows about group chats:
// the groups of the node's file are connected, and any other becomes a
// request to an admin.
func AccessGroups() do.Provider[access.Groups] {
	return func(i do.Injector) (access.Groups, error) {
		connected := groups.Connected{List: do.MustInvoke[confighist.TelegramFile](i).Groups, Key: telegram.GroupKey}
		requests := groups.NewRequests(do.MustInvoke[*groups.Store](i), do.MustInvoke[alert.Admins](i),
			do.MustInvoke[*sqlqueue.Queue](i).Put, time.Now, do.MustInvoke[*slog.Logger](i))
		return access.Groups{Has: connected.Has, Unknown: requests.Seen}, nil
	}
}

// GroupActions provides the buttons under a group's request: Allow writes
// the group's line to the node's file.
func GroupActions() do.Provider[groups.Actions] {
	return func(i do.Injector) (groups.Actions, error) {
		return groups.Actions{Store: do.MustInvoke[*groups.Store](i), File: do.MustInvoke[confighist.TelegramFile](i),
			Chat: telegram.GroupChat, IsAdmin: do.MustInvoke[*access.List](i).IsAdmin}, nil
	}
}

// GroupTopics provides the titles of the forum topics of connected groups.
func GroupTopics() do.Provider[groups.Topics] {
	return func(i do.Injector) (groups.Topics, error) {
		connected := groups.Connected{List: do.MustInvoke[confighist.TelegramFile](i).Groups, Key: telegram.GroupKey}
		return groups.NewTopics(do.MustInvoke[*groups.Store](i), connected.Has, do.MustInvoke[*slog.Logger](i)), nil
	}
}

// Destinations provides the named destinations of the node's file.
func Destinations() do.Provider[groups.Destinations] {
	return func(i do.Injector) (groups.Destinations, error) {
		file := settings.DestinationsFile{Path: settingsFile(do.MustInvoke[Bootstrap](i))}
		return groups.Destinations{List: file.Destinations}, nil
	}
}

// Linking provides the admin's tools for the links between modules: the
// node's file, every change recorded, and the active modules' ports from the
// store (R-BUS-5).
func Linking() do.Provider[builtin.Linking] {
	return func(i do.Injector) (builtin.Linking, error) {
		return builtin.Linking{File: linksFile(i), Modules: do.MustInvoke[*modstore.Store](i).LinkModules}, nil
	}
}

// Routing provides the admin's tools for where messages go: destinations,
// a module's notify_to and disconnecting a group.
func Routing() do.Provider[builtin.Routing] {
	return func(i do.Injector) (builtin.Routing, error) {
		path := settingsFile(do.MustInvoke[Bootstrap](i))
		return builtin.Routing{
			Dests:   destinationsFile(i),
			Groups:  do.MustInvoke[confighist.TelegramFile](i),
			Topics:  do.MustInvoke[*groups.Store](i),
			Modules: settings.ModulesFile{Path: path}.Modules,
			Router:  do.MustInvoke[*modhost.Host](i),
			Parse:   telegram.ParseConv,
			Key:     telegram.GroupKey,
			Link:    telegram.ParseLink,
			Private: telegram.PrivateConv,
		}, nil
	}
}
