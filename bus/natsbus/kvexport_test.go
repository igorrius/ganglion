package natsbus

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/bus"
)

// kvBus is an embedded bus with an empty cells bucket.
func kvBus(t *testing.T) *Bus {
	t.Helper()
	b := openEmbedded(t, "")
	if err := b.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	return b
}

// Deletes and purges are exported as lines and replayed, and an empty
// bucket exports nothing.
func TestExportImportKVOperations(t *testing.T) {
	ctx := context.Background()
	src := kvBus(t)
	var empty bytes.Buffer
	if n, err := src.ExportKV(ctx, bus.BucketCells, &empty); err != nil || n != 0 || empty.Len() != 0 {
		t.Fatalf("an empty bucket: %d lines, %q, %v", n, empty.String(), err)
	}
	kv, err := src.JetStream().KeyValue(ctx, bus.BucketCells)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []func() error{
		func() error { _, err := kv.Put(ctx, "a", []byte("1")); return err },
		func() error { _, err := kv.Put(ctx, "b", []byte("\x00binary\xff")); return err },
		func() error { _, err := kv.Put(ctx, "a", []byte("2")); return err },
		func() error { return kv.Delete(ctx, "b") },
		func() error { _, err := kv.Put(ctx, "c", []byte("3")); return err },
		func() error { return kv.Purge(ctx, "c") },
	} {
		if err := step(); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	n, err := src.ExportKV(ctx, bus.BucketCells, &out)
	if err != nil || n != 5 { // the purge rolled "c" up into one line
		t.Fatalf("export: %d lines, %v\n%s", n, err, out.String())
	}
	dst := kvBus(t)
	if m, err := dst.ImportKV(ctx, bus.BucketCells, bytes.NewReader(out.Bytes())); err != nil || m != n {
		t.Fatalf("import: %d, %v", m, err)
	}
	var again bytes.Buffer
	if _, err := dst.ExportKV(ctx, bus.BucketCells, &again); err != nil {
		t.Fatal(err)
	}
	if strip(out.String()) != strip(again.String()) {
		t.Fatalf("the second export differs:\n%s\n%s", out.String(), again.String())
	}
	kv2, err := dst.JetStream().KeyValue(ctx, bus.BucketCells)
	if err != nil {
		t.Fatal(err)
	}
	if e, err := kv2.Get(ctx, "a"); err != nil || string(e.Value()) != "2" {
		t.Fatalf("a = %v, %v", e, err)
	}
	if _, err := kv2.Get(ctx, "b"); err == nil {
		t.Fatal("b came back")
	}
}

// strip removes the "seq" of every line, which an import renumbers.
func strip(s string) string {
	var b strings.Builder
	for line := range strings.SplitSeq(strings.TrimSpace(s), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			return "bad: " + line
		}
		delete(m, "seq")
		out, _ := json.Marshal(m)
		b.Write(out)
		b.WriteByte('\n')
	}
	return b.String()
}

// A bad export leaves the bucket empty, so the next try is not refused.
func TestImportKVUndoesAFailedImport(t *testing.T) {
	ctx := context.Background()
	const first = `{"key":"a","seq":1,"op":"put","value":"MQ=="}` + "\n"
	for name, in := range map[string]string{
		"not json":      first + "not json\n",
		"unknown op":    first + `{"key":"b","seq":2,"op":"burn"}` + "\n",
		"out of order":  first + `{"key":"b","seq":1,"op":"put","value":"Mg=="}` + "\n",
		"unknown field": first + `{"key":"b","seq":2,"op":"put","extra":1}` + "\n",
		"a bad key":     first + `{"key":"a b","seq":2,"op":"put","value":"MQ=="}` + "\n",
	} {
		b := kvBus(t)
		if _, err := b.ImportKV(ctx, bus.BucketCells, strings.NewReader(in)); err == nil {
			t.Errorf("%s: no error", name)
			continue
		}
		if n, err := b.ImportKV(ctx, bus.BucketCells, strings.NewReader(first)); err != nil || n != 1 {
			t.Errorf("%s: a good import after it: %d, %v", name, n, err)
		}
	}
}
