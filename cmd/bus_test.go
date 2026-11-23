package cmd

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/bus/natsbus"
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
	for _, name := range []string{natsbus.SeedOperator, natsbus.SeedAccount, natsbus.SeedSigning, natsbus.SeedSystem, natsbus.SeedCore} {
		seed, err := v.Get(context.Background(), secrets.ScopeNode, name)
		if err != nil || seed == "" {
			t.Fatalf("%s: %q, %v", name, seed, err)
		}
		if strings.Contains(first, seed) {
			t.Fatalf("the output shows %s", name)
		}
	}
	c, err := natsbus.LoadChain(context.Background(), v, v.Locked, time.Now)
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
