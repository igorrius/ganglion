package groups

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
)

// Actions of the buttons under a group's request.
const (
	ActionAllow  = "group.allow"
	ActionRefuse = "group.refuse"
)

// nameRunes caps the name written to the node's file: a title is the
// group's own text.
const nameRunes = 64

// varTitle is the placeholder of a group's title in the texts.
const varTitle = "title"

// errNotAdmin refuses a press by someone who is not an admin.
var errNotAdmin = errors.New("groups: only an admin connects a group")

// ButtonArgs are the arguments of both buttons.
type ButtonArgs struct {
	Group string `json:"group"`
	Title string `json:"title,omitempty"`
}

// Writer writes a group's line to the node's file (settings.TelegramFile).
type Writer interface {
	SetGroup(g settings.Group) error
}

// ChatFunc returns the chat id of a group's key; ok is false for a key
// that is no group's (telegram.GroupChat).
type ChatFunc func(group string) (chat int64, ok bool)

// AdminFunc reports whether principal is an admin (access.List.IsAdmin).
type AdminFunc func(ctx context.Context, principal string) (bool, error)

// Actions are the buttons under a group's request.
type Actions struct {
	Store   *Store
	File    Writer
	Chat    ChatFunc
	IsAdmin AdminFunc
}

// QuickActions are the buttons by action name, for the agent
// (agent.Deps.QuickActions).
func (a Actions) QuickActions() map[string]pending.QuickAction {
	return map[string]pending.QuickAction{ActionAllow: a.allow, ActionRefuse: a.refuse}
}

// allow connects the group with the role family (R-DLV-17). It is
// idempotent: the line is set, not added. The note for the model names
// the group by its key, never by its title, which is outside text.
func (a Actions) allow(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	args, chat, err := a.pressed(ctx, p)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	g := settings.Group{Chat: chat, Name: text.Truncate(args.Title, nameRunes), Role: settings.GroupRoleFamily}
	if err := a.File.SetGroup(g); err != nil {
		return pending.QuickEdit{}, fmt.Errorf("groups: connect %s: %w", args.Group, err)
	}
	if err := a.Store.Forget(ctx, args.Group); err != nil {
		return pending.QuickEdit{}, err
	}
	return pending.QuickEdit{
		Text: p.Speak.Text(texts.GroupAllowed, texts.Vars{varTitle: texts.Escape(g.Name)}),
		Note: "the owner connected the group chat " + args.Group + " by button; its members may now talk to you there",
	}, nil
}

// refuse remembers that the group is not to be asked about again.
func (a Actions) refuse(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	args, _, err := a.pressed(ctx, p)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	if err := a.Store.Set(ctx, args.Group, args.Title, StatusRefused); err != nil {
		return pending.QuickEdit{}, err
	}
	return pending.QuickEdit{
		Text: p.Speak.Text(texts.GroupRefused, texts.Vars{varTitle: texts.Escape(text.Truncate(args.Title, nameRunes))}),
		Note: "the owner refused to connect the group chat " + args.Group + " by button",
	}, nil
}

// pressed checks who pressed and what the button names.
func (a Actions) pressed(ctx context.Context, p pending.QuickPress) (ButtonArgs, int64, error) {
	admin, err := a.IsAdmin(ctx, p.Button.Principal)
	if err != nil {
		return ButtonArgs{}, 0, fmt.Errorf("groups: %w", err)
	}
	if !admin {
		return ButtonArgs{}, 0, errNotAdmin
	}
	var args ButtonArgs
	if err := json.Unmarshal(p.Button.Args, &args); err != nil {
		return ButtonArgs{}, 0, fmt.Errorf("groups: button arguments: %w", err)
	}
	chat, ok := a.Chat(args.Group)
	if !ok {
		return ButtonArgs{}, 0, fmt.Errorf("groups: %q is not a group", args.Group)
	}
	return args, chat, nil
}
