package chatlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"net/url"
	"slices"
	"sync/atomic"

	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

// labFile is the Bot API id of every file a lab peer sends.
const labFile = "lab-file"

// Telegram is the Peer of a chat with the fake Bot API: the node's real
// Telegram adapter talks to the same fake.
type Telegram struct {
	fake *telegramtest.Server
	chat int64
	name string
	last atomic.Int64 // the newest update this peer added
}

// NewTelegram returns the person named name in chat, on fake.
func NewTelegram(fake *telegramtest.Server, chat int64, name string) *Telegram {
	return &Telegram{fake: fake, chat: chat, name: name}
}

// Say implements Peer.
func (p *Telegram) Say(_ context.Context, text string) error {
	p.last.Store(p.fake.AddMessage(telegramtest.Incoming{ChatID: p.chat, UserID: p.chat, Name: p.name, Text: text}))
	return nil
}

// SendFile implements Peer.
func (p *Telegram) SendFile(_ context.Context, f File) error {
	var extra map[string]any
	switch f.Kind {
	case KindDocument:
		extra = map[string]any{KindDocument: map[string]any{"file_id": labFile, "file_unique_id": labFile,
			"file_name": f.Name, "mime_type": f.Type, "file_size": f.Size}}
	case KindPhoto:
		extra = map[string]any{KindPhoto: []map[string]any{{"file_id": labFile, "file_unique_id": labFile,
			"width": 800, "height": 600, "file_size": f.Size}}}
	default:
		return fmt.Errorf("chatlab: file kind %q", f.Kind)
	}
	p.last.Store(p.fake.AddMessage(telegramtest.Incoming{ChatID: p.chat, UserID: p.chat, Name: p.name, Caption: f.Caption, Extra: extra}))
	return nil
}

// Press implements Peer.
func (p *Telegram) Press(_ context.Context, label string) error {
	for _, m := range slices.Backward(p.fake.Showing()) {
		for _, b := range buttons(m.Keyboard) {
			if b.Text == label {
				p.last.Store(p.fake.AddCallback(telegramtest.Callback{ChatID: m.ChatID, UserID: p.chat, Name: p.name,
					MessageID: m.MessageID, Data: b.Data}))
				return nil
			}
		}
	}
	return fmt.Errorf("%w: %q", ErrNoButton, label)
}

// Messages implements Peer.
func (p *Telegram) Messages() []Message {
	edited := map[int64]bool{}
	for _, e := range p.fake.Edits() {
		edited[e.MessageID] = true
	}
	type numbered struct {
		id int64
		m  Message
	}
	var all []numbered
	for _, s := range p.fake.Showing() {
		m := Message{Text: s.Text, Replaced: edited[s.MessageID]}
		for _, b := range buttons(s.Keyboard) {
			m.Buttons = append(m.Buttons, b.Text)
		}
		all = append(all, numbered{s.MessageID, m})
	}
	for _, u := range p.fake.Uploads() {
		kind := KindDocument
		if u.Method == "sendPhoto" {
			kind = KindPhoto
		}
		all = append(all, numbered{u.MessageID, Message{Text: u.Caption, Files: []Attachment{{Kind: kind, Name: u.FileName, Size: len(u.Data)}}}})
	}
	slices.SortFunc(all, func(a, b numbered) int { return int(a.id - b.id) })
	out := make([]Message, len(all))
	for i, n := range all {
		out[i] = n.m
	}
	return out
}

// Taken reports whether the node has taken the last thing this peer did.
func (p *Telegram) Taken() bool { return p.fake.Confirmed() >= p.last.Load() }

type button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

// buttons reads an inline keyboard as the fake keeps it.
func buttons(keyboard string) []button {
	if keyboard == "" {
		return nil
	}
	var rows [][]button
	if err := json.Unmarshal([]byte(keyboard), &rows); err != nil {
		return nil
	}
	return slices.Concat(rows...)
}

// CheckLoopback refuses a Bot API URL that is not on this machine, by
// address, so the lab cannot reach a real bot.
func CheckLoopback(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("chatlab: Bot API URL: %w", err)
	}
	addr, err := netip.ParseAddr(u.Hostname())
	if err != nil || !addr.IsLoopback() {
		return fmt.Errorf("chatlab: Bot API URL %q is not a loopback address", rawURL)
	}
	return nil
}
