package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/groups"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
	"github.com/igorrius/ganglion/turns"
)

const familyGroup = "telegram:-1001"

// groupRequestEvent is the event groups.Requests puts for a group that is
// not connected.
func groupRequestEvent(p event.GroupRequest) event.Event {
	seq++
	raw, _ := json.Marshal(p)
	return event.Event{ID: fmt.Sprintf("01GR%04d", seq), IdemKey: fmt.Sprintf("group:request:%s:%d", p.Group, seq),
		Source: event.SourceGroups, Conv: "telegram:42", Kind: event.KindGroupRequest, Payload: raw, At: t0}
}

func familyRequest() event.GroupRequest {
	return event.GroupRequest{Group: familyGroup, Conv: familyGroup + "/45", Title: "Family *chat*",
		From: "telegram:user:9", Name: "Ann_B", Principal: owner, Transport: "telegram"}
}

// R-DLV-17: a group's request becomes a card with two buttons in the
// admin's private chat and one notice in the conversation it came from,
// with no model call and nothing in the history.
func TestGroupRequestSendsTheCardAndTheNotice(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	ev := groupRequestEvent(familyRequest())
	out := e.turn(t, ev)
	if len(out) != 2 {
		t.Fatalf("out %+v", out)
	}
	card, notice := out[0], out[1]
	if card.Conv != "telegram:42" || card.Transport != "telegram" || card.IdemKey != ev.IdemKey+":card" || !card.NoPreview || len(card.Buttons) != 2 {
		t.Fatalf("card %+v", card)
	}
	for _, want := range []string{familyGroup, `Family \*chat\*`, `Ann\_B`} {
		if !strings.Contains(card.Text, want) {
			t.Errorf("the card lacks %q: %q", want, card.Text)
		}
	}
	if card.Buttons[0].Label != texts.EnglishText(texts.ButtonAllow) || card.Buttons[1].Label != texts.EnglishText(texts.ButtonRefuse) {
		t.Fatalf("buttons %+v", card.Buttons)
	}
	if notice.Conv != familyGroup+"/45" || notice.Text != texts.EnglishText(texts.GroupNotConnected) || len(notice.Buttons) != 0 || notice.IdemKey != ev.IdemKey+":notice" {
		t.Fatalf("notice %+v", notice)
	}
	if n := len(e.smart.requests()) + len(e.cheap.requests()); n != 0 {
		t.Fatalf("%d model calls for a group's request", n)
	}
	// Each button is the admin's, in the private chat, and names the group.
	for i, action := range []string{groups.ActionAllow, groups.ActionRefuse} {
		p, err := e.pending.Press(context.Background(), card.Buttons[i].ID, pending.By{Principal: owner, Conv: "telegram:42", Event: "press"})
		if err != nil {
			t.Fatalf("press %s: %v", action, err)
		}
		var args groups.ButtonArgs
		if err := json.Unmarshal(p.Button.Args, &args); err != nil || p.Button.Action != action || args.Group != familyGroup ||
			!p.Button.Expires.Equal(e.now.Add(groups.RequestTTL)) {
			t.Fatalf("button %+v, args %+v, err %v", p.Button, args, err)
		}
	}
	// Abuse: someone else cannot press the admin's button.
	ev2 := groupRequestEvent(familyRequest())
	out = e.turn(t, ev2)
	if _, err := e.pending.Press(context.Background(), out[0].Buttons[0].ID, pending.By{Principal: "telegram:user:9", Conv: "telegram:42", Event: "p2"}); err == nil {
		t.Fatal("a stranger pressed Allow")
	}
	tr, err := e.turns.Get(context.Background(), ev.ID)
	if err != nil || tr.Outcome != turns.OutcomeCommand || tr.Principal != owner {
		t.Fatalf("turn %+v, %v", tr, err)
	}
}

// Abuse: a request that did not come from the node, or is addressed to
// someone who is not an admin, sends nothing.
func TestGroupRequestNotTheNodesIsDropped(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	forged := groupRequestEvent(familyRequest())
	forged.Source = "telegram"
	if out := e.turn(t, forged); len(out) != 0 {
		t.Fatalf("a forged request sent %+v", out)
	}
	e.admin = false
	if out := e.turn(t, groupRequestEvent(familyRequest())); len(out) != 0 {
		t.Fatalf("a request for someone who is not an admin sent %+v", out)
	}
}

// R-POL-10: the admin role does not follow an admin into a group chat.
func TestAdminRoleDoesNotFollowIntoAGroup(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	var asked int
	e.deps.Restart = func() { asked++ }
	e.a = New(overDefaults(Config{}), e.deps)

	inGroup := message(owner, "Igor", "/restart")
	p, _ := json.Marshal(event.Message{Text: "/restart", From: owner, Name: "Igor", Group: familyGroup})
	inGroup.Payload, inGroup.Conv = p, familyGroup+"/45"
	if out := e.turn(t, inGroup); asked != 0 || len(out) != 1 || out[0].Text != replyTasksNotAvailable {
		t.Fatalf("an admin's command ran in a group: asked %d, out %+v", asked, out)
	}
	if e.turn(t, message(owner, "Igor", "/restart")); asked != 1 {
		t.Fatalf("the same command in the private chat: asked %d", asked)
	}
}
