//go:build linux

package builder

import (
	"io/fs"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// dropFileCache tells the kernel it may drop the cached pages of every file
// beneath root. A build reads hundreds of megabytes of compiler libraries;
// the kernel keeps them until memory runs short, and a container's limit
// counts them (0.9 GB stayed after one build on Railway). It is advice: a
// page in use stays, and a file that cannot be opened is passed over.
func dropFileCache(root string) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return nil //nolint:nilerr // best effort: go on with the rest
		}
		f, err := os.Open(path)
		if err != nil {
			return nil //nolint:nilerr // best effort
		}
		_ = unix.Fadvise(int(f.Fd()), 0, 0, unix.FADV_DONTNEED)
		_ = f.Close()
		return nil
	})
}
