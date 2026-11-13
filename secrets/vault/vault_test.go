package vault_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
)

const (
	scopeA = secrets.Scope("module:local/a")
	scopeB = secrets.Scope("module:local/b")
	// value is the secret of the tests; no error and no listing may hold it.
	value = "s3cr3t-value-0042"
)

// clock is a time the test moves.
type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

func newClock() *clock { return &clock{at: time.Date(2026, 11, 13, 21, 9, 0, 0, time.UTC)} }

func memory(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sqlitedb.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func open(t *testing.T, db *sql.DB, key vault.Key, c *clock) *vault.Vault {
	t.Helper()
	v, err := vault.Open(context.Background(), db, key, c.now)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func set(t *testing.T, v *vault.Vault, scope secrets.Scope, name, val string) {
	t.Helper()
	if err := v.Set(context.Background(), scope, name, val, "cli"); err != nil {
		t.Fatal(err)
	}
}

func TestSetThenGet(t *testing.T) {
	ctx := context.Background()
	v := open(t, memory(t), vault.Key{1}, newClock())
	if got, err := v.Get(ctx, scopeA, "TOKEN"); err != nil || got != "" {
		t.Fatalf("Get of an unknown name = %q, %v", got, err)
	}
	set(t, v, scopeA, "TOKEN", value)
	if got, err := v.Get(ctx, scopeA, "TOKEN"); err != nil || got != value {
		t.Fatalf("Get = %v, %v", got == value, err)
	}
	if got, err := v.Source(scopeA).Secret("TOKEN"); err != nil || got != value {
		t.Fatalf("Source.Secret = %v, %v", got == value, err)
	}
}

func TestSetReplacesTheValueAndTheTime(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	v := open(t, memory(t), vault.Key{1}, c)
	set(t, v, scopeA, "TOKEN", "first")
	c.at = c.at.Add(time.Hour)
	if err := v.Set(ctx, scopeA, "TOKEN", "second", "telegram:7"); err != nil {
		t.Fatal(err)
	}
	list, err := v.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("List = %v, %v", list, err)
	}
	if e := list[0]; !e.SetAt.Equal(c.at) || e.By != "telegram:7" {
		t.Fatalf("the entry = %+v", e)
	}
	if got, _ := v.Get(ctx, scopeA, "TOKEN"); got != "second" {
		t.Fatalf("Get = %q", got)
	}
}

func TestScopesAreApart(t *testing.T) {
	ctx := context.Background()
	v := open(t, memory(t), vault.Key{1}, newClock())
	set(t, v, scopeA, "TOKEN", "for-a")
	set(t, v, scopeB, "TOKEN", "for-b")
	set(t, v, secrets.ScopeNode, "OTHER", "for-node")
	for scope, want := range map[secrets.Scope]string{scopeA: "for-a", scopeB: "for-b", secrets.ScopeNode: ""} {
		if got, err := v.Get(ctx, scope, "TOKEN"); err != nil || got != want {
			t.Errorf("Get in %s = %q, %v", scope, got, err)
		}
	}
}

func TestSetRefusesWithoutTheValueInTheError(t *testing.T) {
	ctx := context.Background()
	v := open(t, memory(t), vault.Key{1}, newClock())
	long := strings.Repeat("x", vault.MaxValue-len(value)) + value
	for name, tc := range map[string]struct {
		name, value string
		want        error
	}{
		"a bad name":       {"1A", value, vault.ErrName},
		"an empty name":    {"", value, vault.ErrName},
		"an empty value":   {"TOKEN", "", vault.ErrEmpty},
		"a value too long": {"TOKEN", long + "y", vault.ErrTooLong},
	} {
		err := v.Set(ctx, scopeA, tc.name, tc.value, "cli")
		if !errors.Is(err, tc.want) || strings.Contains(err.Error(), value) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := v.Set(ctx, scopeA, "TOKEN", long, "cli"); err != nil {
		t.Fatalf("a value of MaxValue bytes: %v", err)
	}
	if list, _ := v.List(ctx); len(list) != 1 {
		t.Fatalf("refused values were stored: %v", list)
	}
}

// row is the stored bytes of one secret.
func row(t *testing.T, db *sql.DB, scope secrets.Scope, name string) (nonce, ciphertext []byte) {
	t.Helper()
	err := db.QueryRow(`SELECT nonce, ciphertext FROM secrets WHERE scope = ? AND name = ?`, string(scope), name).Scan(&nonce, &ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	return nonce, ciphertext
}

func TestTheStoredBytesDoNotHoldTheValue(t *testing.T) {
	db := memory(t)
	v := open(t, db, vault.Key{1}, newClock())
	set(t, v, scopeA, "TOKEN", value)
	nonce1, sealed1 := row(t, db, scopeA, "TOKEN")
	set(t, v, scopeA, "TOKEN", value)
	nonce2, sealed2 := row(t, db, scopeA, "TOKEN")
	if bytes.Contains(sealed1, []byte(value)) || bytes.Contains(nonce1, []byte(value)) {
		t.Fatal("the row holds the value")
	}
	if len(nonce1) != 12 || bytes.Equal(nonce1, nonce2) || bytes.Equal(sealed1, sealed2) {
		t.Fatal("two Sets of one value gave the same nonce or ciphertext")
	}
}

func TestARowChangedInTheDatabaseDoesNotOpen(t *testing.T) {
	ctx := context.Background()
	for name, tc := range map[string]struct {
		change string
		scope  secrets.Scope
		name   string
	}{
		"moved to another scope": {`UPDATE secrets SET scope = 'module:local/b'`, scopeB, "TOKEN"},
		"moved to the node":      {`UPDATE secrets SET scope = 'node'`, secrets.ScopeNode, "TOKEN"},
		"renamed":                {`UPDATE secrets SET name = 'OTHER'`, scopeA, "OTHER"},
		"a flipped byte":         {`UPDATE secrets SET ciphertext = zeroblob(length(ciphertext))`, scopeA, "TOKEN"},
		"a short nonce":          {`UPDATE secrets SET nonce = x'00'`, scopeA, "TOKEN"},
	} {
		db := memory(t)
		v := open(t, db, vault.Key{1}, newClock())
		set(t, v, scopeA, "TOKEN", value)
		if _, err := db.Exec(tc.change); err != nil {
			t.Fatal(err)
		}
		got, err := v.Get(ctx, tc.scope, tc.name)
		switch {
		case err == nil || got != "":
			t.Errorf("%s: Get = %q, %v", name, got, err)
		case !strings.Contains(err.Error(), string(tc.scope)) || !strings.Contains(err.Error(), tc.name):
			t.Errorf("%s: the error does not name the scope and the name: %v", name, err)
		case strings.Contains(err.Error(), value):
			t.Errorf("%s: the error holds the value", name)
		}
	}
}

func TestRemove(t *testing.T) {
	ctx := context.Background()
	v := open(t, memory(t), vault.Key{1}, newClock())
	set(t, v, scopeA, "ONE", "1")
	set(t, v, scopeA, "TWO", "2")
	set(t, v, scopeB, "ONE", "3")
	if ok, err := v.Remove(ctx, scopeA, "ONE"); err != nil || !ok {
		t.Fatalf("Remove = %v, %v", ok, err)
	}
	if ok, err := v.Remove(ctx, scopeA, "ONE"); err != nil || ok {
		t.Fatalf("the second Remove = %v, %v", ok, err)
	}
	set(t, v, scopeA, "ONE", "1")
	if n, err := v.RemoveScope(ctx, scopeA); err != nil || n != 2 {
		t.Fatalf("RemoveScope = %d, %v", n, err)
	}
	list, err := v.List(ctx)
	if err != nil || len(list) != 1 || list[0].Scope != scopeB {
		t.Fatalf("List = %v, %v", list, err)
	}
}

func TestListIsOrderedAndHoldsNoValue(t *testing.T) {
	ctx := context.Background()
	c := newClock()
	v := open(t, memory(t), vault.Key{1}, c)
	set(t, v, secrets.ScopeNode, "BOT", value)
	set(t, v, scopeB, "Z", value)
	set(t, v, scopeA, "B", value)
	set(t, v, scopeA, "A", value)
	list, err := v.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []vault.Entry{
		{Scope: scopeA, Name: "A", SetAt: c.at, By: "cli"},
		{Scope: scopeA, Name: "B", SetAt: c.at, By: "cli"},
		{Scope: scopeB, Name: "Z", SetAt: c.at, By: "cli"},
		{Scope: secrets.ScopeNode, Name: "BOT", SetAt: c.at, By: "cli"},
	}
	if len(list) != len(want) {
		t.Fatalf("List = %v", list)
	}
	for i, e := range list {
		if e.Scope != want[i].Scope || e.Name != want[i].Name || !e.SetAt.Equal(want[i].SetAt) || e.By != want[i].By {
			t.Errorf("entry %d = %+v, want %+v", i, e, want[i])
		}
	}
}

func TestHasNeedsNoKeyAndNoTable(t *testing.T) {
	ctx := context.Background()
	if has, err := vault.Has(ctx, memory(t), scopeA, "TOKEN"); err != nil || has {
		t.Fatalf("Has without the table = %v, %v", has, err)
	}
	path := filepath.Join(t.TempDir(), "node.db")
	db, err := sqlitedb.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	set(t, open(t, db, vault.Key{1}, newClock()), scopeA, "TOKEN", value)
	ro, err := sqlitedb.OpenReadOnly(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ro.Close() }()
	for _, tc := range []struct {
		scope secrets.Scope
		name  string
		want  bool
	}{{scopeA, "TOKEN", true}, {scopeB, "TOKEN", false}, {scopeA, "OTHER", false}} {
		if has, err := vault.Has(ctx, ro, tc.scope, tc.name); err != nil || has != tc.want {
			t.Errorf("Has(%s, %s) = %v, %v", tc.scope, tc.name, has, err)
		}
	}
}

func TestOperationsAreCounted(t *testing.T) {
	rec := telemetrytest.Install(t)
	ctx := context.Background()
	v := open(t, memory(t), vault.Key{1}, newClock())
	set(t, v, scopeA, "TOKEN", value)
	_, _ = v.Get(ctx, scopeA, "TOKEN")
	_, _ = v.Get(ctx, scopeA, "OTHER")
	_ = v.Set(ctx, scopeA, "1A", value, "cli")
	_, _ = v.Remove(ctx, scopeA, "TOKEN")
	_, _ = v.Remove(ctx, scopeA, "TOKEN")
	for _, c := range []struct{ op, outcome string }{
		{"set", "ok"}, {"set", "error"}, {"get", "ok"}, {"get", "unset"}, {"remove", "ok"}, {"remove", "unset"},
	} {
		if got := rec.Value(t, "ganglion.secrets.ops", map[string]string{"op": c.op, "outcome": c.outcome}); got != 1 {
			t.Errorf("secrets ops %s/%s = %v, want 1", c.op, c.outcome, got)
		}
	}
}
