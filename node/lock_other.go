//go:build !unix

package node

// lockDB is a no-op where flock is not available; one core process per
// database file is then the operator's job (ADR-0019).
func lockDB(string) (func(), error) { return func() {}, nil }
