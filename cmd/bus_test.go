package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/secrets"
)

// `ganglion bus trust` makes the chain in the store and prints the server's
// configuration: the operator's JWT and the system account, never a seed;
// a second run prints the same chain.
func TestBusTrustPrintsThePublicChainOnly(t *testing.T) {
	db := filepath.Join(t.TempDir(), "node.db")
	run := func() string {
		var out, errOut bytes.Buffer
		if err := newRootIO(nil, &out, &errOut, nil).Run(context.Background(), []string{"ganglion", "bus", "trust", "--db", db}); err != nil {
			t.Fatalf("bus trust: %v\n%s", err, errOut.String())
		}
		return out.String()
	}
	first := run()
	v, _ := storeOf(t, db)
	stored, err := v.Internal().Get(context.Background(), secrets.ScopeNode, natsbus.ChainName)
	if err != nil || stored == "" {
		t.Fatalf("the chain is not stored: %v", err)
	}
	var seeds map[string]string
	if err := json.Unmarshal([]byte(stored), &seeds); err != nil || len(seeds) != 5 {
		t.Fatalf("stored chain: %d seeds, %v", len(seeds), err)
	}
	for role, seed := range seeds {
		if strings.Contains(first, seed) {
			t.Fatalf("the output shows the %s seed", role)
		}
	}

	c, err := natsbus.ReadChain(context.Background(), v.Internal(), v.Locked, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	op, err := c.OperatorJWT()
	if err != nil {
		t.Fatal(err)
	}
	// The JWT's claims part names the operator; its iat may differ by a second.
	head := op[:strings.IndexByte(op, '.')]
	if !strings.Contains(first, "operator: "+head) || !strings.Contains(first, "system_account: "+c.SystemAccountPublicKey()) || !strings.Contains(first, "type: full") {
		t.Fatalf("output:\n%s", first)
	}
	if second := run(); !strings.Contains(second, "system_account: "+c.SystemAccountPublicKey()) {
		t.Fatal("a second run made another chain")
	}
}

// Beside a running node (its database locked) `bus trust` only reads the
// chain the node made, and never makes one.
func TestBusTrustBesideARunningNodeOnlyReads(t *testing.T) {
	db := filepath.Join(t.TempDir(), "node.db")
	run := func() (string, error) {
		var out, errOut bytes.Buffer
		err := newRootIO(nil, &out, &errOut, nil).Run(context.Background(), []string{"ganglion", "bus", "trust", "--db", db})
		return out.String(), err
	}
	release, err := node.LockDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := run(); !errors.Is(err, natsbus.ErrNoStoredChain) {
		t.Fatalf("with no chain: %v", err)
	}
	release()
	made, err := run()
	if err != nil {
		t.Fatal(err)
	}
	release, err = node.LockDB(db)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	read, err := run()
	if err != nil || !strings.Contains(read, "system_account: ") || read[strings.Index(read, "system_account"):strings.Index(read, "resolver:")] != made[strings.Index(made, "system_account"):strings.Index(made, "resolver:")] {
		t.Fatalf("beside a node: %v\n%s", err, read)
	}
}
