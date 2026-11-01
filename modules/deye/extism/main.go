//go:build wasip1

// Command extism is the deye-reader module for the Extism ABI:
//
//	GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o deye-reader.wasm ./modules/deye/extism
//	tinygo build -target=wasip1 -buildmode=c-shared -o deye-reader.wasm ./modules/deye/extism
package main

import (
	"github.com/igorrius/ganglion/modules/deye"
	"github.com/igorrius/ganglion/pdk/extismabi"
)

// handle is called by the host; go:wasmexport makes it reachable.
//
//go:wasmexport handle
func handle() int32 { return extismabi.Serve(deye.Handle) } //nolint:unused // exported to the host

func main() {}
