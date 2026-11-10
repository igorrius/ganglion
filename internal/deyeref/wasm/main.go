//go:build wasip1

// Command wasm is the Go reference reader as a module, for the comparison
// with the Rust example and for modbench:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o deye-reader.wasm ./internal/deyeref/wasm
//	tinygo build -target=wasip1 -buildmode=c-shared -o deye-reader.wasm ./internal/deyeref/wasm
package main

import (
	"github.com/igorrius/ganglion/internal/deyeref"
	"github.com/igorrius/ganglion/pdk/guest"
)

// handle is called by the runner; go:wasmexport makes it reachable.
//
//go:wasmexport handle
func handle(inLen uint32) uint64 { return guest.Serve(inLen, deyeref.Handle) } //nolint:unused // exported to the host

func main() {}
