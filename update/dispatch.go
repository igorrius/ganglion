package update

import (
	"fmt"
	"io"
	"path/filepath"
)

// OwnerFunc returns the user id that owns the file at path.
type OwnerFunc func(path string) (uid int, err error)

// Dispatcher is the step before a host command other than `run`: it hands
// the command to the node's active release, so the host runs the version
// the node runs (R-UPD-9, ADR-0096). It only reads the state: attempts and
// fall backs stay the launcher's.
type Dispatcher struct {
	Store *Store
	Self  string // this process's executable, symlinks resolved
	UID   int    // the user running the command
	Owner OwnerFunc
	Exec  ExecFunc
	Log   io.Writer // where a line goes that the operator should see
}

// Dispatch returns when this process is the one to run the command, which
// is also what happens on any failure: the command is never stopped. When
// it hands the command over it does not return.
func (d Dispatcher) Dispatch() {
	st, err := d.Store.State()
	if err != nil {
		_, _ = fmt.Fprintf(d.Log, "ganglion: the node's releases could not be read; running this binary: %v\n", err)
		return
	}
	if st.Active == "" || isBinaryOf(d.Store, st.Active, d.Self) {
		return
	}
	bin := d.Store.BinaryPath(st.Active)
	owner, err := d.Owner(bin)
	if err != nil {
		_, _ = fmt.Fprintf(d.Log, "ganglion: the node runs %s, which could not be checked; running this binary: %v\n", st.Active, err)
		return
	}
	if owner != d.UID {
		_, _ = fmt.Fprintf(d.Log, "ganglion: the node runs %s, which belongs to uid %d; this is the image's binary. Run host commands as the node's user to use %s.\n", st.Active, owner, st.Active)
		return
	}
	if err := d.Store.Verify(st.Active); err != nil {
		_, _ = fmt.Fprintf(d.Log, "ganglion: the node's release %s does not pass its check; running this binary: %v\n", st.Active, err)
		return
	}
	if err := d.Exec(bin); err != nil {
		_, _ = fmt.Fprintf(d.Log, "ganglion: %s could not be started; running this binary: %v\n", st.Active, err)
	}
}

// isBinaryOf reports whether self is the binary of version in store.
func isBinaryOf(store *Store, version, self string) bool {
	if version == "" {
		return false
	}
	bin := store.BinaryPath(version)
	if resolved, err := filepath.EvalSymlinks(bin); err == nil {
		bin = resolved
	}
	return bin == self
}
