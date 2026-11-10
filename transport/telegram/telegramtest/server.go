// Package telegramtest is a fake Telegram Bot API for tests and the crash
// harness. It keeps the semantics our transport relies on: an offset
// confirms updates, long polling wakes on a new message, a second poller
// ends the first with 409, a webhook blocks polling with 409, and errors
// carry error_code, description and retry_after like the real API.
package telegramtest

import (
	"cmp"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
)

// MaxTextLen is the longest message text sendMessage accepts, in UTF-16
// code units, as Telegram counts it.
const MaxTextLen = 4096

// maxUpdates is how many updates one getUpdates call returns at most.
const maxUpdates = 100

// Incoming is a message a person sends to the bot.
type Incoming struct {
	ChatID   int64 // negative for a group
	ThreadID int64 // forum topic; 0 for none
	UserID   int64
	Name     string
	Text     string // empty: a message without text, such as a sticker
	Caption  string // caption of a photo or document
	IsBot    bool   // the sender is a bot
	Language string // the sender's language_code; "" for none
	// Extra are more Bot API message fields, set as they are: "photo",
	// "document", "voice" and the like.
	Extra map[string]any
}

// Outgoing is a message the bot sent.
type Outgoing struct {
	ChatID   int64
	ThreadID int64
	Text     string
	Silent   bool
	Entities string // the entities as sent, JSON; "" when none
	ReplyTo  int64  // reply_parameters.message_id; 0 when not a reply
	Keyboard string // reply_markup.inline_keyboard as sent, JSON; "" when none or empty
	Rich     bool   // sent by sendRichMessage; Text is its plain text
	// NoPreview is link_preview_options.is_disabled: the message shows no
	// link preview.
	NoPreview bool
}

// Callback is a press of an inline button under a message the bot sent.
type Callback struct {
	ChatID    int64
	ThreadID  int64 // forum topic of the message; 0 for none
	UserID    int64
	Name      string
	MessageID int64  // the message holding the button; 0 for an inaccessible message
	Data      string // the button's callback_data
	Language  string // the presser's language_code; "" for none
}

// Edit is one successful edit; an edit of the buttons only keeps the text.
type Edit struct {
	ChatID    int64
	MessageID int64
	Text      string
	Entities  string // JSON; "" when none
	Keyboard  string // JSON; "" when none or empty
}

// shown is what a sent message shows now, for editMessageText.
type shown struct {
	text, entities, keyboard string
}

// Toast is one answerCallbackQuery call: the press answered and the
// notice shown with it ("" for none).
type Toast struct {
	ID   string
	Text string
}

// BotCommand is one command of a setMyCommands call.
type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// CommandSet is one successful setMyCommands call: the commands and the
// language they are for ("" for every other language).
type CommandSet struct {
	Language string
	Commands []BotCommand
}

// ChatAction is one sendChatAction call.
type ChatAction struct {
	ChatID   int64
	ThreadID int64
	Action   string
}

type failure struct {
	code       int
	retryAfter int
}

// Server is a fake Bot API on an httptest.Server.
type Server struct {
	ts     *httptest.Server
	token  string
	botID  int64
	closed chan struct{}

	mu         sync.Mutex
	updates    []map[string]any // pending, in update_id order
	nextUpdate int64
	nextMsg    int64
	confirmed  int64
	sent       []Outgoing
	uploads    []Upload
	fail       map[string]failure
	webhook    bool
	kick       chan struct{}            // closed to end the getUpdates call in flight
	notify     chan struct{}            // closed and replaced when a message arrives
	threads    map[int64]map[int64]bool // chat → known forum topics
	calls      map[string]int
	actions    []ChatAction
	reactions  []Reaction
	allowed    []string                  // allowed_updates of the last getUpdates that gave them
	messages   map[int64]map[int64]shown // chat → message id → what the bot's message shows
	edits      []Edit
	answered   []string // callback query ids answered
	toasts     []Toast
	commands   []CommandSet
	held       map[string]chan struct{} // methods whose calls wait until released
	methods    []string                 // every call with the right token, in order
	nextQuery  int64
}

// T is what the fake needs from its owner: a test, or chatlab's program,
// which cannot implement testing.TB (ADR-0048).
type T interface {
	Helper()
	Fatalf(format string, args ...any)
	Cleanup(func())
}

// NewServer starts a fake Bot API for token (<bot_id>:<secret>). It is
// closed by t.Cleanup.
func NewServer(t T, token string) *Server {
	t.Helper()
	prefix, _, _ := strings.Cut(token, ":")
	id, err := strconv.ParseInt(prefix, 10, 64)
	if err != nil {
		t.Fatalf("telegramtest: token %q has no numeric bot id", prefix)
	}
	s := &Server{
		token:      token,
		botID:      id,
		closed:     make(chan struct{}),
		nextUpdate: 100000,
		fail:       map[string]failure{},
		notify:     make(chan struct{}),
		threads:    map[int64]map[int64]bool{},
		calls:      map[string]int{},
		messages:   map[int64]map[int64]shown{},
		held:       map[string]chan struct{}{},
	}
	s.ts = httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(func() {
		close(s.closed) // releases held polls, so Close does not wait for them
		s.ts.Close()
	})
	return s
}

// URL is the base URL to give the client instead of https://api.telegram.org.
func (s *Server) URL() string { return s.ts.URL }

// AddMessage queues a message to the bot and returns its update_id.
func (s *Server) AddMessage(m Incoming) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextUpdate++
	s.nextMsg++
	chatType := "private"
	if m.ChatID < 0 {
		chatType = "supergroup"
	}
	name := m.Name
	if name == "" {
		name = "User"
	}
	msg := map[string]any{
		"message_id": s.nextMsg,
		"date":       time.Now().Unix(),
		"chat":       map[string]any{"id": m.ChatID, "type": chatType, "is_forum": m.ThreadID != 0},
		"from":       sender(m.UserID, m.IsBot, name, m.Language),
	}
	if m.Text != "" {
		msg["text"] = m.Text
	}
	if m.Caption != "" {
		msg["caption"] = m.Caption
	}
	maps.Copy(msg, m.Extra)
	if m.ThreadID != 0 {
		msg["message_thread_id"] = m.ThreadID
		msg["is_topic_message"] = true
		if s.threads[m.ChatID] == nil {
			s.threads[m.ChatID] = map[int64]bool{}
		}
		s.threads[m.ChatID][m.ThreadID] = true
	}
	s.updates = append(s.updates, map[string]any{"update_id": s.nextUpdate, "message": msg})
	close(s.notify)
	s.notify = make(chan struct{})
	return s.nextUpdate
}

// ReplyTo is the Bot API reply_to_message object of the bot's message
// messageID in chatID as it shows now, after its edits, for Incoming.Extra.
// It is nil when the bot has no such message.
func (s *Server) ReplyTo(chatID, messageID int64) map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.messages[chatID][messageID]
	if !ok {
		return nil
	}
	chatType := "private"
	if chatID < 0 {
		chatType = "supergroup"
	}
	return map[string]any{
		"message_id": messageID,
		"date":       time.Now().Unix(),
		"chat":       map[string]any{"id": chatID, "type": chatType},
		"from":       sender(s.botID, true, "Test", ""),
		"text":       m.text,
	}
}

// sender is the Bot API User of an update.
func sender(id int64, isBot bool, name, language string) map[string]any {
	from := map[string]any{"id": id, "is_bot": isBot, "first_name": name}
	if language != "" {
		from["language_code"] = language
	}
	return from
}

// AddCallback queues a press of an inline button and returns its
// update_id. The callback query's id is "cq<n>".
func (s *Server) AddCallback(c Callback) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextUpdate++
	s.nextQuery++
	name := c.Name
	if name == "" {
		name = "User"
	}
	chatType := "private"
	if c.ChatID < 0 {
		chatType = "supergroup"
	}
	msg := map[string]any{"message_id": c.MessageID, "date": time.Now().Unix(), "chat": map[string]any{"id": c.ChatID, "type": chatType}}
	if c.MessageID == 0 {
		msg["date"] = 0 // an inaccessible message
	}
	if c.ThreadID != 0 {
		msg["message_thread_id"], msg["is_topic_message"] = c.ThreadID, true
	}
	s.updates = append(s.updates, map[string]any{"update_id": s.nextUpdate, "callback_query": map[string]any{
		"id":            "cq" + strconv.FormatInt(s.nextQuery, 10),
		"from":          sender(c.UserID, false, name, c.Language),
		"message":       msg,
		"chat_instance": "ci" + strconv.FormatInt(c.ChatID, 10),
		"data":          c.Data,
	}})
	close(s.notify)
	s.notify = make(chan struct{})
	return s.nextUpdate
}

// Answered returns the ids of the callback queries answered, in order.
func (s *Server) Answered() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.answered...)
}

// Toasts returns the answerCallbackQuery calls with their texts, in
// order.
func (s *Server) Toasts() []Toast {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Toast(nil), s.toasts...)
}

// CommandSets returns the successful setMyCommands calls, in order.
func (s *Server) CommandSets() []CommandSet {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]CommandSet(nil), s.commands...)
}

// Methods returns the Bot API methods called with the right token, in the
// order they arrived, getUpdates and getMe left out: what the bot did.
func (s *Server) Methods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.methods...)
}

// Edits returns the successful edits (editMessageText and
// editMessageReplyMarkup), in order.
func (s *Server) Edits() []Edit {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Edit(nil), s.edits...)
}

// AllowedUpdates returns the allowed_updates of the last getUpdates call
// that gave them.
func (s *Server) AllowedUpdates() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.allowed...)
}

// Sent returns the messages delivered by sendMessage, in order.
func (s *Server) Sent() []Outgoing {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Outgoing(nil), s.sent...)
}

// Shown is a message the bot sent as the chat shows it now, after any
// edits.
type Shown struct {
	ChatID    int64
	MessageID int64
	Text      string
	Keyboard  string // JSON; "" when none or empty
}

// Showing returns the bot's messages as they look now, in the order they
// were sent, so a test can press a button that an edit put there.
func (s *Server) Showing() []Shown {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Shown
	for chat, msgs := range s.messages {
		for id, m := range msgs {
			out = append(out, Shown{ChatID: chat, MessageID: id, Text: m.text, Keyboard: m.keyboard})
		}
	}
	slices.SortFunc(out, func(a, b Shown) int { return cmp.Compare(a.MessageID, b.MessageID) })
	return out
}

// Confirmed returns the highest update_id confirmed by an offset, or 0.
func (s *Server) Confirmed() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.confirmed
}

// LastMessageID returns the message_id of the last message added.
func (s *Server) LastMessageID() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextMsg
}

// Reaction is a reaction the bot set; Emoji "" took one off.
type Reaction struct {
	ChatID, MessageID int64
	Emoji             string
}

// Reactions returns the reactions the bot set, oldest first.
func (s *Server) Reactions() []Reaction {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.reactions)
}

// ChatActions returns the sendChatAction calls received.
func (s *Server) ChatActions() []ChatAction {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ChatAction(nil), s.actions...)
}

// Calls returns how many times method was called with the right token.
func (s *Server) Calls(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[method]
}

// Hold makes every call of method wait, before it is answered, until the
// returned release is called or the caller gives up; release is safe to
// call more than once.
func (s *Server) Hold(method string) (release func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ch := make(chan struct{})
	s.held[method] = ch
	var once sync.Once
	return func() { once.Do(func() { close(ch) }) }
}

// FailNext makes the next call of method fail with code; retryAfter > 0
// adds parameters.retry_after (use it with 429).
func (s *Server) FailNext(method string, code, retryAfter int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fail[method] = failure{code: code, retryAfter: retryAfter}
}

// SetWebhook turns a webhook on or off; while it is on, getUpdates
// returns 409.
func (s *Server) SetWebhook(on bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.webhook = on
}

func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, "/bot")
	i := strings.LastIndex(rest, "/")
	if !ok || i < 0 {
		writeErr(w, http.StatusNotFound, "Not Found", 0)
		return
	}
	token, method := rest[:i], rest[i+1:]
	if token != s.token {
		writeErr(w, http.StatusUnauthorized, "Unauthorized", 0)
		return
	}
	params := map[string]json.RawMessage{}
	var file *formFile
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		var err error
		if params, file, err = readForm(r); err != nil {
			writeErr(w, http.StatusBadRequest, "Bad Request: can't parse the form", 0)
			return
		}
	} else if err := json.NewDecoder(r.Body).Decode(&params); err != nil && err.Error() != "EOF" {
		writeErr(w, http.StatusBadRequest, "Bad Request: can't parse JSON", 0)
		return
	}

	s.mu.Lock()
	hold := s.held[method]
	s.mu.Unlock()
	if hold != nil {
		select {
		case <-hold:
		case <-r.Context().Done():
			return
		case <-s.closed:
			return
		}
	}

	s.mu.Lock()
	s.calls[method]++
	if method != "getUpdates" && method != "getMe" {
		s.methods = append(s.methods, method)
	}
	if f, ok := s.fail[method]; ok {
		delete(s.fail, method)
		s.mu.Unlock()
		desc := http.StatusText(f.code)
		if f.code == http.StatusTooManyRequests && f.retryAfter > 0 {
			desc = fmt.Sprintf("Too Many Requests: retry after %d", f.retryAfter)
		}
		writeErr(w, f.code, desc, f.retryAfter)
		return
	}
	s.mu.Unlock()

	switch method {
	case "getMe":
		writeOK(w, map[string]any{"id": s.botID, "is_bot": true, "first_name": "Test", "username": "test_bot"})
	case "getUpdates":
		s.getUpdates(w, r, params)
	case "sendMessage":
		s.sendMessage(w, params)
	case "sendRichMessage":
		s.sendRichMessage(w, params)
	case "sendPhoto", "sendDocument":
		s.sendFile(w, method, params, file)
	case "editMessageText":
		s.editMessageText(w, params)
	case "editMessageReplyMarkup":
		s.editMessageReplyMarkup(w, params)
	case "answerCallbackQuery":
		var id, text string
		_ = json.Unmarshal(params["callback_query_id"], &id)
		_ = json.Unmarshal(params["text"], &text)
		s.mu.Lock()
		s.answered = append(s.answered, id)
		s.toasts = append(s.toasts, Toast{ID: id, Text: text})
		s.mu.Unlock()
		writeOK(w, true)
	case "setMyCommands":
		var cs CommandSet
		_ = json.Unmarshal(params["commands"], &cs.Commands)
		_ = json.Unmarshal(params["language_code"], &cs.Language)
		if len(cs.Commands) == 0 {
			writeErr(w, http.StatusBadRequest, "Bad Request: commands are empty", 0)
			return
		}
		s.mu.Lock()
		s.commands = append(s.commands, cs)
		s.mu.Unlock()
		writeOK(w, true)
	case "setMessageReaction":
		var p struct {
			ChatID    int64 `json:"chat_id"`
			MessageID int64 `json:"message_id"`
			Reaction  []struct {
				Emoji string `json:"emoji"`
			} `json:"reaction"`
		}
		raw, _ := json.Marshal(params)
		_ = json.Unmarshal(raw, &p)
		r := Reaction{ChatID: p.ChatID, MessageID: p.MessageID}
		if len(p.Reaction) > 0 {
			r.Emoji = p.Reaction[0].Emoji
		}
		s.mu.Lock()
		s.reactions = append(s.reactions, r)
		s.mu.Unlock()
		writeOK(w, true)
	case "sendChatAction":
		var p struct {
			ChatID          int64  `json:"chat_id"`
			MessageThreadID int64  `json:"message_thread_id"`
			Action          string `json:"action"`
		}
		raw, _ := json.Marshal(params)
		_ = json.Unmarshal(raw, &p)
		s.mu.Lock()
		s.actions = append(s.actions, ChatAction{ChatID: p.ChatID, ThreadID: p.MessageThreadID, Action: p.Action})
		s.mu.Unlock()
		writeOK(w, true)
	default:
		writeErr(w, http.StatusNotFound, "Not Found", 0)
	}
}

func intParam(params map[string]json.RawMessage, name string) int64 {
	var v int64
	_ = json.Unmarshal(params[name], &v)
	return v
}

func (s *Server) getUpdates(w http.ResponseWriter, r *http.Request, params map[string]json.RawMessage) {
	offset := intParam(params, "offset")
	timer := time.NewTimer(time.Duration(intParam(params, "timeout")) * time.Second)
	defer timer.Stop()

	var allowed []string
	_ = json.Unmarshal(params["allowed_updates"], &allowed)
	s.mu.Lock()
	if allowed != nil {
		s.allowed = allowed
	}
	if s.webhook {
		s.mu.Unlock()
		writeErr(w, http.StatusConflict,
			"Conflict: can't use getUpdates method while webhook is active; use deleteWebhook to delete the webhook first", 0)
		return
	}
	if offset > 0 {
		kept := s.updates[:0]
		for _, u := range s.updates {
			if u["update_id"].(int64) >= offset {
				kept = append(kept, u)
			}
		}
		s.updates = kept
		s.confirmed = max(s.confirmed, offset-1)
	}
	if s.kick != nil {
		close(s.kick) // the older poller gets 409
	}
	mine := make(chan struct{})
	s.kick = mine

	for {
		select {
		case <-mine:
			s.mu.Unlock()
			writeErr(w, http.StatusConflict,
				"Conflict: terminated by other getUpdates request; make sure that only one bot instance is running", 0)
			return
		default:
		}
		if ups := s.deliverable(); len(ups) > 0 {
			s.kick = nil
			s.mu.Unlock()
			writeOK(w, ups)
			return
		}
		notify := s.notify
		s.mu.Unlock()
		select {
		case <-notify:
		case <-mine:
		case <-timer.C:
			s.release(mine)
			writeOK(w, []any{})
			return
		case <-r.Context().Done():
			s.release(mine)
			return
		case <-s.closed:
			s.release(mine)
			writeOK(w, []any{})
			return
		}
		s.mu.Lock()
	}
}

// deliverable returns the pending updates of the allowed types, at most
// maxUpdates; like Telegram, an empty list allows every type.
func (s *Server) deliverable() []map[string]any {
	var ups []map[string]any
	for _, u := range s.updates {
		if len(ups) == maxUpdates {
			break
		}
		if len(s.allowed) == 0 || slices.ContainsFunc(s.allowed, func(k string) bool { _, ok := u[k]; return ok }) {
			ups = append(ups, u)
		}
	}
	return ups
}

// release clears the in-flight poll unless a newer one replaced it.
func (s *Server) release(mine chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.kick == mine {
		s.kick = nil
	}
}

func (s *Server) sendMessage(w http.ResponseWriter, params map[string]json.RawMessage) {
	var p struct {
		ChatID              int64  `json:"chat_id"`
		MessageThreadID     int64  `json:"message_thread_id"`
		Text                string `json:"text"`
		DisableNotification bool   `json:"disable_notification"`
		Entities            []struct {
			Type   string `json:"type"`
			Offset int    `json:"offset"`
			Length int    `json:"length"`
		} `json:"entities"`
		ReplyParameters *struct {
			MessageID int64 `json:"message_id"`
		} `json:"reply_parameters"`
		ReplyMarkup        *markup `json:"reply_markup"`
		LinkPreviewOptions *struct {
			IsDisabled bool `json:"is_disabled"`
		} `json:"link_preview_options"`
	}
	raw, _ := json.Marshal(params)
	if err := json.Unmarshal(raw, &p); err != nil {
		writeErr(w, http.StatusBadRequest, "Bad Request: "+err.Error(), 0)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case p.ChatID == 0:
		writeErr(w, http.StatusBadRequest, "Bad Request: chat_id is empty", 0)
		return
	case p.MessageThreadID != 0 && !s.threads[p.ChatID][p.MessageThreadID]:
		writeErr(w, http.StatusBadRequest, "Bad Request: message thread not found", 0)
		return
	case p.Text == "":
		writeErr(w, http.StatusBadRequest, "Bad Request: message text is empty", 0)
		return
	case len(utf16.Encode([]rune(p.Text))) > MaxTextLen:
		writeErr(w, http.StatusBadRequest, "Bad Request: message is too long", 0)
		return
	}
	units := len(utf16.Encode([]rune(p.Text)))
	for _, e := range p.Entities {
		if e.Type == "" || e.Offset < 0 || e.Length <= 0 || e.Offset+e.Length > units {
			writeErr(w, http.StatusBadRequest, "Bad Request: can't parse entities", 0)
			return
		}
	}
	out := Outgoing{ChatID: p.ChatID, ThreadID: p.MessageThreadID, Text: p.Text, Silent: p.DisableNotification, Keyboard: p.ReplyMarkup.keyboard()}
	if len(p.Entities) > 0 {
		out.Entities = string(params["entities"])
	}
	if p.ReplyParameters != nil {
		out.ReplyTo = p.ReplyParameters.MessageID
	}
	out.NoPreview = p.LinkPreviewOptions != nil && p.LinkPreviewOptions.IsDisabled
	s.keep(w, out)
}

// keep records a sent message and answers with it. The caller holds s.mu.
func (s *Server) keep(w http.ResponseWriter, out Outgoing) {
	s.sent = append(s.sent, out)
	s.nextMsg++
	if s.messages[out.ChatID] == nil {
		s.messages[out.ChatID] = map[int64]shown{}
	}
	s.messages[out.ChatID][s.nextMsg] = shown{text: out.Text, entities: out.Entities, keyboard: out.Keyboard}
	msg := map[string]any{
		"message_id": s.nextMsg,
		"date":       time.Now().Unix(),
		"chat":       map[string]any{"id": out.ChatID, "type": "private"},
		"text":       out.Text,
	}
	if out.ThreadID != 0 {
		msg["message_thread_id"] = out.ThreadID
	}
	writeOK(w, msg)
}

// markup is reply_markup with an inline keyboard.
type markup struct {
	InlineKeyboard json.RawMessage `json:"inline_keyboard"`
}

// keyboard is the inline keyboard as JSON; "" when there is none or it is
// empty.
func (m *markup) keyboard() string {
	if m == nil || len(m.InlineKeyboard) == 0 {
		return ""
	}
	var rows [][]json.RawMessage
	if err := json.Unmarshal(m.InlineKeyboard, &rows); err != nil || len(rows) == 0 {
		return ""
	}
	return string(m.InlineKeyboard)
}

// editMessageText changes a message the bot sent, with Telegram's
// answers for a message that is gone and for an edit that changes nothing.
func (s *Server) editMessageText(w http.ResponseWriter, params map[string]json.RawMessage) {
	var p struct {
		ChatID      int64           `json:"chat_id"`
		MessageID   int64           `json:"message_id"`
		Text        string          `json:"text"`
		Entities    json.RawMessage `json:"entities"`
		ReplyMarkup *markup         `json:"reply_markup"`
	}
	raw, _ := json.Marshal(params)
	if err := json.Unmarshal(raw, &p); err != nil || p.Text == "" {
		writeErr(w, http.StatusBadRequest, "Bad Request: message text is empty", 0)
		return
	}
	if len(utf16.Encode([]rune(p.Text))) > MaxTextLen {
		writeErr(w, http.StatusBadRequest, "Bad Request: MESSAGE_TOO_LONG", 0)
		return
	}
	e := Edit{ChatID: p.ChatID, MessageID: p.MessageID, Text: p.Text, Keyboard: p.ReplyMarkup.keyboard()}
	if len(p.Entities) > 0 && string(p.Entities) != "null" {
		e.Entities = string(p.Entities)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.messages[p.ChatID][p.MessageID]
	switch {
	case !ok:
		writeErr(w, http.StatusBadRequest, "Bad Request: message to edit not found", 0)
		return
	case cur == shown{text: e.Text, entities: e.Entities, keyboard: e.Keyboard}:
		writeErr(w, http.StatusBadRequest, "Bad Request: message is not modified: specified new message content and reply markup are exactly the same as a current content and reply markup of the message", 0)
		return
	}
	s.messages[p.ChatID][p.MessageID] = shown{text: e.Text, entities: e.Entities, keyboard: e.Keyboard}
	s.edits = append(s.edits, e)
	writeOK(w, map[string]any{"message_id": p.MessageID, "date": time.Now().Unix(), "chat": map[string]any{"id": p.ChatID, "type": "private"}, "text": p.Text})
}

// editMessageReplyMarkup changes only the buttons of a message the bot
// sent; the Edit it records keeps the message's text.
func (s *Server) editMessageReplyMarkup(w http.ResponseWriter, params map[string]json.RawMessage) {
	var p struct {
		ChatID      int64   `json:"chat_id"`
		MessageID   int64   `json:"message_id"`
		ReplyMarkup *markup `json:"reply_markup"`
	}
	raw, _ := json.Marshal(params)
	_ = json.Unmarshal(raw, &p)
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, ok := s.messages[p.ChatID][p.MessageID]
	next := shown{text: cur.text, entities: cur.entities, keyboard: p.ReplyMarkup.keyboard()}
	switch {
	case !ok:
		writeErr(w, http.StatusBadRequest, "Bad Request: message to edit not found", 0)
		return
	case cur == next:
		writeErr(w, http.StatusBadRequest, "Bad Request: message is not modified: specified new message content and reply markup are exactly the same as a current content and reply markup of the message", 0)
		return
	}
	s.messages[p.ChatID][p.MessageID] = next
	s.edits = append(s.edits, Edit{ChatID: p.ChatID, MessageID: p.MessageID, Text: next.text, Entities: next.entities, Keyboard: next.keyboard})
	writeOK(w, true)
}

func writeOK(w http.ResponseWriter, result any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func writeErr(w http.ResponseWriter, code int, desc string, retryAfter int) {
	body := map[string]any{"ok": false, "error_code": code, "description": desc}
	if retryAfter > 0 {
		body["parameters"] = map[string]any{"retry_after": retryAfter}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
