package sandbox

import (
	"os"
	"testing"
)

// TestMain lets the test binary be the helper, as the ganglion binary is.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == HelperArg {
		os.Exit(Helper())
	}
	os.Exit(m.Run())
}
