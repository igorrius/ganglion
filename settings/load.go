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

// Load reads the file at path; a missing file is Default().
func Load(path string, read ReadFunc) (File, error) {
	src, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
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

// walk decodes n into v. A mapping into a struct goes key by key, so an error
// names the deepest path; anything else is decoded whole.
func walk(n *yaml.Node, path string, v reflect.Value) error {
	if v.Kind() == reflect.Pointer && v.Type().Elem().Kind() == reflect.Struct && n.Kind == yaml.MappingNode {
		v.Set(reflect.New(v.Type().Elem()))
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct || n.Kind != yaml.MappingNode {
		return leaf(n, path, v)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key := n.Content[i].Value
		field, ok := fieldByTag(v, key)
		if !ok {
			return pathError(path, fmt.Sprintf("unknown key %q", key))
		}
		if err := walk(n.Content[i+1], joinPath(path, key), field); err != nil {
			return err
		}
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

// check is the validation that is not about the type of a value.
func (f File) check() error {
	for _, c := range []struct {
		path, reason string
		ok           bool
	}{
		{"node.tz", "must be an IANA time zone such as Europe/Kyiv", validTZ(f.Node.TZ)},
		{"log.level", "must be debug, info, warn or error", slices.Contains([]string{"debug", "info", "warn", "error"}, f.Log.Level)},
		{"metrics.addr", `must be host:port or off`, validAddr(f.Metrics.Addr)},
		{"telegram.api_url", "must be an http or https address", validURL(f.Telegram.APIURL)},
		{"telegram.admin", "must be <transport>:user:<id>, such as telegram:user:123456", validAdmin(f.Telegram.Admin)},
		{"telegram.reply", "must be auto, always or off", slices.Contains([]string{auto, "always", off}, f.Telegram.Reply)},
		{"agent.search.mode", "must be auto or off", slices.Contains([]string{auto, off}, f.Agent.Search.Mode)},
		{"agent.workspace", "must not be empty", f.Agent.Workspace != ""},
	} {
		if !c.ok {
			return pathError(c.path, c.reason)
		}
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
