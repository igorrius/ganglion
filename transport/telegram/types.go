// Package telegram is the Telegram transport: our own minimal Bot API
// client (ADR-0018), long polling and the send path. Bot API types are
// unexported; the rest of the system sees only event.Event and
// event.Outgoing (ADR-0017).
package telegram

import "encoding/json"

// update is a Bot API Update; messages, button presses and the bot's own
// membership of a chat are requested.
type update struct {
	UpdateID      int64          `json:"update_id"`
	Message       *message       `json:"message,omitempty"`
	CallbackQuery *callbackQuery `json:"callback_query,omitempty"`
	MyChatMember  *chatMember    `json:"my_chat_member,omitempty"`
}

// chatMember is a Bot API ChatMemberUpdated about the bot itself, reduced
// to what says the bot was added to a chat.
type chatMember struct {
	Chat          chat `json:"chat"`
	From          user `json:"from"`
	NewChatMember struct {
		Status string `json:"status"`
		User   user   `json:"user"`
	} `json:"new_chat_member"`
}

// callbackQuery is a press of an inline button. Message is the message
// holding the button; Telegram sends date 0 when it is no longer
// accessible.
type callbackQuery struct {
	ID      string   `json:"id"`
	From    user     `json:"from"`
	Message *message `json:"message,omitempty"`
	Data    string   `json:"data,omitempty"`
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
	// Entities and CaptionEntities mark the parts of Text and Caption; of
	// them only what addresses the bot is read (R-DLV-16).
	Entities        []inEntity `json:"entities,omitempty"`
	CaptionEntities []inEntity `json:"caption_entities,omitempty"`
	// RichMessage is a rich formatted message, read when there is no text (R-DLV-12).
	RichMessage json.RawMessage `json:"rich_message,omitempty"`
	// Outside content (ruling R35): a forward's origin, a post Telegram
	// forwarded from a linked channel, a reply to a message in another
	// chat, and the quoted part of the replied message. Of the first three
	// only their presence is read, of the quote its text. The message a
	// reply answers (ReplyToMessage) is outside content too (R-DLV-13): of
	// it the agent gets its id, date, author, text and whether a file came
	// with it.
	ForwardOrigin      json.RawMessage `json:"forward_origin,omitempty"`
	IsAutomaticForward bool            `json:"is_automatic_forward,omitempty"`
	ExternalReply      json.RawMessage `json:"external_reply,omitempty"`
	Quote              *textQuote      `json:"quote,omitempty"`
	ReplyToMessage     *message        `json:"reply_to_message,omitempty"`
	// ForumTopicCreated marks the service message that created a forum topic,
	// the topic's root (R-DLV-13); its presence is read, and its name as the
	// topic's title.
	ForumTopicCreated json.RawMessage `json:"forum_topic_created,omitempty"`
	// ForumTopicEdited marks the service message that renamed a forum topic
	// or changed its icon; of both only the name is read, as the topic's
	// title (R-CFG-12).
	ForumTopicEdited json.RawMessage `json:"forum_topic_edited,omitempty"`
	// Files that came with the message (R-DLV-10); only what describes
	// them is read, never the file id.
	Photo     []fileInfo `json:"photo,omitempty"`
	Document  *fileInfo  `json:"document,omitempty"`
	Audio     *fileInfo  `json:"audio,omitempty"`
	Voice     *fileInfo  `json:"voice,omitempty"`
	Video     *fileInfo  `json:"video,omitempty"`
	VideoNote *fileInfo  `json:"video_note,omitempty"`
	Animation *fileInfo  `json:"animation,omitempty"`
	Sticker   *fileInfo  `json:"sticker,omitempty"`
}

// fileInfo is what a Bot API PhotoSize, Document, Audio, Voice, Video,
// VideoNote, Animation or Sticker says about its file.
type fileInfo struct {
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
}

// textQuote is a Bot API TextQuote, reduced to its text.
type textQuote struct {
	Text string `json:"text"`
}

// present reports whether an optional object was sent: not missing and
// not null.
func present(raw json.RawMessage) bool {
	return len(raw) > 0 && string(raw) != "null"
}

type chat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
	// Title is a group's name: outside text.
	Title   string `json:"title,omitempty"`
	IsForum bool   `json:"is_forum,omitempty"`
}

// inEntity is a Bot API MessageEntity of an incoming message, reduced to
// what the addressing rule reads. Offset and Length count UTF-16 code units.
type inEntity struct {
	Type   string `json:"type"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
	User   *user  `json:"user,omitempty"` // of a text_mention
}

type user struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name,omitempty"`
	Username  string `json:"username,omitempty"`
	// LanguageCode is optional, and an IETF tag when set (R-LANG-1).
	LanguageCode string `json:"language_code,omitempty"`
}

// sendParams are the parameters of sendMessage that we use.
type sendParams struct {
	ChatID              int64               `json:"chat_id"`
	MessageThreadID     int64               `json:"message_thread_id,omitempty"`
	Text                string              `json:"text"`
	Entities            []entity            `json:"entities,omitempty"`
	DisableNotification bool                `json:"disable_notification,omitempty"`
	ReplyParameters     *replyParameters    `json:"reply_parameters,omitempty"`
	ReplyMarkup         *inlineMarkup       `json:"reply_markup,omitempty"`
	LinkPreviewOptions  *linkPreviewOptions `json:"link_preview_options,omitempty"`
}

// linkPreviewOptions are the options of a message's link preview that we use.
type linkPreviewOptions struct {
	IsDisabled bool `json:"is_disabled"`
}

// richParams are the parameters of sendRichMessage that we use.
type richParams struct {
	ChatID              int64            `json:"chat_id"`
	MessageThreadID     int64            `json:"message_thread_id,omitempty"`
	RichMessage         richMessage      `json:"rich_message"`
	DisableNotification bool             `json:"disable_notification,omitempty"`
	ReplyParameters     *replyParameters `json:"reply_parameters,omitempty"`
}

// editParams are the parameters of editMessageText that we use.
type editParams struct {
	ChatID      int64        `json:"chat_id"`
	MessageID   int64        `json:"message_id"`
	Text        string       `json:"text"`
	Entities    []entity     `json:"entities,omitempty"`
	ReplyMarkup inlineMarkup `json:"reply_markup"` // always sent: an empty keyboard removes the buttons
}

// markupParams are the parameters of editMessageReplyMarkup.
type markupParams struct {
	ChatID      int64        `json:"chat_id"`
	MessageID   int64        `json:"message_id"`
	ReplyMarkup inlineMarkup `json:"reply_markup"`
}

// inlineMarkup is an InlineKeyboardMarkup.
type inlineMarkup struct {
	InlineKeyboard [][]inlineButton `json:"inline_keyboard"`
}

type inlineButton struct {
	Text         string `json:"text"`
	CallbackData string `json:"callback_data"`
}

type replyParameters struct {
	MessageID                int64 `json:"message_id"`
	AllowSendingWithoutReply bool  `json:"allow_sending_without_reply"`
}
