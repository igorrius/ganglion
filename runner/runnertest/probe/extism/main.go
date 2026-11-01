//go:build wasip1

// Command extism is the probe module for the Extism ABI.
package main

import (
	"github.com/igorrius/ganglion/pdk/extismabi"
	"github.com/igorrius/ganglion/runner/runnertest/probe"
)

// handle is called by the host; go:wasmexport makes it reachable.
//
//go:wasmexport handle
func handle() int32 { return extismabi.Serve(probe.Run) } //nolint:unused // exported to the host

func main() {}
