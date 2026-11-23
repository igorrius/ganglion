package natsbus

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// RedialLeaf drops the link and the server dials again with what the creds
// file holds at that moment: a file that is unusable keeps the edge
// unlinked, the good file brings it back.
func TestRedialLeafReadsTheCredsFileAgain(t *testing.T) {
	ctx := context.Background()
	hub, port := hubForEdges(t, filepath.Join(t.TempDir(), "hub"))
	creds := edgeCreds(t, hub, "e1", edgePerms("e1"))
	good, err := os.ReadFile(creds)
	if err != nil {
		t.Fatal(err)
	}
	edge := openEdge(t, filepath.Join(t.TempDir(), "edge"), "e1", "nats-leaf://127.0.0.1:"+itoa(port), creds)
	waitFor(t, func() bool { return edge.EdgeHealth(ctx).Linked })

	if err := os.WriteFile(creds, []byte("not a creds file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := edge.RedialLeaf(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return !edge.EdgeHealth(ctx).Linked })

	if err := os.WriteFile(creds, good, 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return edge.EdgeHealth(ctx).Linked })
}

func TestRedialLeafNeedsAnEdge(t *testing.T) {
	hub, _ := hubForEdges(t, filepath.Join(t.TempDir(), "hub"))
	if err := hub.RedialLeaf(); err == nil {
		t.Fatal("a core has no leaf link to redial")
	}
}
