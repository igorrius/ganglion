// Package telegram is the Telegram transport: our own minimal Bot API
// client (ADR-0018), long polling and the send path. Bot API types are
// unexported; the rest of the system sees only event.Event and
// event.Outgoing (ADR-0017).
package telegram

// update is a Bot API Update; only messages are requested.
type update struct {
	UpdateID int64    `json:"update_id"`
	Message  *message `json:"message,omitempty"`
}

// message is a Bot API Message, reduced to the fields we read.
type message struct {
	MessageID       int64  `json:"message_id"`
	MessageThreadID int64  `json:"message_thread_id,omitempty"`
	IsTopicMessage  bool   `json:"is_topic_message,omitempty"`
	Date            int64  `json:"date"` // Unix seconds
	Chat            chat   `json:"chat"`
	From            *user  `json:"from,omitempty"`
	Text            string `json:"text,omitempty"`
	Caption         string `json:"caption,omitempty"`
}

type chat struct {
	ID      int64  `json:"id"`
	Type    string `json:"type"`
	IsForum bool   `json:"is_forum,omitempty"`
}

type user struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
}

// sendParams are the parameters of sendMessage that we use.
type sendParams struct {
	ChatID              int64  `json:"chat_id"`
	MessageThreadID     int64  `json:"message_thread_id,omitempty"`
	Text                string `json:"text"`
	DisableNotification bool   `json:"disable_notification,omitempty"`
}
