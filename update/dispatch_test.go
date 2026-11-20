package update

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dispatchKit is a store, a fake exec and owner, and a log.
type dispatchKit struct {
	*kit
	execs []string
	log   bytes.Buffer
	self  string
	image string // the version of this binary, the image's
	uid   int
	owner int
	exec  func(string) error
}

func newDispatchKit(t *testing.T) *dispatchKit {
	t.Helper()
	return &dispatchKit{kit: newKit(t), self: "/usr/local/bin/ganglion", image: "v0.2.1", uid: 10001, owner: 10001,
		exec: func(string) error { return nil }}
}

func (k *dispatchKit) dispatch() {
	Dispatcher{Store: k.store, Self: k.self, Image: k.image, UID: k.uid, Log: &k.log,
		Owner: func(string) (int, error) { return k.owner, nil },
		Exec:  func(p string) error { k.execs = append(k.execs, p); return k.exec(p) }}.Dispatch()
}

func TestDispatchWithNoStateRunsOn(t *testing.T) {
	k := newDispatchKit(t)
	k.dispatch()
	if len(k.execs) != 0 || k.log.Len() != 0 || k.stateBytes() != nil {
		t.Fatalf("%v %q", k.execs, k.log.String())
	}
}

func TestDispatchHandsTheCommandToTheActiveRelease(t *testing.T) {
	k := newDispatchKit(t)
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	before := k.stateBytes()
	k.dispatch()
	if len(k.execs) != 1 || k.execs[0] != k.store.BinaryPath("v0.3.0") {
		t.Fatalf("%v", k.execs)
	}
	if !bytes.Equal(before, k.stateBytes()) {
		t.Fatal("a dispatch changed the state")
	}
}

func TestDispatchOfANewerImageRunsOn(t *testing.T) {
	for _, image := range []string{"v0.3.0", "v0.4.0", "dev"} {
		t.Run(image, func(t *testing.T) {
			k := newDispatchKit(t)
			if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
				t.Fatal(err)
			}
			before := k.stateBytes()
			k.image = image
			k.dispatch()
			if len(k.execs) != 0 || k.log.Len() != 0 {
				t.Fatalf("%v %q", k.execs, k.log.String())
			}
			if !bytes.Equal(before, k.stateBytes()) {
				t.Fatal("a dispatch changed the state")
			}
		})
	}
}

func TestDispatchOfTheActiveReleaseItselfRunsOn(t *testing.T) {
	k := newDispatchKit(t)
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	self, err := filepath.EvalSymlinks(k.store.BinaryPath("v0.3.0"))
	if err != nil {
		t.Fatal(err)
	}
	k.self = self
	k.dispatch()
	if len(k.execs) != 0 || k.log.Len() != 0 {
		t.Fatalf("%v %q", k.execs, k.log.String())
	}
}

// Abuse: root never runs a binary the node's user can change.
func TestDispatchNeverRunsAnotherUsersBinary(t *testing.T) {
	k := newDispatchKit(t)
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	k.uid = 0
	k.dispatch()
	if len(k.execs) != 0 {
		t.Fatalf("executed %v", k.execs)
	}
	if !strings.Contains(k.log.String(), "v0.3.0") || !strings.Contains(k.log.String(), "node's user") {
		t.Fatalf("%q", k.log.String())
	}
}

// Abuse: a binary replaced after the install is not run.
func TestDispatchNeverRunsAReplacedBinary(t *testing.T) {
	k := newDispatchKit(t)
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	bin := k.store.BinaryPath("v0.3.0")
	if err := os.Chmod(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("replaced"), 0o700); err != nil {
		t.Fatal(err)
	}
	k.dispatch()
	if len(k.execs) != 0 || !strings.Contains(k.log.String(), "check") {
		t.Fatalf("%v %q", k.execs, k.log.String())
	}
}

// Abuse: a version that is a path is refused, not followed.
func TestDispatchRefusesAStateNamingAPath(t *testing.T) {
	k := newDispatchKit(t)
	if err := os.WriteFile(filepath.Join(k.store.Dir(), StateFile), []byte(`{"active":"../x","previous":"","confirmed":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	k.dispatch()
	if len(k.execs) != 0 || k.log.Len() == 0 {
		t.Fatalf("%v %q", k.execs, k.log.String())
	}
}

func TestDispatchRunsOnWhenTheExecFails(t *testing.T) {
	k := newDispatchKit(t)
	if _, err := k.install("v0.3.0", "v0.2.1"); err != nil {
		t.Fatal(err)
	}
	k.exec = func(string) error { return errors.New("noexec") }
	k.dispatch()
	if len(k.execs) != 1 || !strings.Contains(k.log.String(), "could not be started") {
		t.Fatalf("%v %q", k.execs, k.log.String())
	}
}
