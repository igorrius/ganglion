//go:build wasip1

// Command own is the deye-reader module for ganglion's own ABI:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o deye-reader.wasm ./modules/deye/own
//	tinygo build -target=wasip1 -buildmode=c-shared -o deye-reader.wasm ./modules/deye/own
package main

import (
	"github.com/igorrius/ganglion/modules/deye"
	"github.com/igorrius/ganglion/pdk/ownabi"
)

// handle is called by the runner; go:wasmexport makes it reachable.
//
//go:wasmexport handle
func handle(inLen uint32) uint64 { return ownabi.Serve(inLen, deye.Handle) } //nolint:unused // exported to the host

func main() {}
