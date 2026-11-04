// Package chatlab tests the agent by talking to it (ADR-0048): a
// channel-neutral peer, scenarios as data, facts from what the node did, a
// judge for what a reply means, and a report that always shows n of n.
package chatlab

import (
	"context"
	"errors"
)

// Peer is the person's side of a conversation, on any channel. Waiting until
// the node is quiet is the node's side, so it is not here.
type Peer interface {
	Say(ctx context.Context, text string) error
	SendFile(ctx context.Context, f File) error
	// Press presses the newest button showing with label; ErrNoButton when
	// none shows, so a scenario cannot pass by pressing nothing.
	Press(ctx context.Context, label string) error
	// Messages is everything shown to the person, oldest first, edits
	// applied. A new message always comes after the ones before it.
	Messages() []Message
}

// Kinds of file.
const (
	KindDocument = "document"
	KindPhoto    = "photo"
)

// File is a file the person sends; its content does not matter to a node
// that cannot open files yet.
type File struct {
	Kind    string `yaml:"kind"` // KindDocument or KindPhoto
	Name    string `yaml:"name"`
	Type    string `yaml:"type"` // MIME type
	Size    int64  `yaml:"size"`
	Caption string `yaml:"caption"`
}

// Attachment is a file shown with a message.
type Attachment struct {
	Kind string // KindPhoto or KindDocument
	Name string
	Size int
}

// Message is one message as the person sees it now.
type Message struct {
	Text     string
	Buttons  []string // labels, in order
	Files    []Attachment
	Replaced bool // edited after it was first shown
}

// ErrNoButton is returned by Press when no button with the label shows.
var ErrNoButton = errors.New("chatlab: no such button is showing")
