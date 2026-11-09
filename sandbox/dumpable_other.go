//go:build !linux

package sandbox

// SetDumpable has nothing to do where /proc does not expose the node's
// environment; the sandbox is off there.
func SetDumpable(bool) error { return nil }
