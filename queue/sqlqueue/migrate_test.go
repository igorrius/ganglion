package sqlqueue_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"testing/fstest"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/sqlitedb"
)

// Refs recorded before migration 006 get the copy from their outbox row; one
// whose row is gone is dropped.
func TestMigrationCopiesOlderRefs(t *testing.T) {
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	before := fstest.MapFS{}
	for _, name := range []string{"001_queue.sql", "002_inbox_interrupted.sql", "003_outbox_ref.sql",
		"004_inbox_taken_by.sql", "005_outbox_refs.sql"} {
		data, err := os.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		before[name] = &fstest.MapFile{Data: data}
	}
	if err := sqlitedb.Migrate(ctx, db, "queue", before); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO outbox (id, idem_key, transport, conv, body, status, created_ms, done_ms)
		 VALUES ('o1', 'k1', 'telegram', 'telegram:1',
		         '{"Text":"Apples are red.","Origin":{"kind":"task","session":"task:1"}}', 'sent', 1, 5)`,
		`INSERT INTO outbox_refs (transport, conv, ref, outbox_id) VALUES ('telegram', 'telegram:1', '10', 'o1')`,
		`INSERT INTO outbox_refs (transport, conv, ref, outbox_id) VALUES ('telegram', 'telegram:1', '11', 'gone')`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	q, err := sqlqueue.New(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := q.SentByRef(ctx, "telegram", "telegram:1", "10")
	want := event.Outgoing{Text: "Apples are red.", Origin: &event.Origin{Kind: event.OriginTask, Session: "task:1"}}
	if err != nil || !found || !reflect.DeepEqual(got, want) {
		t.Fatalf("an older ref: found %v, %+v, %v", found, got, err)
	}
	var sent int64
	if err := db.QueryRowContext(ctx, `SELECT sent_ms FROM outbox_refs WHERE ref = '10'`).Scan(&sent); err != nil || sent != 5 {
		t.Fatalf("sent_ms = %d, %v; want the row's done_ms", sent, err)
	}
	if _, found, err := q.SentByRef(ctx, "telegram", "telegram:1", "11"); err != nil || found {
		t.Fatalf("a ref whose row is gone: found %v, %v", found, err)
	}
}
