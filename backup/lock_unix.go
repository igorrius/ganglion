//go:build unix

package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockRun takes an exclusive lock on <dataDir>/backup.lock, so a backup
// started on the host (`ganglion backup now`) and the node's daily run
// never share the work folder. The lock goes away with the process.
func lockRun(dataDir string) (release func(), err error) {
	f, err := os.OpenFile(filepath.Join(dataDir, lockName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("backup: lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("backup: lock: %w", err)
	}
	return func() { _ = f.Close() }, nil
}
