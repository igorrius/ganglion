//go:build !linux

package cmd

// hardenProcess has nothing to do where /proc does not expose the node's
// environment to its user's other processes.
func hardenProcess() error { return nil }
