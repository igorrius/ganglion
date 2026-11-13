package agent

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
)

// fakeSecrets is a SecretsAdmin over fixed entries that records its calls.
type fakeSecrets struct {
	entries []vault.Entry
	locked  bool
	removed []string // "scope name" of each Remove
	resets  int
	listed  int
}

func (f *fakeSecrets) List(context.Context) ([]vault.Entry, error) {
	f.listed++
	return f.entries, nil
}

func (f *fakeSecrets) Remove(_ context.Context, scope secrets.Scope, name string) (bool, error) {
	f.removed = append(f.removed, string(scope)+" "+name)
	return slices.ContainsFunc(f.entries, func(e vault.Entry) bool { return e.Scope == scope && e.Name == name }), nil
}

func (f *fakeSecrets) Reset(context.Context) (int, error) {
	f.resets++
	n := len(f.entries)
	f.entries, f.locked = nil, false
	return n, nil
}

func (f *fakeSecrets) Locked() bool { return f.locked }

func storedSecrets() *fakeSecrets {
	at := time.Date(2026, 11, 13, 21, 9, 0, 0, time.UTC)
	return &fakeSecrets{entries: []vault.Entry{
		{Scope: "module:local/deye-reader", Name: "DEYE_APP_ID", SetAt: at, By: "cli"},
		{Scope: "module:local/deye-reader", Name: "DEYE_APP_SECRET", SetAt: at.AddDate(0, 0, 1), By: "cli"},
		{Scope: secrets.ScopeNode, Name: "TELEGRAM_BOT_TOKEN", SetAt: at, By: "cli"},
	}}
}

func secretsCmdEnv(t *testing.T, s SecretsAdmin) *env {
	t.Helper()
	e := newEnv(t, Config{}, nil)
	e.deps.Secrets = s
	e.deps.Private = privateOf
	e.a = New(overDefaults(Config{}), e.deps)
	return e
}

// lastNote is the last message of the history when it is a command's note.
func lastNote(t *testing.T, e *env) string {
	t.Helper()
	msgs := e.state(t).Messages
	if len(msgs) == 0 || !strings.HasPrefix(msgs[len(msgs)-1].Content, commandPrefix) {
		return ""
	}
	return msgs[len(msgs)-1].Content
}

// /secrets lists the names by owner with the day each was set, with no
// model, no value and no note in the history (R-CFG-9).
func TestSecretsCommandLists(t *testing.T) {
	s := storedSecrets()
	e := secretsCmdEnv(t, s)
	e.smart.push(say("Hello."))
	e.turn(t, message(owner, "Igor", "hi"))
	calls, history := len(e.smart.requests()), len(e.state(t).Messages)
	out := e.turn(t, message(owner, "Igor", "/secrets"))
	want := "Stored secrets. Values are never shown.\n\ndeye-reader\n- DEYE\\_APP\\_ID, set 13.11\n- DEYE\\_APP\\_SECRET, set 14.11\n\n" +
		"The node's own\n- TELEGRAM\\_BOT\\_TOKEN, set 13.11"
	if len(out) != 1 || out[0].Text != want {
		t.Fatalf("list %q", out[0].Text)
	}
	if len(e.smart.requests()) != calls || len(e.state(t).Messages) != history {
		t.Fatal("/secrets reached the model or the history")
	}
}

func TestSecretsCommandEmptyLockedAndRefused(t *testing.T) {
	e := secretsCmdEnv(t, NoSecrets{})
	if out := e.turn(t, message(owner, "Igor", "/secrets")); len(out) != 1 || !strings.HasPrefix(out[0].Text, "No secrets are stored.") {
		t.Fatalf("empty %+v", out)
	}

	s := storedSecrets()
	s.locked = true
	e = secretsCmdEnv(t, s)
	out := e.turn(t, message(owner, "Igor", "/secrets"))
	if len(out) != 1 || !strings.HasPrefix(out[0].Text, "The store is locked") || !strings.Contains(out[0].Text, "DEYE\\_APP\\_ID") {
		t.Fatalf("locked %+v", out)
	}

	s = storedSecrets()
	e = secretsCmdEnv(t, s)
	ev := message(owner, "Igor", "/secrets")
	ev.Conv = "telegram:-100500" // a group
	if out := e.turn(t, ev); len(out) != 1 || !strings.Contains(out[0].Text, "private chat") || strings.Contains(out[0].Text, "DEYE") {
		t.Fatalf("in a group %+v", out)
	}
	e.admin = false
	if out := e.turn(t, message("telegram:user:7", "Olena", "/secrets reset confirm")); len(out) != 1 || out[0].Text != replyTasksNotAvailable {
		t.Fatalf("a family member got %+v", out)
	}
	if s.listed != 0 || s.resets != 0 || len(s.removed) != 0 {
		t.Fatalf("a refused /secrets reached the store: %+v", s)
	}
}

func TestSecretsCommandRemoves(t *testing.T) {
	s := storedSecrets()
	e := secretsCmdEnv(t, s)
	for cmd, want := range map[string]string{
		"/secrets rm deye-reader DEYE_APP_ID":       "deye-reader: DEYE\\_APP\\_ID is removed.",
		"/secrets rm local/deye-reader DEYE_APP_ID": "deye-reader: DEYE\\_APP\\_ID is removed.",
		"/secrets rm node TELEGRAM_BOT_TOKEN":       "The node's own: TELEGRAM\\_BOT\\_TOKEN is removed.",
		"/secrets rm deye-reader NOPE":              "deye-reader has no secret NOPE.",
	} {
		if out := e.turn(t, message(owner, "Igor", cmd)); len(out) != 1 || out[0].Text != want {
			t.Errorf("%s: %+v", cmd, out)
		}
	}
	slices.Sort(s.removed)
	if want := []string{"module:local/deye-reader DEYE_APP_ID", "module:local/deye-reader DEYE_APP_ID",
		"module:local/deye-reader NOPE", "node TELEGRAM_BOT_TOKEN"}; !slices.Equal(s.removed, want) {
		t.Fatalf("removed %q", s.removed)
	}

	// The history notes a removal by scope and name, and nothing else.
	e = secretsCmdEnv(t, storedSecrets())
	e.turn(t, message(owner, "Igor", "/secrets rm deye-reader DEYE_APP_ID"))
	if got, want := lastNote(t, e), "[command] the owner ran /secrets: removed the stored secret DEYE_APP_ID of deye-reader"; got != want {
		t.Fatalf("note %q", got)
	}
	e.turn(t, message(owner, "Igor", "/secrets rm deye-reader NOPE"))
	if got := lastNote(t, e); strings.Contains(got, "NOPE") {
		t.Fatalf("a removal of nothing left a note: %q", got)
	}

	s = storedSecrets()
	e = secretsCmdEnv(t, s)
	for _, cmd := range []string{"/secrets rm", "/secrets rm deye-reader", "/secrets rm deye-reader A B", "/secrets rm bad/../name X",
		"/secrets rm deye-reader 1BAD", "/secrets show", "/secrets get deye-reader DEYE_APP_ID"} {
		if out := e.turn(t, message(owner, "Igor", cmd)); len(out) != 1 || !strings.HasPrefix(out[0].Text, "Usage: /secrets") {
			t.Errorf("%s: %+v", cmd, out)
		}
	}
	if len(s.removed) != 0 {
		t.Fatalf("a wrong command removed %q", s.removed)
	}
}

// R-CFG-10: a reset says what it would drop and waits for the second
// command; only "reset confirm" drops.
func TestSecretsCommandResetsOnlyWhenConfirmed(t *testing.T) {
	s := storedSecrets()
	s.locked = true
	e := secretsCmdEnv(t, s)
	out := e.turn(t, message(owner, "Igor", "/secrets reset"))
	if len(out) != 1 || !strings.Contains(out[0].Text, "all 3 stored secrets") || !strings.Contains(out[0].Text, "/secrets reset confirm") {
		t.Fatalf("reset %+v", out)
	}
	if out := e.turn(t, message(owner, "Igor", "/secrets reset now")); len(out) != 1 || !strings.HasPrefix(out[0].Text, "Usage: /secrets") {
		t.Fatalf("reset now %+v", out)
	}
	if s.resets != 0 || lastNote(t, e) != "" {
		t.Fatalf("an unconfirmed reset: %d resets, note %q", s.resets, lastNote(t, e))
	}
	out = e.turn(t, message(owner, "Igor", "/secrets reset confirm"))
	if len(out) != 1 || out[0].Text != "Dropped 3 secrets. The store is open on the node's key." || s.resets != 1 {
		t.Fatalf("reset confirm %+v, %d resets", out, s.resets)
	}
	if got, want := lastNote(t, e), "[command] the owner ran /secrets: reset the secret store, dropping all 3 stored secrets"; got != want {
		t.Fatalf("note %q", got)
	}
}

func TestSecretsCommandHasHints(t *testing.T) {
	e := secretsCmdEnv(t, NoSecrets{})
	for _, c := range e.a.Commands() {
		if c.Name == "secrets" && len(c.Description) == 3 {
			return
		}
	}
	t.Fatal("/secrets is not listed with hints in three languages")
}
