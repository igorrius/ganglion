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
	note := &askedNote{}
	e.deps.Restart = func() { asked++ }
	e.deps.RestartNote = note
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
	// The node will say it is back where it was asked (#238); the one who
	// was refused is not recorded.
	if len(note.asked) != 1 || note.asked[0] != owner+" "+out[0].Transport+" "+out[0].Conv {
		t.Fatalf("recorded %v", note.asked)
	}
	if len(e.smart.requests()) != 0 {
		t.Fatal("a model was called")
	}
	if !strings.Contains(e.logs.String(), `msg="agent: restart requested" by=`+owner) {
		t.Fatalf("the log does not say who asked for the restart:\n%s", e.logs.String())
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

// askedNote records the restart requests.
type askedNote struct{ asked []string }

func (n *askedNote) Ask(principal, transport, conv string) error {
	n.asked = append(n.asked, principal+" "+transport+" "+conv)
	return nil
}
