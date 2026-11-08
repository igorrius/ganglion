package modstore_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

func TestStatusAfterAFallback(t *testing.T) {
	e := newEnv(t)
	s := e.open(t)
	if cs := s.Status(t.Context(), status.Scope{Now: time.Now()}); len(cs) != 0 {
		t.Fatalf("a clean start shows %+v", cs)
	}
	v2 := variant("v2")
	update(t, s, release("0.2.0", v2, goodManifest))
	if err := os.WriteFile(filepath.Join(e.dir, "html2md", sum(v2)+".wasm"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	cs := e.open(t).Status(t.Context(), status.Scope{Now: time.Now()})
	if len(cs) != 1 {
		t.Fatalf("lines %+v", cs)
	}
	c := cs[0]
	if c.State != status.Warn || c.Line != texts.StatusModuleFallback || c.Reason != texts.StatusReasonModuleFailed ||
		c.Vars["module"] != "html2md" || c.Vars["version"] != "0.2.0" || c.Since.IsZero() || c.Section != status.SectionProblems {
		t.Errorf("fallback: %+v", c)
	}
}
