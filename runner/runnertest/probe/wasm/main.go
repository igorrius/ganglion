//go:build wasip1

// Command wasm is the probe module built for the runner.
package main

import (
	"github.com/igorrius/ganglion/pdk/guest"
	"github.com/igorrius/ganglion/runner/runnertest/probe"
)

// handle is called by the runner; go:wasmexport makes it reachable.
//
//go:wasmexport handle
func handle(inLen uint32) uint64 { return guest.Serve(inLen, probe.Run) } //nolint:unused // exported to the host

func main() {}
