//go:build !unix

package backup

// lockRun is a no-op where flock is not available; the node's daily run
// still never overlaps itself.
func lockRun(string) (func(), error) { return func() {}, nil }
