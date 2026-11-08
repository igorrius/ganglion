//go:build !linux

package host

import (
	"testing"
	"time"
)

// lchtimes cannot set a symlink's own times here; the test that needs it
// is skipped.
func lchtimes(t *testing.T, _ string, _ time.Time) {
	t.Helper()
	t.Skip("setting a symlink's own times is done on Linux")
}
