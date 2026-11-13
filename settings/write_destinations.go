package settings

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Errors of the destination writers; a tool decides on them with errors.Is.
var (
	// ErrDestinationUsed refuses removing a destination a module's line names.
	ErrDestinationUsed = errors.New("settings: a module's notify_to names the destination")
	// ErrNoDestination refuses a notify_to that names what is not under destinations.
	ErrNoDestination = errors.New("settings: no destination by that name")
)

// Keys of the node's file the destination writers touch.
const (
	keyDestinations = "destinations"
	keyNotifyTo     = "notify_to"
	keyName         = "name"
	keyConv         = "conv"
)

// DestinationsFile is the destinations: section of the node's file and
// the notify_to of its modules as they are now: it reads the file on
// every call, so a change is seen at once (R-CFG-12).
type DestinationsFile struct {
	Path string
}

// Destinations reads the file and returns destinations; a missing file
// has none. The whole file is checked, as at start.
func (f DestinationsFile) Destinations() ([]Destination, error) {
	file, err := LoadOptional(f.Path, os.ReadFile)
	if err != nil {
		return nil, err
	}
	return file.Destinations, nil
}

// SetDestination writes d as the line of d.Name under destinations,
// adding it at the end when it is missing.
func (f DestinationsFile) SetDestination(d Destination) error {
	if key, reason := d.check(); key != "" {
		return fmt.Errorf("settings: destination: %s %s", key, reason)
	}
	return editDoc(f.Path, func(doc *yaml.Node) error {
		seq, err := destinationsNode(doc)
		if err != nil {
			return err
		}
		item := namedLine(seq, d.Name)
		if item == nil {
			item = &yaml.Node{Kind: yaml.MappingNode, Tag: tagMap}
			seq.Content = append(seq.Content, item)
		}
		setScalar(item, keyName, d.Name, tagStr)
		setScalar(item, keyConv, d.Conv, tagStr)
		return nil
	})
}

// RemoveDestination drops the line of name from destinations. It fails
// with ErrDestinationUsed, naming the modules, while a module's notify_to
// names it; a name that is not listed is not an error.
func (f DestinationsFile) RemoveDestination(name string) error {
	return editDoc(f.Path, func(doc *yaml.Node) error {
		mods, err := modulesNode(doc)
		if err != nil {
			return err
		}
		if users := usersOf(mods, name); len(users) > 0 {
			return fmt.Errorf("%w: %s", ErrDestinationUsed, strings.Join(users, ", "))
		}
		seq, err := destinationsNode(doc)
		if err != nil {
			return err
		}
		line := namedLine(seq, name)
		seq.Content = slices.DeleteFunc(seq.Content, func(n *yaml.Node) bool { return n == line })
		return nil
	})
}

// SetNotifyTo makes the notify_to of module's line equal to names, each
// DestinationOwner or a name under destinations; no names drops the key,
// so the module writes to its owner. The line is added when it is
// missing, and the rest of it stays. It fails with ErrNoDestination for a
// name that is not there.
func (f DestinationsFile) SetNotifyTo(module string, names []string) error {
	return editDoc(f.Path, func(doc *yaml.Node) error {
		known := map[string]bool{DestinationOwner: true}
		dests, err := destinationsNode(doc)
		if err != nil {
			return err
		}
		for _, item := range dests.Content {
			if v, _ := field(item, keyName); v != nil {
				known[v.Value] = true
			}
		}
		if reason := notifyToReason(names, known); reason != "" {
			return fmt.Errorf("%w: notify_to %s", ErrNoDestination, reason)
		}
		mods, err := modulesNode(doc)
		if err != nil {
			return err
		}
		setNotifyTo(lineOf(mods, module), names)
		dropEmpty(doc, keyDestinations)
		return nil
	})
}

// setNotifyTo writes names as the flow list notify_to of a module's line,
// or removes the key when there are none.
func setNotifyTo(item *yaml.Node, names []string) {
	v, at := field(item, keyNotifyTo)
	if len(names) == 0 {
		if v != nil {
			item.Content = slices.Delete(item.Content, at, at+2)
		}
		return
	}
	list := &yaml.Node{Kind: yaml.SequenceNode, Tag: tagSeq, Style: yaml.FlowStyle}
	for _, n := range names {
		list.Content = append(list.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Value: n})
	}
	if v != nil {
		*v = *list
		return
	}
	item.Content = append(item.Content, plain(keyNotifyTo), list)
}

// usersOf lists the modules whose notify_to names the destination.
func usersOf(mods *yaml.Node, name string) []string {
	var out []string
	for _, item := range mods.Content {
		list, _ := field(item, keyNotifyTo)
		if list == nil {
			continue
		}
		for _, n := range list.Content {
			if n.Value != name {
				continue
			}
			if v, _ := field(item, keyName); v != nil {
				out = append(out, v.Value)
			}
		}
	}
	return out
}

// destinationsNode finds the destinations: sequence, creating the
// document, the mapping and the key as needed.
func destinationsNode(doc *yaml.Node) (*yaml.Node, error) {
	root, err := rootOf(doc)
	if err != nil {
		return nil, err
	}
	seq, err := child(root, keyDestinations, yaml.SequenceNode, tagSeq)
	if err != nil {
		return nil, err
	}
	seq.Style &^= yaml.FlowStyle // a flow list would put every destination on one line
	return seq, nil
}

// dropEmpty removes the top-level key when a writer that only read it
// left it an empty list it had to create.
func dropEmpty(doc *yaml.Node, key string) {
	root := doc.Content[0]
	if v, at := field(root, key); v != nil && v.Kind == yaml.SequenceNode && len(v.Content) == 0 {
		root.Content = slices.Delete(root.Content, at, at+2)
	}
}

// rootOf is the document's top-level mapping, created when the file is
// empty.
func rootOf(doc *yaml.Node) (*yaml.Node, error) {
	if doc.Kind == 0 {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: tagMap}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: the top level is not a mapping", FileName)
	}
	return root, nil
}

// namedLine is the line called name in the sequence; nil when there is
// none.
func namedLine(seq *yaml.Node, name string) *yaml.Node {
	for _, item := range seq.Content {
		if v, _ := field(item, keyName); v != nil && v.Value == name {
			return item
		}
	}
	return nil
}
