//go:build wasip1

// Package ownabi implements pdk.Host for ganglion's own module ABI: host
// functions in the "ganglion" import module, JSON across the boundary.
// One of the two variants compared in ADR-0011; see runner for the host side.
package ownabi

import (
	"encoding/json"
	"errors"
	"runtime"
	"time"
	"unsafe"

	"github.com/igorrius/ganglion/pdk"
)

// Host functions provided by the runner in the "ganglion" import module.
// Variable-length answers are left in a host-side buffer; the module learns
// their length from the call's result and copies them out with take. That
// way the host never allocates inside the module's memory.

//go:wasmimport ganglion log
func hostLog(ptr, size uint32)

//go:wasmimport ganglion now_ms
func hostNowMs() int64

//go:wasmimport ganglion take
func hostTake(ptr uint32)

//go:wasmimport ganglion http_fetch
func hostHTTPFetch(ptr, size uint32) uint32

//go:wasmimport ganglion state_get
func hostStateGet(ptr, size uint32) int32

//go:wasmimport ganglion state_put
func hostStatePut(kptr, ksize, vptr, vsize uint32)

//go:wasmimport ganglion secret_get
func hostSecretGet(ptr, size uint32) int32

//go:wasmimport ganglion config_get
func hostConfigGet(ptr, size uint32) int32

func ptr(b []byte) uint32 {
	if len(b) == 0 {
		return 0
	}
	return uint32(uintptr(unsafe.Pointer(&b[0])))
}

// take copies the host's pending buffer of size n into module memory.
func take(n int32) []byte {
	buf := make([]byte, n)
	hostTake(ptr(buf))
	runtime.KeepAlive(buf)
	return buf
}

type wasmHost struct{}

func (wasmHost) Fetch(req pdk.Request) (pdk.Response, error) {
	b, err := json.Marshal(req)
	if err != nil {
		return pdk.Response{}, err
	}
	n := hostHTTPFetch(ptr(b), uint32(len(b)))
	runtime.KeepAlive(b)
	var resp pdk.Response
	if err := json.Unmarshal(take(int32(n)), &resp); err != nil {
		return pdk.Response{}, err
	}
	if resp.Error != "" {
		return resp, errors.New(resp.Error)
	}
	return resp, nil
}

func lookup(fn func(ptr, size uint32) int32, key string) (string, bool) {
	k := []byte(key)
	n := fn(ptr(k), uint32(len(k)))
	runtime.KeepAlive(k)
	if n < 0 {
		return "", false
	}
	return string(take(n)), true
}

// TinyGo does not allow imported functions as values, hence the wrappers.

func (wasmHost) StateGet(key string) (string, bool) {
	return lookup(func(p, n uint32) int32 { return hostStateGet(p, n) }, key)
}

func (wasmHost) Secret(name string) (string, bool) {
	return lookup(func(p, n uint32) int32 { return hostSecretGet(p, n) }, name)
}

func (wasmHost) Config(key string) (string, bool) {
	return lookup(func(p, n uint32) int32 { return hostConfigGet(p, n) }, key)
}

func (wasmHost) StatePut(key, value string) {
	k, v := []byte(key), []byte(value)
	hostStatePut(ptr(k), uint32(len(k)), ptr(v), uint32(len(v)))
	runtime.KeepAlive(k)
	runtime.KeepAlive(v)
}

func (wasmHost) Now() time.Time { return time.UnixMilli(hostNowMs()) }

func (wasmHost) Log(msg string) {
	b := []byte(msg)
	hostLog(ptr(b), uint32(len(b)))
	runtime.KeepAlive(b)
}

// out keeps the last Output alive until the runner has read it.
var out []byte

// Serve runs one handle call. A module's exported handle function calls it:
//
//	//go:wasmexport handle
//	func handle(inLen uint32) uint64 { return ownabi.Serve(inLen, run) }
//
// The result packs the Output's address (high 32 bits) and length.
func Serve(inLen uint32, fn func(pdk.Host, pdk.Input) (any, error)) uint64 {
	var o pdk.Output
	var in pdk.Input
	if err := json.Unmarshal(take(int32(inLen)), &in); err != nil {
		o = pdk.Encode(nil, err)
	} else {
		o = pdk.Encode(fn(wasmHost{}, in))
	}
	b, err := json.Marshal(o)
	if err != nil {
		b = []byte(`{"ok":false,"kind":"module.exception","error":"encode output"}`)
	}
	out = b
	return uint64(ptr(out))<<32 | uint64(len(out))
}
