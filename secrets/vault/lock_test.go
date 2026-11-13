package vault_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/texts"
)

// stored is every stored byte of the store, for comparing before and after.
func stored(t *testing.T, db *sql.DB) string {
	t.Helper()
	var out string
	for _, q := range []string{
		`SELECT scope || '/' || name, hex(nonce), hex(ciphertext) FROM secrets ORDER BY scope, name`,
		`SELECT key, hex(nonce), hex(ciphertext) FROM secret_meta ORDER BY key`,
	} {
		rows, err := db.Query(q)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var a, b, c string
			if err := rows.Scan(&a, &b, &c); err != nil {
				t.Fatal(err)
			}
			out += fmt.Sprintln(a, b, c)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestTheSameKeyOpensTheStoreAgain(t *testing.T) {
	ctx := context.Background()
	db := memory(t)
	set(t, open(t, db, vault.Key{1}, newClock()), scopeA, "TOKEN", value)
	var checks int
	if err := db.QueryRow(`SELECT count(*) FROM secret_meta WHERE key = 'check'`).Scan(&checks); err != nil || checks != 1 {
		t.Fatalf("check rows = %d, %v", checks, err)
	}
	again := open(t, db, vault.Key{1}, newClock())
	if again.Locked() {
		t.Fatal("the same key locked the store")
	}
	if got, err := again.Get(ctx, scopeA, "TOKEN"); err != nil || got != value {
		t.Fatalf("Get = %v, %v", got == value, err)
	}
}

func TestAnotherKeyLocksTheStoreAndChangesNothing(t *testing.T) {
	rec := telemetrytest.Install(t)
	ctx := context.Background()
	db := memory(t)
	first := open(t, db, vault.Key{1}, newClock())
	set(t, first, scopeA, "TOKEN", value)
	set(t, first, scopeA, "OTHER", value)
	before := stored(t, db)

	v := open(t, db, vault.Key{2}, newClock())
	if !v.Locked() {
		t.Fatal("another key did not lock the store")
	}
	if got, err := v.Get(ctx, scopeA, "TOKEN"); err != nil || got != "" {
		t.Fatalf("Get on a locked store = %q, %v", got, err)
	}
	if got, err := v.Source(scopeA).Secret("TOKEN"); err != nil || got != "" {
		t.Fatalf("Source on a locked store = %q, %v", got, err)
	}
	if err := v.Set(ctx, scopeA, "NEW", value, "cli"); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("Set on a locked store: %v", err)
	}
	if list, err := v.List(ctx); err != nil || len(list) != 2 {
		t.Fatalf("List on a locked store = %v, %v", list, err)
	}
	if after := stored(t, db); after != before {
		t.Fatalf("a locked store changed its rows:\n%s\nwas:\n%s", after, before)
	}
	for _, c := range []struct{ op, outcome string }{{"get", "locked"}, {"set", "locked"}} {
		if got := rec.Value(t, "ganglion.secrets.ops", map[string]string{"op": c.op, "outcome": c.outcome}); got == 0 {
			t.Errorf("secrets ops %s/%s is not counted", c.op, c.outcome)
		}
	}
	if ok, err := v.Remove(ctx, scopeA, "OTHER"); err != nil || !ok {
		t.Fatalf("Remove on a locked store = %v, %v", ok, err)
	}
	if first.Locked() {
		t.Fatal("the store on the right key became locked")
	}
}

func TestResetDropsTheSecretsAndAdoptsTheKey(t *testing.T) {
	ctx := context.Background()
	db := memory(t)
	first := open(t, db, vault.Key{1}, newClock())
	set(t, first, scopeA, "TOKEN", value)
	set(t, first, scopeB, "TOKEN", value)

	v := open(t, db, vault.Key{2}, newClock())
	if n, err := v.Reset(ctx); err != nil || n != 2 {
		t.Fatalf("Reset = %d, %v", n, err)
	}
	if list, err := v.List(ctx); err != nil || len(list) != 0 || v.Locked() {
		t.Fatalf("after Reset: %v, locked %v, %v", list, v.Locked(), err)
	}
	set(t, v, scopeA, "TOKEN", "new")
	if got, err := v.Get(ctx, scopeA, "TOKEN"); err != nil || got != "new" {
		t.Fatalf("Get after Reset = %q, %v", got, err)
	}
	if old := open(t, db, vault.Key{1}, newClock()); !old.Locked() {
		t.Fatal("the old key still opens a store that was reset on another")
	}
}

func TestResetOnAnOpenStoreDropsEverything(t *testing.T) {
	ctx := context.Background()
	db := memory(t)
	v := open(t, db, vault.Key{1}, newClock())
	set(t, v, scopeA, "TOKEN", value)
	if n, err := v.Reset(ctx); err != nil || n != 1 || v.Locked() {
		t.Fatalf("Reset = %d, locked %v, %v", n, v.Locked(), err)
	}
	if again := open(t, db, vault.Key{1}, newClock()); again.Locked() {
		t.Fatal("the key no longer fits after its own Reset")
	}
}

func TestTwoOpensOfANewStoreAgree(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "node.db")
	first, err := sqlitedb.Open(ctx, path) // the file exists once a node has started on it
	if err != nil {
		t.Fatal(err)
	}
	_ = first.Close()
	var (
		wg     sync.WaitGroup
		vaults [2]*vault.Vault
		errs   [2]error
	)
	for i := range vaults {
		wg.Go(func() {
			db, err := sqlitedb.Open(ctx, path)
			if err != nil {
				errs[i] = err
				return
			}
			t.Cleanup(func() { _ = db.Close() })
			vaults[i], errs[i] = vault.Open(ctx, db, vault.Key{1}, newClock().now)
		})
	}
	wg.Wait()
	for i, v := range vaults {
		if errs[i] != nil || v.Locked() {
			t.Fatalf("open %d: %v, locked %v", i, errs[i], v != nil && v.Locked())
		}
	}
}

func TestStatusSpeaksOnlyWhenLocked(t *testing.T) {
	ctx := context.Background()
	db := memory(t)
	v := open(t, db, vault.Key{1}, newClock())
	if v.Name() != "secrets" {
		t.Fatalf("Name = %q", v.Name())
	}
	if checks, err := v.Status(ctx, status.Scope{}); err != nil || len(checks) != 0 {
		t.Fatalf("Status of an open store = %v, %v", checks, err)
	}
	locked := open(t, db, vault.Key{2}, newClock())
	checks, err := locked.Status(ctx, status.Scope{})
	if err != nil || len(checks) != 1 {
		t.Fatalf("Status of a locked store = %v, %v", checks, err)
	}
	c := checks[0]
	if c.Section != status.SectionProblems || c.State != status.Fail ||
		c.Line != texts.StatusSecretsLocked || c.Reason != texts.StatusReasonSecretsKey {
		t.Fatalf("the line = %+v", c)
	}
}
