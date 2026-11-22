package settings

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// keyLinks is the links: section of the node's file.
const keyLinks = "links"

// localPrefix is the prefix of a local module's full name in the modules:
// section; a link names a module by the short name after it.
const localPrefix = "local/"

// linkEnd is one end of a link: a module's short name, a dot and a port.
var linkEnd = regexp.MustCompile(`^[a-z][a-z0-9_-]*\.[a-z][a-z0-9_]{0,31}$`)

// Link is one line of links: a connection from an output (or a calling
// port) of one module to an input (or a serving port) of another, each end
// written `<module>.<port>` (R-BUS-5). The file holds only the form; the
// ends, kinds and shapes are checked against the manifests by the links
// package.
type Link struct {
	// From is the producer or the caller.
	From string `yaml:"from"`
	// To is the receiver or the server.
	To string `yaml:"to"`
}

// String is the link as the node writes it in a message: `a.x → b.y`.
func (l Link) String() string { return l.From + " → " + l.To }

// check says what is wrong with the form of the link: the key and the
// reason; "" when nothing is. It never repeats a value.
func (l Link) check() (key, reason string) {
	for _, e := range []struct{ key, val string }{{"from", l.From}, {"to", l.To}} {
		if !linkEnd.MatchString(e.val) {
			return e.key, "must be <module>.<port>: a module's short name, a dot, and a port name"
		}
	}
	return "", ""
}

// Touches reports whether either end of the link is a port of the module,
// given by its short name or its full name (local/<name>).
func (l Link) Touches(module string) bool {
	short := strings.TrimPrefix(module, localPrefix)
	return endModule(l.From) == short || endModule(l.To) == short
}

// endModule is the module part of an end.
func endModule(end string) string {
	m, _, _ := strings.Cut(end, ".")
	return m
}

// checkLinks refuses a link of a bad form and a link written twice.
func checkLinks(ls []Link) error {
	seen := map[Link]bool{}
	for i, l := range ls {
		at := keyLinks + "[" + strconv.Itoa(i) + "]"
		if key, reason := l.check(); key != "" {
			return pathError(at+"."+key, reason)
		}
		if seen[l] {
			return pathError(at, "is listed twice")
		}
		seen[l] = true
	}
	return nil
}

// LinksFile is the links: section of the node's file as it is now: it reads
// the file on every call, so a change is seen at once (R-BUS-5).
type LinksFile struct {
	Path string
}

// Links reads the file and returns its links; a missing file has none. The
// whole file is checked, as at start.
func (f LinksFile) Links() ([]Link, error) {
	file, err := LoadOptional(f.Path, os.ReadFile)
	if err != nil {
		return nil, err
	}
	return file.Links, nil
}

// AddLink appends l to links, keeping the comments of the file. A link
// that is already there is left as it is. The meaning of the link is the
// caller's to check: the file holds only its form.
func (f LinksFile) AddLink(l Link) error {
	if key, reason := l.check(); key != "" {
		return fmt.Errorf("settings: link: %s %s", key, reason)
	}
	return editDoc(f.Path, func(doc *yaml.Node) error {
		seq, err := linksNode(doc)
		if err != nil {
			return err
		}
		if !slices.ContainsFunc(seq.Content, func(n *yaml.Node) bool { return linkOf(n) == l }) {
			seq.Content = append(seq.Content, linkNode(l))
		}
		return nil
	})
}

// RemoveLink drops l from links; a link that is not there is not an error.
func (f LinksFile) RemoveLink(l Link) error {
	return editDoc(f.Path, func(doc *yaml.Node) error {
		seq, err := linksNode(doc)
		if err != nil {
			return err
		}
		seq.Content = slices.DeleteFunc(seq.Content, func(n *yaml.Node) bool { return linkOf(n) == l })
		dropEmpty(doc, keyLinks)
		return nil
	})
}

// dropModuleLinks removes every link that touches the module, in the
// document of a removal.
func dropModuleLinks(doc *yaml.Node, module string) error {
	root, err := rootOf(doc)
	if err != nil {
		return err
	}
	v, _ := field(root, keyLinks)
	if v == nil || v.Kind != yaml.SequenceNode {
		return nil
	}
	v.Content = slices.DeleteFunc(v.Content, func(n *yaml.Node) bool { return linkOf(n).Touches(module) })
	dropEmpty(doc, keyLinks)
	return nil
}

// linkOf reads a links: item; a malformed one is the zero Link, which no
// real link equals and which touches no module.
func linkOf(n *yaml.Node) Link {
	var l Link
	if v, _ := field(n, "from"); v != nil {
		l.From = v.Value
	}
	if v, _ := field(n, "to"); v != nil {
		l.To = v.Value
	}
	return l
}

// linkNode is a links: item, a flow mapping on one line.
func linkNode(l Link) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: tagMap, Style: yaml.FlowStyle, Content: []*yaml.Node{
		plain("from"), {Kind: yaml.ScalarNode, Tag: tagStr, Value: l.From},
		plain("to"), {Kind: yaml.ScalarNode, Tag: tagStr, Value: l.To}}}
}

// linksNode finds the links: sequence, creating the document, the mapping
// and the key as needed.
func linksNode(doc *yaml.Node) (*yaml.Node, error) {
	root, err := rootOf(doc)
	if err != nil {
		return nil, err
	}
	seq, err := child(root, keyLinks, yaml.SequenceNode, tagSeq)
	if err != nil {
		return nil, err
	}
	seq.Style &^= yaml.FlowStyle
	return seq, nil
}
