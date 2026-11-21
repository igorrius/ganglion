package node

import "errors"

// ErrLocked is what LockDB returns when another process holds the database's
// lock: a node is running on it (ADR-0019). A command that must not work on
// a database a running node owns tells that case from a failure to lock.
var ErrLocked = errors.New("node: another core process uses the database")
