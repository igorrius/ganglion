package modtrial

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"

	"go.yaml.in/yaml/v3"

	"github.com/igorrius/ganglion/runner"
)

// endPattern is the sending end of an event or a call: `<module>.<port>`,
// the module's short name and a port name, as a link names them.
var endPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*\.[a-z][a-z0-9_]{0,31}$`)

// keyFrom is the trigger key naming the sender, and the payload key that
// carries it to the module.
const keyFrom = "from"

// Keys a trigger mapping may hold, by the kind of trigger they belong to.
var triggerKeys = map[string][]string{
	kindTool:  {"tool", "args"},
	kindEvent: {"event", "payload", keyFrom},
	kindCall:  {"call", "request", keyFrom},
}

// triggerKind is the kind a trigger mapping names by its first key: tool,
// event or call. A key of another kind, or one it does not know, is refused.
func triggerKind(n yaml.Node) (string, error) {
	var kinds, keys []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		k := n.Content[i].Value
		keys = append(keys, k)
		known := false
		for kind, ks := range triggerKeys {
			if slices.Contains(ks, k) {
				known = true
				if k == kind && !slices.Contains(kinds, kind) {
					kinds = append(kinds, kind)
				}
			}
		}
		if !known {
			return "", fmt.Errorf("trigger: unknown key %q", k)
		}
	}
	kind := kindTool
	switch {
	case len(kinds) > 1:
		return "", errors.New("trigger names more than one of tool, event and call")
	case len(kinds) == 1:
		kind = kinds[0]
	}
	for _, k := range keys {
		if !slices.Contains(triggerKeys[kind], k) {
			return "", fmt.Errorf("trigger: key %q does not go with %s", k, kind)
		}
	}
	return kind, nil
}

// eventTrigger reads {event, payload, from}: an event delivered to an
// accepts port, checked with the port's shape, which a node checks before it
// delivers.
func (c checker) eventTrigger(n yaml.Node) (Trigger, error) {
	var tr struct {
		Event   string    `yaml:"event"`
		Payload yaml.Node `yaml:"payload"`
		From    string    `yaml:"from"`
	}
	if err := n.Decode(&tr); err != nil {
		return Trigger{}, fmt.Errorf("trigger: %w", err)
	}
	shape, ok := c.m.Ports.Accepts[tr.Event]
	if !ok {
		return Trigger{}, fmt.Errorf("trigger event %q is not an accepts port of the manifest", tr.Event)
	}
	payload, err := portValue(tr.Payload, "payload", shape)
	if err != nil {
		return Trigger{}, fmt.Errorf("trigger event %q: %w", tr.Event, err)
	}
	from, err := sender(tr.From, tr.Event)
	if err != nil {
		return Trigger{}, fmt.Errorf("trigger event %q: %w", tr.Event, err)
	}
	return Trigger{Kind: kindEvent, Port: tr.Event, Payload: payload, From: from}, nil
}

// callTrigger reads {call, request, from}: a call made on a served port,
// checked with the port's in shape.
func (c checker) callTrigger(n yaml.Node) (Trigger, error) {
	var tr struct {
		Call    string    `yaml:"call"`
		Request yaml.Node `yaml:"request"`
		From    string    `yaml:"from"`
	}
	if err := n.Decode(&tr); err != nil {
		return Trigger{}, fmt.Errorf("trigger: %w", err)
	}
	port, ok := c.m.Ports.Serves[tr.Call]
	if !ok {
		return Trigger{}, fmt.Errorf("trigger call %q is not a serves port of the manifest", tr.Call)
	}
	req, err := portValue(tr.Request, "request", port.In)
	if err != nil {
		return Trigger{}, fmt.Errorf("trigger call %q: %w", tr.Call, err)
	}
	from, err := sender(tr.From, tr.Call)
	if err != nil {
		return Trigger{}, fmt.Errorf("trigger call %q: %w", tr.Call, err)
	}
	return Trigger{Kind: kindCall, Port: tr.Call, Payload: req, From: from}, nil
}

// sender is the sending end; "trial.<port>" when the file names none.
func sender(from, port string) (string, error) {
	if from == "" {
		return "trial." + port, nil
	}
	if !endPattern.MatchString(from) {
		return "", fmt.Errorf("from %q is not <module>.<port>", from)
	}
	return from, nil
}

// portValue is a YAML value as JSON that fits shape. The refusal names the
// field and the shape's error, never more.
func portValue(n yaml.Node, what string, shape runner.Shape) (json.RawMessage, error) {
	if n.IsZero() {
		return nil, fmt.Errorf("%s is required", what)
	}
	var v any
	if err := n.Decode(&v); err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%s is not JSON", what)
	}
	if err := shape.Check(b); err != nil {
		return nil, fmt.Errorf("%s does not fit the port's shape: %w", what, err)
	}
	return b, nil
}

// calls checks the example's canned answers of calls: each names a calls
// port once, and has either an answer that fits the port's out shape or the
// refusal the module gets.
func (c checker) calls(raw []rawCall) ([]CannedCall, error) {
	var out []CannedCall
	seen := map[string]bool{}
	for _, rc := range raw {
		port, ok := c.m.Ports.Calls[rc.Port]
		switch {
		case !ok:
			return nil, fmt.Errorf("calls: %q is not a calls port of the manifest", rc.Port)
		case seen[rc.Port]:
			return nil, fmt.Errorf("calls: port %q appears more than once", rc.Port)
		}
		seen[rc.Port] = true
		cc, err := cannedCall(rc, port)
		if err != nil {
			return nil, fmt.Errorf("calls %q: %w", rc.Port, err)
		}
		out = append(out, cc)
	}
	return out, nil
}

// failCodes are the refusals a canned call may give, by the word the file
// uses.
var failCodes = map[string]runner.CallCode{
	"denied":      runner.CallDenied,
	"format":      runner.CallFormat,
	"not_here":    runner.CallNotHere,
	"failed":      runner.CallFailed,
	"unavailable": runner.CallUnavailable,
}

func cannedCall(rc rawCall, port runner.PortCall) (CannedCall, error) {
	hasAnswer := !rc.Answer.IsZero()
	switch {
	case hasAnswer && rc.Fails != "":
		return CannedCall{}, errors.New("has both answer and fails; give one")
	case !hasAnswer && rc.Fails == "":
		return CannedCall{}, errors.New("needs an answer or fails")
	case rc.Fails != "":
		code, ok := failCodes[rc.Fails]
		if !ok {
			return CannedCall{}, fmt.Errorf("fails %q is not one of %v", rc.Fails, slices.Sorted(maps.Keys(failCodes)))
		}
		return CannedCall{Port: rc.Port, Code: code}, nil
	}
	ans, err := portValue(rc.Answer, "answer", port.Out)
	if err != nil {
		return CannedCall{}, err
	}
	return CannedCall{Port: rc.Port, Answer: ans, Code: runner.CallAnswered}, nil
}
