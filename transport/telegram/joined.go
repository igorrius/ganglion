package telegram

import (
	"encoding/json"
	"strings"

	"github.com/igorrius/ganglion/event"
)

// Statuses of a chat member that mean the bot is in the chat.
const (
	statusMember = "member"
	statusAdmin  = "administrator"
)

// joinedEvent turns "the bot was added to a group" into a group.joined
// event in the group's conversation (R-DLV-17). Leaving, being removed and
// a change in a chat that is no group are skipped.
func (t *Transport) joinedEvent(me user, u update) (event.Event, bool) {
	c := u.MyChatMember
	in := c.NewChatMember.Status == statusMember || c.NewChatMember.Status == statusAdmin
	if !isGroup(c.Chat) || !in || c.NewChatMember.User.ID != me.ID || c.From.IsBot {
		t.log.Debug("telegram: update skipped", "update_id", u.UpdateID, "reason", "not the bot joining a group")
		return event.Event{}, false
	}
	key := GroupKey(c.Chat.ID)
	payload, err := json.Marshal(event.GroupSeen{Group: key, Conv: key, Title: c.Chat.Title,
		From: Principal(c.From.ID), Name: strings.TrimSpace(c.From.FirstName + " " + c.From.LastName)})
	if err != nil {
		return event.Event{}, false // a struct of strings always marshals
	}
	now := t.now()
	return event.Event{ID: event.NewID(now), IdemKey: idemKey(me.ID, u.UpdateID), Source: Name, Conv: key,
		Kind: event.KindGroupJoined, Payload: payload, At: now}, true
}
