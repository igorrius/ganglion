package agent

import (
	"strings"
	"testing"

	"github.com/igorrius/ganglion/texts"
)

// R-CFG-6: /restart is for admins, answers first and then asks the node to
// restart; for anyone else it does nothing.
func TestRestartIsAdminOnly(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	var asked int
	e.deps.Restart = func() { asked++ }
	e.a = New(overDefaults(Config{}), e.deps)

	e.admin = false
	out := e.turn(t, message(owner, "Igor", "/restart"))
	if asked != 0 || len(out) != 1 || out[0].Text != replyTasksNotAvailable {
		t.Fatalf("asked %d, out %+v", asked, out)
	}
	e.admin = true
	out = e.turn(t, message(owner, "Igor", "/restart"))
	if asked != 1 || len(out) != 1 || !strings.Contains(out[0].Text, texts.EnglishText(texts.ReplyRestarting)) {
		t.Fatalf("asked %d, out %+v", asked, out)
	}
	if len(e.smart.requests()) != 0 {
		t.Fatal("a model was called")
	}
}

func TestRestartHasAHint(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	for _, c := range e.a.Commands() {
		if c.Name == "restart" {
			return
		}
	}
	t.Fatal("no /restart hint")
}
