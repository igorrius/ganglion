package settings

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	"go.yaml.in/yaml/v3"
)

// YAML tags of the nodes the writers make.
const (
	tagMap  = "!!map"
	tagSeq  = "!!seq"
	tagStr  = "!!str"
	tagInt  = "!!int"
	tagBool = "!!bool"
	tagNull = "!!null"
)

// SetModule writes m as the line of m.Name under modules:, adding it at the
// end when it is missing. It edits the YAML node tree, so the comments and
// the order of everything else stay (a config that did not change keeps its
// own comments too), and replaces the file through a temporary file in the
// same directory and a rename: a failed write leaves the old file as it was.
func SetModule(path string, m Module) error {
	return edit(path, func(seq *yaml.Node) error {
		item := lineOf(seq, m.Name)
		if err := setConfig(item, m.Config); err != nil {
			return err
		}
		setOff(item, m.Off)
		return nil
	})
}

// lineOf is the line of the module called name in the sequence, added at
// the end when it is missing.
func lineOf(seq *yaml.Node, name string) *yaml.Node {
	if i := indexOf(seq, name); i >= 0 {
		return seq.Content[i]
	}
	item := &yaml.Node{Kind: yaml.MappingNode, Tag: tagMap, Content: []*yaml.Node{
		plain(keyName), {Kind: yaml.ScalarNode, Tag: tagStr, Value: name}}}
	seq.Content = append(seq.Content, item)
	return item
}

// plain is a key written without quotes (a node with no tag is never
// quoted, while the string "off" would be).
func plain(key string) *yaml.Node { return &yaml.Node{Kind: yaml.ScalarNode, Value: key} }

// field is the value node of key in the mapping and its position, or nil.
func field(item *yaml.Node, key string) (*yaml.Node, int) {
	for j := 0; j+1 < len(item.Content); j += 2 {
		if item.Content[j].Value == key {
			return item.Content[j+1], j
		}
	}
	return nil, -1
}

// setConfig makes the line's config equal to cfg; an equal one is left as
// it is, comments included, and an empty one is dropped.
func setConfig(item *yaml.Node, cfg map[string]any) error {
	old, j := field(item, "config")
	if len(cfg) == 0 {
		if j >= 0 {
			item.Content = append(item.Content[:j], item.Content[j+2:]...)
		}
		return nil
	}
	var nv yaml.Node
	if err := nv.Encode(cfg); err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	if old == nil {
		item.Content = append(item.Content, plain("config"), &nv)
		return nil
	}
	var cur map[string]any
	var want map[string]any
	if old.Decode(&cur) == nil && nv.Decode(&want) == nil && reflect.DeepEqual(cur, want) {
		return nil
	}
	nv.HeadComment, nv.LineComment, nv.FootComment = old.HeadComment, old.LineComment, old.FootComment
	*old = nv
	return nil
}

// setOff writes off: true or removes the key.
func setOff(item *yaml.Node, off bool) {
	v, j := field(item, "off")
	switch {
	case !off && j >= 0:
		item.Content = append(item.Content[:j], item.Content[j+2:]...)
	case off && j >= 0:
		v.Kind, v.Tag, v.Value, v.Style = yaml.ScalarNode, tagBool, "true", 0
	case off:
		item.Content = append(item.Content, plain("off"), &yaml.Node{Kind: yaml.ScalarNode, Tag: tagBool, Value: "true"})
	}
}

// ModulesFile is the modules: section of the node's file as it is now: it
// reads the file on every call, so a write by /modules or by the module
// supervisor is seen at once (M4b).
type ModulesFile struct {
	Path string
}

// Modules reads the file and returns its modules: section; a missing file
// has none. The whole file is checked, as at start.
func (f ModulesFile) Modules() ([]Module, error) {
	file, err := LoadOptional(f.Path, os.ReadFile)
	if err != nil {
		return nil, err
	}
	return file.Modules, nil
}

// SetModule is SetModule on this file.
func (f ModulesFile) SetModule(m Module) error { return SetModule(f.Path, m) }

// SetOff writes or removes off: true on the line of name, adding the line
// when it is missing, and leaves its config as it is. why is the node's
// reason, written beside off: true as a comment ("crashloop"); turning a
// module on removes both (ruling 8 of the M4b plan).
func (f ModulesFile) SetOff(name string, off bool, why string) error {
	return edit(f.Path, func(seq *yaml.Node) error {
		item := lineOf(seq, name)
		setOff(item, off)
		if v, _ := field(item, "off"); v != nil {
			v.LineComment = why
		}
		return nil
	})
}

// SetNotifyTo is DestinationsFile.SetNotifyTo on this file.
func (f ModulesFile) SetNotifyTo(module string, names []string) error {
	return DestinationsFile(f).SetNotifyTo(module, names)
}

// RemoveModule is RemoveModule on this file.
func (f ModulesFile) RemoveModule(name string) error { return RemoveModule(f.Path, name) }

// RemoveModule drops the line of name from modules:; a name that is not
// there is not an error.
func RemoveModule(path, name string) error {
	return edit(path, func(seq *yaml.Node) error {
		if i := indexOf(seq, name); i >= 0 {
			seq.Content = append(seq.Content[:i], seq.Content[i+1:]...)
		}
		return nil
	})
}

// indexOf is the position of the module called name in the sequence, or -1.
func indexOf(seq *yaml.Node, name string) int {
	for i, it := range seq.Content {
		if it.Kind != yaml.MappingNode {
			continue
		}
		for j := 0; j+1 < len(it.Content); j += 2 {
			if it.Content[j].Value == keyName && it.Content[j+1].Value == name {
				return i
			}
		}
	}
	return -1
}

// writeMu serialises the writers of this process: a read-modify-rename that
// interleaved with another would lose one of the edits (a background
// off-switch against a chat command's write). It guards writers in one
// process only; another process writing the file is not covered.
var writeMu sync.Mutex

// editDoc reads the file, hands the document to change and writes the
// result back. A missing file is an empty one. A result the node would not
// start with is refused and the file is left as it was (R-CFG-15).
func editDoc(path string, change func(doc *yaml.Node) error) error {
	writeMu.Lock()
	defer writeMu.Unlock()
	src, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("settings: %w", err)
	}
	out, err := editSrc(src, change)
	if err != nil {
		return err
	}
	return replaceFile(path, out)
}

// editSrc hands the document in src to change and returns the result,
// checked as the start checks it.
func editSrc(src []byte, change func(doc *yaml.Node) error) ([]byte, error) {
	var doc yaml.Node
	if len(bytes.TrimSpace(src)) > 0 {
		if err := yaml.Unmarshal(src, &doc); err != nil {
			return nil, fmt.Errorf("%s: %s", FileName, syntaxReason(err))
		}
	}
	if err := change(&doc); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	enc := yaml.NewEncoder(&out)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("settings: %w", err)
	}
	if _, err := Parse(out.Bytes()); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// edit is editDoc on the modules: sequence.
func edit(path string, change func(seq *yaml.Node) error) error {
	return editDoc(path, func(doc *yaml.Node) error {
		seq, err := modulesNode(doc)
		if err != nil {
			return err
		}
		return change(seq)
	})
}

// modulesNode finds the modules: sequence of the document, creating the
// document, the mapping and the key as needed.
func modulesNode(doc *yaml.Node) (*yaml.Node, error) {
	if doc.Kind == 0 {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{Kind: yaml.MappingNode, Tag: tagMap}}
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s: the top level is not a mapping", FileName)
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != keyModules {
			continue
		}
		v := root.Content[i+1]
		if v.Kind == yaml.ScalarNode && v.Tag == tagNull { // "modules:" with every item commented out
			*v = yaml.Node{Kind: yaml.SequenceNode, Tag: tagSeq, HeadComment: v.HeadComment, LineComment: v.LineComment}
		}
		if v.Kind != yaml.SequenceNode {
			return nil, fmt.Errorf("%s: modules is not a list", FileName)
		}
		v.Style &^= yaml.FlowStyle // a flow list would put every module on one line
		return v, nil
	}
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: tagSeq}
	root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: tagStr, Value: keyModules}, seq)
	return seq, nil
}

// replaceFile writes data to a temporary file beside path and renames it
// over path, keeping the file's mode.
func replaceFile(path string, data []byte) error {
	mode := fs.FileMode(0o600)
	if st, err := os.Stat(path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ganglion-*.tmp")
	if err != nil {
		return fmt.Errorf("settings: %w", err)
	}
	name := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return fmt.Errorf("settings: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		return fail(err)
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("settings: %w", err)
	}
	return nil
}
