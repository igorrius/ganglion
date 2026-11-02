//go:build unix

package node

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockDB takes an exclusive lock on <db>.lock, so a second core process
// cannot open the same database (ADR-0019). The lock goes away with the
// process, even after kill -9.
func lockDB(db string) (release func(), err error) {
	if db == ":memory:" {
		return func() {}, nil
	}
	f, err := os.OpenFile(db+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("node: lock %s: %w", db, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, fmt.Errorf("node: another core process uses %s; one core process owns a database file (ADR-0019)", db)
		}
		return nil, fmt.Errorf("node: lock %s: %w", db, err)
	}
	return func() { _ = f.Close() }, nil
}
