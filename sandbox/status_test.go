package sandbox

import (
	"testing"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

func TestStatusSaysWhyItIsOff(t *testing.T) {
	s := New("/nonexistent", Policy{}, fakeKernel{euid: 0, abi: 5}, time.Unix(0, 0))
	got, err := s.Status(t.Context(), status.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	want := status.Check{Section: status.SectionProblems, State: status.Off, Line: texts.StatusSandboxOff,
		Reason: texts.StatusReasonSandboxRoot, Since: time.Unix(0, 0)}
	if len(got) != 1 || got[0].Line != want.Line || got[0].State != want.State || got[0].Reason != want.Reason {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestStatusReadyNamesTheABI(t *testing.T) {
	s := &Sandbox{reason: Ready, abi: 6}
	got, _ := s.Status(t.Context(), status.Scope{})
	if len(got) != 1 || got[0].State != status.OK || got[0].Vars["abi"] != "6" {
		t.Fatalf("got %+v", got)
	}
}

func TestEveryReasonHasANameAndAText(t *testing.T) {
	for r := Ready; r <= SelfTestFailed; r++ {
		if r.String() == "" {
			t.Errorf("reason %d has no name", r)
		}
		if _, ok := offReasons[r]; r != Ready && !ok {
			t.Errorf("reason %v has no text", r)
		}
	}
}
