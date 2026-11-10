package settings

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// FileName is how errors name the file, whatever its path.
const FileName = "ganglion.yaml"

// ReadFunc reads a file, as os.ReadFile does.
type ReadFunc func(path string) ([]byte, error)

// Load reads the file at a path the operator named (--config or
// GANGLION_CONFIG); a missing file is an error, so a typo in the path does
// not start a node on defaults (R-CFG-1).
func Load(path string, read ReadFunc) (File, error) {
	src, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return File{}, pathError(path, "no such file")
	}
	return parseRead(src, err)
}

// LoadOptional reads the file at the default path, beside the database; a
// missing file there is Default().
func LoadOptional(path string, read ReadFunc) (File, error) {
	src, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	return parseRead(src, err)
}

// parseRead parses what a read returned.
func parseRead(src []byte, err error) (File, error) {
	if err != nil {
		return File{}, fmt.Errorf("%s: %w", FileName, err)
	}
	return Parse(src)
}

// Parse reads the file's bytes over Default() and checks every value. An
// error names the path of the key it is about and never repeats a value, so
// a secret written by mistake does not reach a log.
func Parse(src []byte) (File, error) {
	f := Default()
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return File{}, fmt.Errorf("%s: %s", FileName, syntaxReason(err))
	}
	if len(root.Content) > 0 {
		if err := walk(root.Content[0], "", reflect.ValueOf(&f).Elem()); err != nil {
			return File{}, err
		}
	}
	return f, f.check()
}

// lineRE finds the line number in a YAML syntax error.
var lineRE = regexp.MustCompile(`line \d+`)

// syntaxReason is a YAML syntax error cut down to its line: the rest of the
// message may quote the text, and a secret must not reach a log.
func syntaxReason(err error) string {
	if l := lineRE.FindString(err.Error()); l != "" {
		return "not valid YAML at " + l
	}
	return "not valid YAML"
}

// decodeStrict decodes n into v refusing unknown keys, so a custom
// UnmarshalYAML stays strict inside.
func decodeStrict(n *yaml.Node, v any) error {
	var buf bytes.Buffer
	if err := yaml.NewEncoder(&buf).Encode(n); err != nil {
		return err
	}
	dec := yaml.NewDecoder(&buf)
	dec.KnownFields(true)
	return dec.Decode(v)
}

// walk decodes n into v. A mapping into a struct goes key by key, a
// sequence into a slice item by item and a mapping into a map entry by
// entry, so an error names the deepest path, such as
// models.purposes.chat[1].model; anything else is decoded whole. A null
// (a key whose children are all commented out) keeps the default.
func walk(n *yaml.Node, path string, v reflect.Value) error {
	if n.Kind == yaml.ScalarNode && n.Tag == "!!null" {
		return nil
	}
	if v.Kind() == reflect.Pointer && v.Type().Elem().Kind() == reflect.Struct && n.Kind == yaml.MappingNode {
		v.Set(reflect.New(v.Type().Elem()))
		v = v.Elem()
	}
	switch {
	case v.Kind() == reflect.Struct && n.Kind == yaml.MappingNode:
		return walkStruct(n, path, v)
	case v.Kind() == reflect.Slice && n.Kind == yaml.SequenceNode:
		return walkSlice(n, path, v)
	case v.Kind() == reflect.Map && v.Type().Key().Kind() == reflect.String && n.Kind == yaml.MappingNode:
		return walkMap(n, path, v)
	}
	return leaf(n, path, v)
}

// walkStruct decodes a mapping into the struct v key by key; an unknown,
// retired or repeated key is an error.
func walkStruct(n *yaml.Node, path string, v reflect.Value) error {
	if err := noTwice(n, path); err != nil {
		return err
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		field, ok := fieldByTag(v, key)
		if hint, retired := retiredKey(v.Type(), key); !ok && retired {
			return pathError(path, key+" is no longer read: "+hint)
		}
		if !ok {
			return pathError(path, fmt.Sprintf("unknown key %q", key))
		}
		if err := walk(n.Content[i+1], joinPath(path, key), field); err != nil {
			return err
		}
	}
	return nil
}

// walkSlice replaces the slice v with the sequence's items, each at
// path[i].
func walkSlice(n *yaml.Node, path string, v reflect.Value) error {
	s := reflect.MakeSlice(v.Type(), len(n.Content), len(n.Content))
	for i, item := range n.Content {
		if err := walk(item, fmt.Sprintf("%s[%d]", path, i), s.Index(i)); err != nil {
			return err
		}
	}
	v.Set(s)
	return nil
}

// walkMap replaces the map v with the mapping's entries, each at
// path.key.
func walkMap(n *yaml.Node, path string, v reflect.Value) error {
	if err := noTwice(n, path); err != nil {
		return err
	}
	m := reflect.MakeMapWithSize(v.Type(), len(n.Content)/2)
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		val := reflect.New(v.Type().Elem()).Elem()
		if err := walk(n.Content[i+1], joinPath(path, key), val); err != nil {
			return err
		}
		m.SetMapIndex(reflect.ValueOf(key).Convert(v.Type().Key()), val)
	}
	v.Set(m)
	return nil
}

// noTwice refuses a mapping that has a key twice: YAML would keep the last
// one, and a second models: block pasted below the first would hide it.
func noTwice(n *yaml.Node, path string) error {
	seen := map[string]bool{}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		if seen[key] {
			return pathError(path, fmt.Sprintf("key %q twice", key))
		}
		seen[key] = true
	}
	return nil
}

// leaf decodes a value that is not walked further.
func leaf(n *yaml.Node, path string, v reflect.Value) error {
	err := decodeStrict(n, v.Addr().Interface())
	if err == nil {
		return nil
	}
	if _, ok := errors.AsType[*yaml.TypeError](err); ok {
		return pathError(path, "has the wrong kind of value")
	}
	return pathError(path, err.Error())
}

// fieldByTag finds the struct field whose yaml name is key.
func fieldByTag(v reflect.Value, key string) (reflect.Value, bool) {
	t := v.Type()
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("yaml"), ",")
		if name == key {
			return v.Field(i), true
		}
	}
	return reflect.Value{}, false
}

func joinPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

// pathError is "ganglion.yaml: <path>: <reason>"; the root has no path.
func pathError(path, reason string) error {
	if path == "" {
		return fmt.Errorf("%s: %s", FileName, reason)
	}
	return fmt.Errorf("%s: %s: %s", FileName, path, reason)
}

var principalRE = regexp.MustCompile(`^[a-z][a-z0-9]*:user:[0-9A-Za-z_.-]+$`)

// validAdmin accepts "" or <transport>:user:<id>.
func validAdmin(v string) bool { return v == "" || principalRE.MatchString(v) }

// The paths of keys that more than one check names.
const (
	keyTZ          = "node.tz"
	keyAPIURL      = "telegram.api_url"
	keyCallTimeout = "agent.call_timeout"
	keyTurnTimeout = "agent.turn_timeout"
)

// check is the validation that is not about the type of a value.
func (f File) check() error {
	for _, c := range []struct {
		path, reason string
		ok           bool
	}{
		{keyTZ, "must name a time zone", f.Node.TZ != ""},
		{keyTZ, "must be an IANA time zone such as Europe/Kyiv", validTZ(f.Node.TZ)},
		{"log.level", "must be debug, info, warn or error", slices.Contains([]string{"debug", "info", "warn", "error"}, f.Log.Level)},
		{"metrics.addr", `must be host:port or off`, validAddr(f.Metrics.Addr)},
		{keyAPIURL, "must be an http or https address", validURL(f.Telegram.APIURL)},
		{keyAPIURL, "must not carry credentials; use token: env:NAME", !hasCredentials(f.Telegram.APIURL)},
		{"telegram.admin", "must be <transport>:user:<id>, such as telegram:user:123456", validAdmin(f.Telegram.Admin)},
		{"telegram.reply", "must be auto, always or off", slices.Contains([]string{auto, "always", off}, f.Telegram.Reply)},
		{"agent.search.mode", "must be auto or off", slices.Contains([]string{auto, off}, f.Agent.Search.Mode)},
		{"agent.workspace", "must not be empty", f.Agent.Workspace != ""},
		{keyCallTimeout, "must be above 0", f.Agent.CallTimeout > 0},
		{keyTurnTimeout, "must be above 0", f.Agent.TurnTimeout > 0},
		{keyCallTimeout, "must not be longer than " + keyTurnTimeout, f.Agent.CallTimeout <= f.Agent.TurnTimeout},
		{keyTurnTimeout, "plus " + HandlerSlack.String() + " for the node's handler must stay below the queue's lease of " +
			QueueLease.String(), f.Agent.HandlerTimeout() < QueueLease},
	} {
		if !c.ok {
			return pathError(c.path, c.reason)
		}
	}
	if f.Models != nil {
		return f.Models.check("models")
	}
	return nil
}

func validTZ(tz string) bool {
	_, err := time.LoadLocation(tz)
	return err == nil
}

func validAddr(a string) bool {
	if a == off {
		return true
	}
	_, _, err := net.SplitHostPort(a)
	return err == nil
}

func validURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// hasCredentials reports whether an address carries a user or a password:
// a secret is never written in the file.
func hasCredentials(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.User != nil
}
