package config

import (
	"log/slog"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/groups"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/transport/telegram"
	"github.com/samber/do/v2"
)

// GroupStore provides the requests of group chats that are not connected.
func GroupStore() do.Provider[*groups.Store] {
	return func(i do.Injector) (*groups.Store, error) {
		return groups.New(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB, time.Now)
	}
}

// GroupsFile provides the telegram: section of the node's file.
func GroupsFile() do.Provider[settings.TelegramFile] {
	return func(i do.Injector) (settings.TelegramFile, error) {
		return settings.TelegramFile{Path: settingsFile(do.MustInvoke[Bootstrap](i))}, nil
	}
}

// AccessGroups provides what the access filter knows about group chats:
// the groups of the node's file are connected, and any other becomes a
// request to an admin.
func AccessGroups() do.Provider[access.Groups] {
	return func(i do.Injector) (access.Groups, error) {
		connected := groups.Connected{List: do.MustInvoke[settings.TelegramFile](i).Groups, Key: telegram.GroupKey}
		requests := groups.NewRequests(do.MustInvoke[*groups.Store](i), do.MustInvoke[alert.Admins](i),
			do.MustInvoke[*sqlqueue.Queue](i).Put, time.Now, do.MustInvoke[*slog.Logger](i))
		return access.Groups{Has: connected.Has, Unknown: requests.Seen}, nil
	}
}

// GroupActions provides the buttons under a group's request: Allow writes
// the group's line to the node's file.
func GroupActions() do.Provider[groups.Actions] {
	return func(i do.Injector) (groups.Actions, error) {
		return groups.Actions{Store: do.MustInvoke[*groups.Store](i), File: do.MustInvoke[settings.TelegramFile](i),
			Chat: telegram.GroupChat, IsAdmin: do.MustInvoke[*access.List](i).IsAdmin}, nil
	}
}

// GroupTopics provides the titles of the forum topics of connected groups.
func GroupTopics() do.Provider[groups.Topics] {
	return func(i do.Injector) (groups.Topics, error) {
		connected := groups.Connected{List: do.MustInvoke[settings.TelegramFile](i).Groups, Key: telegram.GroupKey}
		return groups.NewTopics(do.MustInvoke[*groups.Store](i), connected.Has, do.MustInvoke[*slog.Logger](i)), nil
	}
}
