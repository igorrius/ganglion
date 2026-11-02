//go:build !unix

package node

// LockDB is a no-op where flock is not available; one core process per
// database file is then the operator's job (ADR-0019).
func LockDB(string) (func(), error) { return func() {}, nil }
