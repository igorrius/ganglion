//go:build wasip1

// Command own is the probe module for ganglion's own ABI.
package main

import (
	"github.com/igorrius/ganglion/pdk/ownabi"
	"github.com/igorrius/ganglion/runner/runnertest/probe"
)

// handle is called by the runner; go:wasmexport makes it reachable.
//
//go:wasmexport handle
func handle(inLen uint32) uint64 { return ownabi.Serve(inLen, probe.Run) } //nolint:unused // exported to the host

func main() {}
