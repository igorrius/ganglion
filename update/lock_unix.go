//go:build unix

package update

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lockDir takes the store's exclusive lock without waiting: a second
// install or rollback is refused as ErrBusy. The lock goes with the
// process, so a crash never leaves the store locked.
func lockDir(path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("update: lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("update: lock: %w", err)
	}
	return func() { _ = f.Close() }, nil // closing the file releases the lock
}
