// Package groups is what the node knows about group chats: which are
// connected (the node's file), and which asked to be (its database). A
// group that is not connected gets one request to an admin (R-DLV-17,
// ADR-0076).
package groups

import (
	"context"
	"fmt"
	"time"

	"github.com/igorrius/ganglion/settings"
)

// MaxPending is how many requests may wait at once: a stranger who adds
// the bot to many groups cannot flood the admin.
const MaxPending = 10

// RequestTTL is how long a request waits for the admin; after it the
// group may ask again.
const RequestTTL = 7 * 24 * time.Hour

// ListFunc returns the connected groups (settings.TelegramFile.Groups).
type ListFunc func() ([]settings.Group, error)

// KeyFunc turns a chat id into the group's key (telegram.GroupKey).
type KeyFunc func(chat int64) string

// Connected answers whether a group is connected. It asks List on every
// call, so a group allowed a moment ago is connected at once.
type Connected struct {
	List ListFunc
	Key  KeyFunc
}

// Has reports whether group is connected. An error reading the list is
// returned: it is never read as "not connected".
func (c Connected) Has(_ context.Context, group string) (bool, error) {
	gs, err := c.List()
	if err != nil {
		return false, fmt.Errorf("groups: %w", err)
	}
	for _, g := range gs {
		if c.Key(g.Chat) == group {
			return true, nil
		}
	}
	return false, nil
}
