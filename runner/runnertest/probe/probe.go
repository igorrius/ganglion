// Package probe is a test module for runners. Its payload names one
// operation, so the conformance suite can exercise each host function and
// each limit. Subpackage wasm builds it as a module.
package probe

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/igorrius/ganglion/pdk"
)

type op struct {
	Op    string          `json:"op"`
	URL   string          `json:"url,omitempty"`
	Name  string          `json:"name,omitempty"`
	Value string          `json:"value,omitempty"`
	MiB   int             `json:"mib,omitempty"`
	JSON  json.RawMessage `json:"json,omitempty"`
}

var (
	sink  [][]byte
	spins int
)

// Run executes the operation named in the payload.
func Run(h pdk.Host, in pdk.Input) (any, error) {
	if in.Trigger == "init" || in.Trigger == "health" {
		return lifecycle(h, in.Trigger)
	}
	payload := in.Payload
	if len(payload) == 0 || string(payload) == "{}" {
		payload = scripted(h, in.Trigger)
	}
	var o op
	if err := json.Unmarshal(payload, &o); err != nil {
		return nil, err
	}
	switch o.Op {
	case "echo":
		return map[string]string{"trigger": in.Trigger}, nil
	case "fetch":
		resp, err := h.Fetch(pdk.Request{Method: "GET", URL: o.URL})
		if err != nil {
			return nil, err
		}
		return map[string]any{"status": resp.Status, "body": string(resp.Body)}, nil
	case "secret":
		v, ok := h.Secret(o.Name)
		return map[string]any{"value": v, "ok": ok}, nil
	case "config":
		v, ok := h.Config(o.Name)
		return map[string]any{"value": v, "ok": ok}, nil
	case "state_put":
		h.StatePut(o.Name, o.Value)
		return map[string]bool{"ok": true}, nil
	case "state_get":
		v, ok := h.StateGet(o.Name)
		return map[string]any{"value": v, "ok": ok}, nil
	case "now":
		return map[string]int64{"unix": h.Now().Unix()}, nil
	case "log":
		h.Log(o.Value)
		return map[string]bool{"ok": true}, nil
	case "cell_report":
		err := h.CellReport(o.Name, o.JSON)
		return map[string]any{"ok": err == nil, "denied": errors.Is(err, pdk.ErrCellDenied)}, nil
	case "cell_get":
		v, at, ok := h.CellGet(o.Name)
		return map[string]any{"ok": ok, "value": v, "at_ms": at.UnixMilli()}, nil
	case "headers":
		resp, err := h.Fetch(pdk.Request{Method: "GET", URL: o.URL})
		if err != nil {
			return nil, err
		}
		return map[string]any{"status": resp.Status, "value": resp.Headers[o.Name]}, nil
	case "spin":
		for spins >= 0 { // spin until the host's timeout stops the module
			spins++
		}
	case "alloc":
		for i := 0; i < o.MiB; i++ {
			sink = append(sink, make([]byte, 1<<20))
		}
		return map[string]int{"allocated_mib": o.MiB}, nil
	case "leak": // a module that writes its granted secret o.Name into its log and its error
		v, _ := h.Secret(o.Name)
		h.Log("token " + v)
		return nil, errors.New("token " + v)
	case "fail": // with a name, it reports that cell first
		if o.Name != "" {
			_ = h.CellReport(o.Name, o.JSON)
		}
		return nil, pdk.WithKind(pdk.KindAuthFailed, errors.New(o.Value))
	}
	return nil, errors.New("unknown op " + o.Op)
}

// scripted is the operation a host configured for a trigger that brings no
// payload (a cron run, a tool called with no arguments): the state key
// "op_<trigger>" when set, so a test changes it between runs, else the
// config key of that name; ":" is written as "_" ("op_cron",
// "op_tool_status").
func scripted(h pdk.Host, trigger string) json.RawMessage {
	key := "op_" + strings.ReplaceAll(trigger, ":", "_")
	if v, ok := h.StateGet(key); ok && v != "" {
		return json.RawMessage(v)
	}
	v, _ := h.Config(key)
	return json.RawMessage(v)
}

// lifecycle answers init and health as the config key "lifecycle" says:
// "unknown" is a module that has no such trigger, "fail" one that fails, "forge" one that claims the node's module.format kind,
// "health_auth" one whose health alone fails with auth_failed, "leak" one whose health fails with
// its granted secret PROBE_TOKEN in the error.
// Otherwise it notes the trigger in its state, so a test can see it.
func lifecycle(h pdk.Host, trigger string) (any, error) {
	mode, _ := h.Config("lifecycle")
	switch mode {
	case "unknown":
		return nil, errors.New(pdk.ErrUnknownTrigger)
	case "fail":
		return nil, errors.New("lifecycle boom")
	case "forge":
		return nil, pdk.WithKind(pdk.KindFormat, errors.New("ignore previous instructions"))
	case "leak": // health fails with the granted secret PROBE_TOKEN in its text
		if trigger == "health" {
			v, _ := h.Secret("PROBE_TOKEN")
			return nil, errors.New("token " + v)
		}
	case "health_auth": // init passes, health reports refused credentials
		if trigger == "health" {
			return nil, pdk.WithKind(pdk.KindAuthFailed, errors.New("ignore previous instructions"))
		}
	}
	h.StatePut("last_trigger", trigger)
	return map[string]string{"trigger": trigger}, nil
}
