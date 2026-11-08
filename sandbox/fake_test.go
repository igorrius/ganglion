package sandbox

// fakeKernel is a Kernel a test sets up.
type fakeKernel struct {
	euid     int
	abi      int
	abiErr   error
	dumpable bool
}

func (k fakeKernel) Euid() int                 { return k.euid }
func (k fakeKernel) LandlockABI() (int, error) { return k.abi, k.abiErr }
func (k fakeKernel) Dumpable() (bool, error)   { return k.dumpable, nil }
