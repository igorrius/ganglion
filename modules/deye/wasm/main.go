//go:build wasip1

// Command wasm is the deye-reader module:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o deye-reader.wasm ./modules/deye/wasm
//	tinygo build -target=wasip1 -buildmode=c-shared -o deye-reader.wasm ./modules/deye/wasm
package main

import (
	"github.com/igorrius/ganglion/modules/deye"
	"github.com/igorrius/ganglion/pdk/guest"
)

// handle is called by the runner; go:wasmexport makes it reachable.
//
//go:wasmexport handle
func handle(inLen uint32) uint64 { return guest.Serve(inLen, deye.Handle) } //nolint:unused // exported to the host

func main() {}
