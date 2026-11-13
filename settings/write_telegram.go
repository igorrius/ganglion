package settings

import (
	"fmt"
	"os"
	"strconv"

	"go.yaml.in/yaml/v3"
)

// TelegramFile is the telegram: section of the node's file as it is now:
// it reads the file on every call, so a group the admin allowed a moment
// ago is seen at once (R-DLV-17).
type TelegramFile struct {
	Path string
}

// Groups reads the file and returns telegram.groups; a missing file has
// none. The whole file is checked, as at start.
func (f TelegramFile) Groups() ([]Group, error) {
	file, err := LoadOptional(f.Path, os.ReadFile)
	if err != nil {
		return nil, err
	}
	return file.Telegram.Groups, nil
}

// SetGroup writes g as the line of g.Chat under telegram.groups, adding it
// at the end when it is missing. The rest of the file keeps its comments
// and order.
func (f TelegramFile) SetGroup(g Group) error {
	if key, reason := g.check(); key != "" {
		return fmt.Errorf("settings: group %d: %s %s", g.Chat, key, reason)
	}
	return editDoc(f.Path, func(doc *yaml.Node) error {
		seq, err := groupsNode(doc)
		if err != nil {
			return err
		}
		item := groupLine(seq, g.Chat)
		if item == nil {
			item = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			seq.Content = append(seq.Content, item)
		}
		setScalar(item, "chat", strconv.FormatInt(g.Chat, 10), "!!int")
		setScalar(item, "name", g.Name, "!!str")
		setScalar(item, "role", g.Role, "!!str")
		return nil
	})
}

// RemoveGroup drops the line of chat from telegram.groups; a chat that is
// not listed is not an error.
func (f TelegramFile) RemoveGroup(chat int64) error {
	return editDoc(f.Path, func(doc *yaml.Node) error {
		seq, err := groupsNode(doc)
		if err != nil {
			return err
		}
		line := groupLine(seq, chat)
		for i, item := range seq.Content {
			if item == line {
				seq.Content = append(seq.Content[:i], seq.Content[i+1:]...)
				break
			}
		}
		return nil
	})
}

// groupsNode finds telegram.groups, creating the document, the mapping
// and the keys as needed.
func groupsNode(doc *yaml.Node) (*yaml.Node, error) {
	if doc.Kind == 0 {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: "!!map"}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: the top level is not a mapping", FileName)
	}
	tg, err := child(root, "telegram", yaml.MappingNode, "!!map")
	if err != nil {
		return nil, err
	}
	seq, err := child(tg, "groups", yaml.SequenceNode, "!!seq")
	if err != nil {
		return nil, err
	}
	seq.Style &^= yaml.FlowStyle // a flow list would put every group on one line
	return seq, nil
}

// child is the value of key in mapping m, added empty when it is missing
// or null ("groups:" with every line commented out).
func child(m *yaml.Node, key string, kind yaml.Kind, tag string) (*yaml.Node, error) {
	if v, _ := field(m, key); v != nil {
		if v.Kind == yaml.ScalarNode && v.Tag == "!!null" {
			*v = yaml.Node{Kind: kind, Tag: tag, HeadComment: v.HeadComment, LineComment: v.LineComment}
		}
		if v.Kind != kind {
			return nil, fmt.Errorf("%s: %s has the wrong shape", FileName, key)
		}
		return v, nil
	}
	v := &yaml.Node{Kind: kind, Tag: tag}
	m.Content = append(m.Content, plain(key), v)
	return v, nil
}

// groupLine is the line of chat in the sequence; nil when there is none.
func groupLine(seq *yaml.Node, chat int64) *yaml.Node {
	want := strconv.FormatInt(chat, 10)
	for _, item := range seq.Content {
		if v, _ := field(item, "chat"); v != nil && v.Value == want {
			return item
		}
	}
	return nil
}

// setScalar sets key of the mapping item to value, adding the key when it
// is missing.
func setScalar(item *yaml.Node, key, value, tag string) {
	if v, _ := field(item, key); v != nil {
		v.Kind, v.Tag, v.Value, v.Style = yaml.ScalarNode, tag, value, 0
		return
	}
	item.Content = append(item.Content, plain(key), &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
}
