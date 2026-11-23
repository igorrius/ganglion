package edgeadmin_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/edgeadmin"
	"github.com/igorrius/ganglion/edgejoin"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/pending"
)

const (
	owner   = "telegram:user:123456"
	ownerC  = "telegram:123456"
	other   = "telegram:user:7"
	otherC  = "telegram:7"
	address = "https://example.com"
	secret  = "ABCD-EFGH-JKMN-PQRS-TVWX-YZ"
)

// fakeService is edgejoin.Service as the admin sees it; it records every
// call, so a refusal can be shown to have reached nothing.
type fakeService struct {
	mu        sync.Mutex
	calls     []string
	digest    string
	edges     []edgejoin.Edge
	taken     bool
	revokeErr error
}

func (f *fakeService) note(s string) { f.mu.Lock(); f.calls = append(f.calls, s); f.mu.Unlock() }

func (f *fakeService) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func (f *fakeService) Propose(_ context.Context, name string) (edgejoin.Proposal, error) {
	f.note("propose " + name)
	if f.taken {
		return edgejoin.Proposal{}, edgejoin.ErrNameTaken
	}
	return edgejoin.Proposal{Name: name, Digest: f.digest, Permissions: natsbus.UserPermissions{
		Publish: []string{"g.edge.home.>", "$JSC.R.>"}, Subscribe: []string{"g.api.home.>"}}}, nil
}

func (f *fakeService) Approve(_ context.Context, name, digest string) (string, time.Time, error) {
	f.note("approve " + name)
	if digest != f.digest {
		return "", time.Time{}, edgejoin.ErrStale
	}
	return secret, time.Date(2026, 11, 23, 12, 10, 0, 0, time.UTC), nil
}

func (f *fakeService) Revoke(_ context.Context, name string) error {
	f.note("revoke " + name)
	if f.revokeErr != nil {
		return f.revokeErr
	}
	for i, e := range f.edges {
		if e.Name == name {
			f.edges = append(f.edges[:i], f.edges[i+1:]...)
			return nil
		}
	}
	return edgejoin.ErrUnknownEdge
}

func (f *fakeService) Edges(context.Context) ([]edgejoin.Edge, error) {
	f.note("edges")
	return f.edges, nil
}

// store keeps the stored buttons by id.
type store struct {
	mu sync.Mutex
	m  map[string]pending.Button
}

func (s *store) AddButtons(_ context.Context, bs ...pending.Button) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range bs {
		s.m[b.ID] = b
	}
	return nil
}

func (s *store) byAction(action string) pending.Button {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.m {
		if b.Action == action {
			return b
		}
	}
	return pending.Button{}
}

type rig struct {
	svc     *fakeService
	buttons *store
	cat     *texts.Catalog
	sent    []event.Outgoing
	a       *edgeadmin.Admin
}

func newRig(t *testing.T, addr string) *rig {
	t.Helper()
	cat, err := texts.New()
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{svc: &fakeService{digest: "d1"}, buttons: &store{m: map[string]pending.Button{}}, cat: cat}
	r.a = edgeadmin.New(edgeadmin.Deps{
		Service: r.svc, Buttons: r.buttons,
		Enqueue: func(_ context.Context, m ...event.Outgoing) error { r.sent = append(r.sent, m...); return nil },
		Lang:    func(context.Context, string) (texts.Lang, error) { return texts.English, nil },
		Texts:   cat,
		IsAdmin: func(_ context.Context, p string) (bool, error) { return p == owner, nil },
		Address: func() string { return addr },
		Private: func(p string) (string, string, bool) {
			if p == owner {
				return "telegram", ownerC, true
			}
			return "telegram", otherC, true
		},
		Location: time.UTC,
	})
	return r
}

func (r *rig) speak() texts.Speaker { return texts.Speaker{Lang: texts.English, Cat: r.cat} }

func (r *rig) call(principal, conv, args string) tool.Call {
	return tool.Call{TurnID: "t1", N: 1, Conv: conv, Transport: "telegram", Principal: principal, Name: edgeadmin.ToolJoin,
		Args: json.RawMessage(args), Speak: r.speak()}
}

func (r *rig) tool(t *testing.T) tool.Handler {
	t.Helper()
	hs := r.a.Tools()
	if len(hs) != 1 || hs[0].Spec().Name != "edge.join" || !hs[0].Spec().AdminOnly {
		t.Fatalf("tools = %v", hs)
	}
	return hs[0]
}

func (r *rig) press(action string) pending.QuickPress {
	b := r.buttons.byAction(action)
	return pending.QuickPress{Speak: r.speak(), Button: b, Transport: "telegram", Event: "e1"}
}

// card asks for a card as the owner and returns it.
func (r *rig) card(t *testing.T) event.Outgoing {
	t.Helper()
	res, err := r.tool(t).Run(context.Background(), r.call(owner, ownerC, `{"name":"home"}`))
	if err != nil || len(res.Out) != 1 {
		t.Fatalf("card: %+v %v", res, err)
	}
	return res.Out[0]
}

func TestCardListsThePermissions(t *testing.T) {
	r := newRig(t, address)
	out := r.card(t)
	for _, want := range []string{"`home`", "`g.edge.home.>`", "`$JSC.R.>`", "`g.api.home.>`", "one-time code", "10 minutes"} {
		if !strings.Contains(out.Text, want) {
			t.Errorf("card lacks %q:\n%s", want, out.Text)
		}
	}
	if len(out.Buttons) != 2 || out.Conv != ownerC {
		t.Fatalf("buttons %+v conv %s", out.Buttons, out.Conv)
	}
	for _, a := range []string{edgeadmin.ActionApprove, edgeadmin.ActionCancel} {
		b := r.buttons.byAction(a)
		if b.Principal != owner || b.Conv != ownerC || b.Expires.IsZero() || !strings.Contains(string(b.Args), `"d1"`) {
			t.Errorf("%s button = %+v", a, b)
		}
	}
	if strings.Contains(out.Text, secret) {
		t.Fatal("a card holds a code")
	}
}

func TestNonAdminAndGroupGetNoCard(t *testing.T) {
	r := newRig(t, address)
	h := r.tool(t)
	if _, err := h.Run(context.Background(), r.call(other, otherC, `{"name":"home"}`)); err == nil {
		t.Fatal("a non-admin got a card")
	}
	if _, err := h.Run(context.Background(), r.call(owner, "telegram:-100500", `{"name":"home"}`)); err == nil {
		t.Fatal("a group got a card")
	}
	if r.svc.count() != 0 || len(r.buttons.m) != 0 {
		t.Fatalf("the service was reached: %v", r.svc.calls)
	}
}

func TestBadAndTakenNamesGiveAnErrorAndNoCard(t *testing.T) {
	r := newRig(t, address)
	h := r.tool(t)
	for _, args := range []string{`{"name":"Bad Name!"}`, `{"name":"core"}`, `{"name":"a.b"}`, `{}`, `{"name":"home","x":1}`} {
		if _, err := h.Run(context.Background(), r.call(owner, ownerC, args)); err == nil {
			t.Errorf("%s: no error", args)
		}
	}
	r.svc.taken = true
	_, err := h.Run(context.Background(), r.call(owner, ownerC, `{"name":"home"}`))
	if err == nil || !strings.Contains(err.Error(), "connected already") {
		t.Fatalf("taken: %v", err)
	}
	if len(r.buttons.m) != 0 {
		t.Fatal("a card was stored")
	}
}

func TestApproveShowsTheCodeOnceAndTheNoteHasNone(t *testing.T) {
	r := newRig(t, address)
	r.card(t)
	e, err := r.a.QuickActions()[edgeadmin.ActionApprove](context.Background(), r.press(edgeadmin.ActionApprove))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(e.Text, secret); n != 1 {
		t.Fatalf("the code shows %d times:\n%s", n, e.Text)
	}
	for _, want := range []string{"`ganglion edge join https://example.com " + secret + "`", "12:10", "password"} {
		if !strings.Contains(e.Text, want) {
			t.Errorf("edit lacks %q:\n%s", want, e.Text)
		}
	}
	if strings.Contains(e.Note, secret) || strings.Contains(e.Note, "ABCD") || e.Note == "" || len(e.Buttons) != 0 {
		t.Fatalf("note %q buttons %v", e.Note, e.Buttons)
	}
}

func TestApproveWithoutAKnownAddress(t *testing.T) {
	r := newRig(t, "")
	r.card(t)
	e, err := r.a.QuickActions()[edgeadmin.ActionApprove](context.Background(), r.press(edgeadmin.ActionApprove))
	if err != nil || !strings.Contains(e.Text, "your node's public address") {
		t.Fatalf("%q %v", e.Text, err)
	}
}

func TestStaleCardGivesNoCode(t *testing.T) {
	r := newRig(t, address)
	r.card(t)
	r.svc.digest = "d2" // the permissions changed after the card
	e, err := r.a.QuickActions()[edgeadmin.ActionApprove](context.Background(), r.press(edgeadmin.ActionApprove))
	if err != nil || strings.Contains(e.Text, secret) || !strings.Contains(e.Text, "changed") || strings.Contains(e.Note, secret) {
		t.Fatalf("stale: %q %q %v", e.Text, e.Note, err)
	}
}

func TestCancelIssuesNothing(t *testing.T) {
	r := newRig(t, address)
	r.card(t)
	before := r.svc.count()
	e, err := r.a.QuickActions()[edgeadmin.ActionCancel](context.Background(), r.press(edgeadmin.ActionCancel))
	if err != nil || r.svc.count() != before || !strings.Contains(e.Text, "nothing was made") {
		t.Fatalf("cancel: %q %v, calls %v", e.Text, err, r.svc.calls)
	}
}

func TestNonAdminCannotPressApproveOrRevoke(t *testing.T) {
	r := newRig(t, address)
	r.card(t)
	r.svc.edges = []edgejoin.Edge{{Name: "home", Joined: time.Now()}}
	if _, _, err := r.a.Card(context.Background(), r.speak(), owner, ownerC, time.UTC); err != nil {
		t.Fatal(err)
	}
	before := r.svc.count()
	for _, action := range []string{edgeadmin.ActionApprove, edgeadmin.ActionRevoke} {
		p := r.press(action)
		p.Button.Principal = other
		if _, err := r.a.QuickActions()[action](context.Background(), p); err == nil {
			t.Errorf("%s: a non-admin pressed it", action)
		}
	}
	if r.svc.count() != before || len(r.svc.edges) != 1 {
		t.Fatalf("the service was reached: %v", r.svc.calls[before:])
	}
}

func TestEdgesCardAndRevokeTwice(t *testing.T) {
	r := newRig(t, address)
	ctx := context.Background()
	text, buttons, err := r.a.Card(ctx, r.speak(), owner, ownerC, time.UTC)
	if err != nil || len(buttons) != 0 || !strings.Contains(text, "No computer") {
		t.Fatalf("empty: %q %v", text, err)
	}
	r.svc.edges = []edgejoin.Edge{{Name: "home", Joined: time.Date(2026, 11, 20, 9, 5, 0, 0, time.UTC), Digest: "d1", Key: "UKEY"}}
	text, buttons, err = r.a.Card(ctx, r.speak(), owner, ownerC, time.UTC)
	if err != nil || len(buttons) != 1 || !strings.Contains(text, "`home`") || !strings.Contains(text, "20 Nov 09:05") ||
		!strings.Contains(text, "digest") || strings.Contains(text, "UKEY") || strings.Contains(text, "d1") {
		t.Fatalf("card: %q %v %v", text, buttons, err)
	}
	revoke := r.a.QuickActions()[edgeadmin.ActionRevoke]
	p := r.press(edgeadmin.ActionRevoke)
	e, err := revoke(ctx, p)
	if err != nil || !strings.Contains(e.Text, "revoked") || !strings.Contains(e.Note, "revoked the edge home") {
		t.Fatalf("revoke: %+v %v", e, err)
	}
	e, err = revoke(ctx, p) // the second press finds it gone
	if err != nil || !strings.Contains(e.Text, "gone already") {
		t.Fatalf("revoke again: %+v %v", e, err)
	}
}

func TestRevokeFailureLeavesAButtonToPressAgain(t *testing.T) {
	r := newRig(t, address)
	ctx := context.Background()
	r.svc.edges = []edgejoin.Edge{{Name: "home", Joined: time.Now()}}
	if _, _, err := r.a.Card(ctx, r.speak(), owner, ownerC, time.UTC); err != nil {
		t.Fatal(err)
	}
	r.svc.revokeErr = errors.New("bus down")
	e, err := r.a.QuickActions()[edgeadmin.ActionRevoke](ctx, r.press(edgeadmin.ActionRevoke))
	if err != nil || !strings.Contains(e.Text, "Press Revoke again") || len(e.Buttons) != 1 {
		t.Fatalf("failure: %+v %v", e, err)
	}
	if _, ok := r.buttons.m[e.Buttons[0].ID]; !ok {
		t.Fatal("the new button is not stored")
	}
}

func TestEveryAdminGetsTheJoinNotice(t *testing.T) {
	cat, err := texts.New()
	if err != nil {
		t.Fatal(err)
	}
	var sent []event.Outgoing
	n := edgeadmin.Notice{
		Admins: alert.Admins{
			List: func(context.Context) ([]string, error) {
				return []string{owner, "telegram:user:8", "telegram:user:9"}, nil
			},
			Private: func(p string) (string, string, bool) {
				if p == "telegram:user:9" {
					return "", "", false // no private chat
				}
				return "telegram", "telegram:" + p[len("telegram:user:"):], true
			},
		},
		Enqueue:  func(_ context.Context, m ...event.Outgoing) error { sent = append(sent, m...); return nil },
		Lang:     func(context.Context, string) (texts.Lang, error) { return texts.English, nil },
		Texts:    cat,
		Location: time.UTC,
	}
	n.Joined(context.Background(), edgejoin.Edge{Name: "home", Key: "UKEYSECRET", Joined: time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)})
	if len(sent) != 2 || sent[0].Conv == sent[1].Conv {
		t.Fatalf("sent %+v", sent)
	}
	for _, m := range sent {
		if !strings.Contains(m.Text, "`home`") || !strings.Contains(m.Text, "12:00") || strings.Contains(m.Text, "UKEYSECRET") {
			t.Errorf("notice %q", m.Text)
		}
	}
}

func TestUnavailableTakesNoEdges(t *testing.T) {
	r := newRig(t, address)
	r.a = edgeadmin.New(edgeadmin.Deps{Service: edgeadmin.Unavailable{}, Buttons: r.buttons, Texts: r.cat,
		Lang:    func(context.Context, string) (texts.Lang, error) { return texts.English, nil },
		IsAdmin: func(context.Context, string) (bool, error) { return true, nil }, Address: func() string { return "" },
		Private: func(string) (string, string, bool) { return "telegram", ownerC, true }, Location: time.UTC})
	_, err := r.tool(t).Run(context.Background(), r.call(owner, ownerC, `{"name":"home"}`))
	if err == nil || !strings.Contains(err.Error(), "cannot take edges") {
		t.Fatalf("%v", err)
	}
}
