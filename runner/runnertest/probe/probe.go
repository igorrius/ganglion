// Package probe is a test module for runners. Its payload names one
// operation, so the conformance suite can exercise each host function and
// each limit. The own and extism subpackages wrap it for each ABI.
package probe

import (
	"encoding/json"
	"errors"

	"github.com/igorrius/ganglion/pdk"
)

type op struct {
	Op    string `json:"op"`
	URL   string `json:"url,omitempty"`
	Name  string `json:"name,omitempty"`
	Value string `json:"value,omitempty"`
	MiB   int    `json:"mib,omitempty"`
}

var (
	sink  [][]byte
	spins int
)

// Run executes the operation named in the payload.
func Run(h pdk.Host, in pdk.Input) (any, error) {
	var o op
	if err := json.Unmarshal(in.Payload, &o); err != nil {
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
	case "spin":
		for spins >= 0 { // spin until the host's timeout stops the module
			spins++
		}
	case "alloc":
		for i := 0; i < o.MiB; i++ {
			sink = append(sink, make([]byte, 1<<20))
		}
		return map[string]int{"allocated_mib": o.MiB}, nil
	case "fail":
		return nil, pdk.WithKind(pdk.KindAuthFailed, errors.New(o.Value))
	}
	return nil, errors.New("unknown op " + o.Op)
}
