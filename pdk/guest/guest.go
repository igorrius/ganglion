//go:build wasip1

// Package guest is the module side of the ganglion ABI (ADR-0011): it
// implements pdk.Host on top of the host functions in the "ganglion" import
// module, with JSON across the boundary. The host side is package runner.
package guest

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

//go:wasmimport ganglion cell_report
func hostCellReport(nptr, nsize, vptr, vsize uint32) int32

//go:wasmimport ganglion cell_get
func hostCellGet(ptr, size uint32) int32

//go:wasmimport ganglion notify
func hostNotify(ptr, size uint32) int32

//go:wasmimport ganglion emit
func hostEmit(pptr, psize, vptr, vsize uint32) int32

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

func (wasmHost) CellReport(name string, value any) error {
	v, err := json.Marshal(value)
	if err != nil {
		return err
	}
	n := []byte(name)
	code := hostCellReport(ptr(n), uint32(len(n)), ptr(v), uint32(len(v)))
	runtime.KeepAlive(n)
	runtime.KeepAlive(v)
	switch code {
	case 0:
		return nil
	case -2:
		return pdk.ErrCellType
	}
	return pdk.ErrCellDenied
}

func (wasmHost) CellGet(id string) (json.RawMessage, time.Time, bool) {
	raw, ok := lookup(func(p, n uint32) int32 { return hostCellGet(p, n) }, id)
	if !ok {
		return nil, time.Time{}, false
	}
	var c struct {
		Value json.RawMessage `json:"value"`
		AtMs  int64           `json:"at_ms"`
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, time.Time{}, false
	}
	return c.Value, time.UnixMilli(c.AtMs), true
}

func (wasmHost) Notify(text string) error {
	b := []byte(text)
	code := hostNotify(ptr(b), uint32(len(b)))
	runtime.KeepAlive(b)
	switch code {
	case 0:
		return nil
	case -2:
		return pdk.ErrNotifyLimit
	case -3:
		return pdk.ErrNotifyNotHere
	}
	return pdk.ErrNotifyDenied
}

func (wasmHost) Emit(port string, value any) error {
	v, err := json.Marshal(value)
	if err != nil {
		return err
	}
	p := []byte(port)
	code := hostEmit(ptr(p), uint32(len(p)), ptr(v), uint32(len(v)))
	runtime.KeepAlive(p)
	runtime.KeepAlive(v)
	switch code {
	case 0:
		return nil
	case -2:
		return pdk.ErrEmitFormat
	case -3:
		return pdk.ErrEmitNotHere
	case -4:
		return pdk.ErrEmitFailed
	}
	return pdk.ErrEmitDenied
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
//	func handle(inLen uint32) uint64 { return guest.Serve(inLen, run) }
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
