//go:build wasip1

// Package extismabi implements pdk.Host on top of Extism: Extism's own HTTP,
// config and log functions, plus state and secret host functions that the
// runner adds in the extism:host/user namespace. One of the two variants
// compared in ADR-0011; see runner/extismrun for the host side.
package extismabi

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/extism/go-pdk"

	gpdk "github.com/igorrius/ganglion/pdk"
)

// Values are passed as Extism memory offsets; 0 means "absent".

//go:wasmimport extism:host/user state_get
func hostStateGet(key uint64) uint64

//go:wasmimport extism:host/user state_put
func hostStatePut(key, value uint64)

//go:wasmimport extism:host/user secret_get
func hostSecretGet(name uint64) uint64

var methods = map[string]pdk.HTTPMethod{
	"GET": pdk.MethodGet, "HEAD": pdk.MethodHead, "POST": pdk.MethodPost,
	"PUT": pdk.MethodPut, "PATCH": pdk.MethodPatch, "DELETE": pdk.MethodDelete,
}

type host struct{}

func (host) Fetch(req gpdk.Request) (gpdk.Response, error) {
	method := req.Method
	if method == "" {
		method = "GET"
	}
	m, ok := methods[method]
	if !ok {
		return gpdk.Response{}, fmt.Errorf("unsupported HTTP method %s", method)
	}
	r := pdk.NewHTTPRequest(m, req.URL)
	for k, v := range req.Headers {
		r.SetHeader(k, v)
	}
	if len(req.Body) > 0 {
		r.SetBody(req.Body)
	}
	resp := r.Send() // a host Extism does not allow traps the whole call
	return gpdk.Response{Status: int(resp.Status()), Body: resp.Body()}, nil
}

func lookup(fn func(uint64) uint64, key string) (string, bool) {
	k := pdk.AllocateString(key)
	defer k.Free()
	off := fn(k.Offset())
	if off == 0 {
		return "", false
	}
	v := pdk.FindMemory(off)
	defer v.Free()
	return string(v.ReadBytes()), true
}

// TinyGo does not allow imported functions as values, hence the closures.

func (host) StateGet(key string) (string, bool) {
	return lookup(func(p uint64) uint64 { return hostStateGet(p) }, key)
}

func (host) Secret(name string) (string, bool) {
	return lookup(func(p uint64) uint64 { return hostSecretGet(p) }, name)
}

func (host) StatePut(key, value string) {
	k, v := pdk.AllocateString(key), pdk.AllocateString(value)
	defer k.Free()
	defer v.Free()
	hostStatePut(k.Offset(), v.Offset())
}

func (host) Config(key string) (string, bool) { return pdk.GetConfig(key) }
func (host) Now() time.Time                   { return time.Now() }
func (host) Log(msg string)                   { pdk.Log(pdk.LogInfo, msg) }

// Serve runs one handle call. A module's exported function calls it:
//
//	//go:wasmexport handle
//	func handle() int32 { return extismabi.Serve(run) }
func Serve(fn func(gpdk.Host, gpdk.Input) (any, error)) int32 {
	var in gpdk.Input
	var o gpdk.Output
	if err := json.Unmarshal(pdk.Input(), &in); err != nil {
		o = gpdk.Encode(nil, err)
	} else {
		o = gpdk.Encode(fn(host{}, in))
	}
	b, err := json.Marshal(o)
	if err != nil {
		b = []byte(`{"ok":false,"kind":"module.exception","error":"encode output"}`)
	}
	pdk.Output(b)
	return 0
}
