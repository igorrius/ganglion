//go:build wasip1

// Command probe is a test module for the runner. Its payload names one
// operation, so tests can exercise each host function and each limit.
package main

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

var sink [][]byte

func run(h pdk.Host, in pdk.Input) (any, error) {
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
		for {
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

// handle is called by the runner; go:wasmexport makes it reachable.
//
//go:wasmexport handle
func handle(inLen uint32) uint64 { return pdk.Serve(inLen, run) }

func main() {}
