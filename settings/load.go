package settings

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/netip"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/bus"
	"go.yaml.in/yaml/v3"
)

// schemeHTTPS is the URL scheme the public and the backup addresses need.
const schemeHTTPS = "https"

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
	if f.Backup != nil && f.Backup.At == "" {
		f.Backup.At = DefaultBackupAt
	}
	f.MCP.fill()
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
	keyPublicURL   = "http.public_url"
	keyUpdates     = "telegram.updates"
	keyCallTimeout = "agent.call_timeout"
	keyTurnTimeout = "agent.turn_timeout"
	keyMaxSteps    = "agent.max_steps"
)

// check is the validation that is not about the type of a value.
func (f File) check() error {
	for _, c := range []struct {
		path, reason string
		ok           bool
	}{
		{keyTZ, "must name a time zone", f.Node.TZ != ""},
		{keyTZ, "must be an IANA time zone such as Europe/Kyiv", validTZ(f.Node.TZ)},
		{pathLogLevel, "must be debug, info, warn or error", slices.Contains([]string{"debug", "info", "warn", "error"}, f.Log.Level)},
		{"metrics.addr", `must be host:port or off`, validAddr(f.Metrics.Addr)},
		{"http.addr", `must be host:port or off`, validAddr(f.HTTP.Addr)},
		{"http.local_addr", `must be off or a loopback address: 127.0.0.1, [::1] or localhost, with a port`, validLocalAddr(f.HTTP.LocalAddr)},
		{keyPublicURL, "must be an https address with no path, on no port or on 443, 80, 88 or 8443", validPublicURL(f.HTTP.PublicURL)},
		{keyUpdates, "must be polling or webhook", slices.Contains([]string{UpdatesPolling, UpdatesWebhook}, f.Telegram.Updates)},
		{keyUpdates, "webhook needs the public listener: set http.addr", f.Telegram.Updates != UpdatesWebhook || f.HTTP.On()},
		{keyUpdates, "webhook needs " + keyPublicURL + ", the address Telegram calls", f.Telegram.Updates != UpdatesWebhook || f.HTTP.PublicURL != ""},
		{keyAPIURL, "must be an http or https address", validURL(f.Telegram.APIURL)},
		{keyAPIURL, "must not carry credentials; use token: env:NAME", !hasCredentials(f.Telegram.APIURL)},
		{"telegram.admin", "must be <transport>:user:<id>, such as telegram:user:123456", validAdmin(f.Telegram.Admin)},
		{pathTelegramReply, "must be auto, always or off", slices.Contains([]string{auto, "always", off}, f.Telegram.Reply)},
		{pathSearchMode, "must be auto or off", slices.Contains([]string{auto, off}, f.Agent.Search.Mode)},
		{"agent.workspace", "must not be empty", f.Agent.Workspace != ""},
		{keyCallTimeout, reasonAboveZero, f.Agent.CallTimeout > 0},
		{keyTurnTimeout, reasonAboveZero, f.Agent.TurnTimeout > 0},
		{keyCallTimeout, "must not be longer than " + keyTurnTimeout, f.Agent.CallTimeout <= f.Agent.TurnTimeout},
		{keyMaxSteps, "must be between 1 and 100", f.Agent.MaxSteps >= 1 && f.Agent.MaxSteps <= 100},
		{"memory.review.quiet", "must be above 0 while the review is enabled", !f.Memory.Review.Enabled || f.Memory.Review.Quiet > 0},
		{keyTurnTimeout, "plus " + HandlerSlack.String() + " for the node's handler must stay below the queue's lease of " +
			QueueLease.String(), f.Agent.HandlerTimeout() < QueueLease},
	} {
		if !c.ok {
			return pathError(c.path, c.reason)
		}
	}
	if err := checkGroups(f.Telegram.Groups); err != nil {
		return err
	}
	if err := f.checkModules(); err != nil {
		return err
	}
	if err := checkLinks(f.Links); err != nil {
		return err
	}
	if err := f.MCP.check(f.Modules); err != nil {
		return err
	}
	if f.Backup != nil {
		if err := f.Backup.check(); err != nil {
			return err
		}
	}
	if err := f.Edge.check(); err != nil {
		return err
	}
	if err := f.Bus.check(); err != nil {
		return err
	}
	if f.Models != nil {
		return f.Models.check("models")
	}
	return nil
}

// check refuses a backup section that cannot run. An error names the key
// and never repeats a value.
func (b Backup) check() error {
	for _, c := range []struct {
		key, reason string
		ok          bool
	}{
		{"endpoint", "must be an https address with a host and no path, query or credentials, such as https://s3.example.com", validBackupEndpoint(b.Endpoint)},
		{"region", "must name the bucket's signing region", b.Region != ""},
		{"bucket", "must be the bucket's name", b.Bucket != "" && !strings.ContainsAny(b.Bucket, "/?# ")},
		{"prefix", "must not start with a slash or hold \"..\"", !strings.HasPrefix(b.Prefix, "/") && !slices.Contains(strings.Split(b.Prefix, "/"), "..")},
		{"access_key_id", "must be the access key's id", b.AccessKeyID != ""},
		{"secret_access_key", "must be env:NAME or store:NAME: the secret key is never written in the file", b.SecretAccessKey != ""},
		{"at", "must be a time of day as HH:MM, such as 03:00", validClock(b.At)},
	} {
		if !c.ok {
			return pathError("backup."+c.key, c.reason)
		}
	}
	return nil
}

// Secret reads the secret key the section names through src. A key that is
// not set is an error that says how to set it, so a node never starts
// believing its backups run.
func (b Backup) Secret(src Sources) (string, error) {
	v, err := b.SecretAccessKey.Resolve(src)
	if err != nil {
		return "", err
	}
	if v == "" {
		return "", fmt.Errorf("%s: backup.secret_access_key: %s is not set; %s", FileName, b.SecretAccessKey, b.SecretAccessKey.HowToSet())
	}
	return v, nil
}

// validBackupEndpoint accepts a bare https address: a host, an optional
// port and nothing else.
func validBackupEndpoint(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == schemeHTTPS && u.Hostname() != "" && u.User == nil && u.RawQuery == "" &&
		u.Fragment == "" && (u.Path == "" || u.Path == "/")
}

// validClock accepts HH:MM, 00:00 to 23:59.
func validClock(s string) bool {
	_, err := time.Parse("15:04", s)
	return err == nil && len(s) == len("15:04")
}

// checkGroups refuses a bad line of telegram.groups and a chat listed twice.
func checkGroups(gs []Group) error {
	seen := map[int64]bool{}
	for i, g := range gs {
		at := "telegram.groups[" + strconv.Itoa(i) + "]."
		if key, reason := g.check(); key != "" {
			return pathError(at+key, reason)
		}
		if seen[g.Chat] {
			return pathError(at+"chat", "is listed twice")
		}
		seen[g.Chat] = true
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

// validLocalAddr accepts "", off, or host:port with a loopback host: the
// listener of modules' pages never binds another interface (R-HTTP-3). A
// port 0 is refused: the owner opens the page at a known address.
func validLocalAddr(a string) bool {
	if a == "" || a == off {
		return true
	}
	host, port, err := net.SplitHostPort(a)
	if err != nil {
		return false
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip, err := netip.ParseAddr(host)
	return err == nil && ip.IsLoopback() && ip.Zone() == ""
}

func validURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == schemeHTTPS) && u.Host != ""
}

// validPublicURL accepts "" or an address Telegram can call a webhook at:
// https, a host, no credentials, no path, query or fragment, and no port
// or one of the four Telegram allows.
func validPublicURL(s string) bool {
	if s == "" {
		return true
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != schemeHTTPS || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return (u.Path == "" || u.Path == "/") && slices.Contains([]string{"", "443", "80", "88", "8443"}, u.Port())
}

// hasCredentials reports whether an address carries a user or a password:
// a secret is never written in the file.
func hasCredentials(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.User != nil
}

// checkModules refuses a module line's notify_to that names no destination
// and a node that cannot name an edge.
func (f File) checkModules() error {
	if err := checkDestinations(f.Destinations, f.Modules); err != nil {
		return err
	}
	return checkModuleNodes(f.Modules)
}

// checkModuleNodes refuses a module's node that cannot name an edge.
func checkModuleNodes(mods []Module) error {
	for i, m := range mods {
		if m.Node != "" && m.Node != bus.CoreNode && bus.CheckEdgeName(m.Node) != nil {
			return pathError("modules["+strconv.Itoa(i)+"].node", "must be an edge's name: one word, no dots, spaces, * or >")
		}
	}
	return nil
}

// checkDestinations refuses a bad line of destinations, a name listed
// twice, and a module's notify_to that names what is not there.
func checkDestinations(ds []Destination, mods []Module) error {
	known := map[string]bool{DestinationOwner: true}
	for i, d := range ds {
		at := "destinations[" + strconv.Itoa(i) + "]."
		if key, reason := d.check(); key != "" {
			return pathError(at+key, reason)
		}
		if known[d.Name] {
			return pathError(at+"name", "is listed twice")
		}
		known[d.Name] = true
	}
	for i, m := range mods {
		if reason := notifyToReason(m.NotifyTo, known); reason != "" {
			return pathError("modules["+strconv.Itoa(i)+"].notify_to", reason)
		}
	}
	return nil
}

// notifyToReason says what is wrong with a module's notify_to; "" when
// nothing is. It never repeats a value.
func notifyToReason(names []string, known map[string]bool) string {
	if len(names) > MaxNotifyTo {
		return "names more than " + strconv.Itoa(MaxNotifyTo) + " destinations"
	}
	seen := map[string]bool{}
	for _, n := range names {
		if !known[n] {
			return "names a destination that is not under destinations"
		}
		if seen[n] {
			return "names a destination twice"
		}
		seen[n] = true
	}
	return ""
}
