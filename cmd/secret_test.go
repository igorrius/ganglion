package cmd

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/sqlitedb"
)

// cliSecret is the value the tests store; no output and no error may hold it.
const cliSecret = "abc123-cli-secret-value"

// secretRun runs `ganglion secret <args>` with stdin on standard input and
// fails the test when the value shows anywhere.
func secretRun(t *testing.T, stdin io.Reader, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := newRootIO(stdin, &out, &errOut, nil).Run(context.Background(), append([]string{"ganglion", "secret"}, args...))
	shown := out.String() + errOut.String()
	if err != nil {
		shown += err.Error()
	}
	if strings.Contains(shown, cliSecret) {
		t.Fatalf("secret %v shows the value:\n%s", args, shown)
	}
	return out.String(), err
}

// storeOf opens the store of the database at db on the key beside it.
func storeOf(t *testing.T, db string) (*vault.Vault, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	key, _, err := vault.LoadKey(secrets.None{}, vault.KeyPath(db))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sqlitedb.Open(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	v, err := vault.Open(ctx, conn, key, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	return v, conn
}

func TestSecretSetStoresTheValueFromStdin(t *testing.T) {
	clearEnv(t)
	ctx := context.Background()
	db := filepath.Join(t.TempDir(), "g.db")
	out, err := secretRun(t, strings.NewReader(cliSecret+"\n"), "set", "--db", db, "--module", "demo-reader", "DEMO_APP_ID")
	if err != nil || out != "module:local/demo-reader: DEMO_APP_ID is set\n" {
		t.Fatalf("set: %q, %v", out, err)
	}
	if info, err := os.Stat(vault.KeyPath(db)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the key file: %v, %v", info, err)
	}
	// The full name names the same module; no --module is the node's own.
	if _, err := secretRun(t, strings.NewReader("second-"+cliSecret), "set", "--db", db, "--module", "local/demo-reader", "DEMO_APP_ID"); err != nil {
		t.Fatal(err)
	}
	if out, err := secretRun(t, strings.NewReader(cliSecret), "set", "--db", db, "BOT"); err != nil || out != "node: BOT is set\n" {
		t.Fatalf("set the node's own: %q, %v", out, err)
	}
	v, _ := storeOf(t, db)
	for scope, want := range map[secrets.Scope][2]string{
		"module:local/demo-reader": {"DEMO_APP_ID", "second-" + cliSecret}, secrets.ScopeNode: {"BOT", cliSecret},
	} {
		if got, err := v.Get(ctx, scope, want[0]); err != nil || got != want[1] {
			t.Errorf("%s %s: stored right %v, %v", scope, want[0], got == want[1], err)
		}
	}
	list, err := v.List(ctx)
	if err != nil || len(list) != 2 || list[0].By != "cli" {
		t.Fatalf("List = %+v, %v", list, err)
	}
}

// endless is an input that never ends.
type endless struct{}

func (endless) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

func TestSecretSetRefusesWhatIsNotOneValueOnStdin(t *testing.T) {
	clearEnv(t)
	db := filepath.Join(t.TempDir(), "g.db")
	for name, tc := range map[string]struct {
		stdin io.Reader
		args  []string
		want  error
		says  string
	}{
		"a value as an argument": {strings.NewReader(""), []string{"NAME", cliSecret}, errSecretUsage, "standard input"},
		"no name":                {strings.NewReader(cliSecret), nil, errSecretUsage, "NAME"},
		"a bad name":             {strings.NewReader(cliSecret), []string{"1A"}, errSecretUsage, "name"},
		"empty input":            {strings.NewReader(""), []string{"NAME"}, vault.ErrEmpty, ""},
		"only whitespace":        {strings.NewReader(" \n\t\n"), []string{"NAME"}, vault.ErrEmpty, ""},
		"over the longest value": {strings.NewReader(strings.Repeat("x", vault.MaxValue+1)), []string{"NAME"}, vault.ErrTooLong, ""},
		"an endless input":       {endless{}, []string{"NAME"}, vault.ErrTooLong, ""},
	} {
		_, err := secretRun(t, tc.stdin, append([]string{"set", "--db", db}, tc.args...)...)
		if !errors.Is(err, tc.want) || !strings.Contains(err.Error(), tc.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := os.Stat(db); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a refused set made the database: %v", err)
	}
	// The longest value, with the newline a file ends with, is stored.
	if _, err := secretRun(t, strings.NewReader(strings.Repeat("x", vault.MaxValue)+"\r\n"), "set", "--db", db, "NAME"); err != nil {
		t.Fatalf("a value of MaxValue bytes: %v", err)
	}
}

func TestSecretListAndRm(t *testing.T) {
	clearEnv(t)
	db := filepath.Join(t.TempDir(), "g.db")
	for _, cmd := range [][]string{{"list", "--db", db}, {"rm", "--db", db, "BOT"}} {
		if _, err := secretRun(t, strings.NewReader(""), cmd...); err == nil || !strings.Contains(err.Error(), "no database at") {
			t.Fatalf("%v without a database: %v", cmd, err)
		}
	}
	for _, args := range [][]string{{"BOT"}, {"--module", "zeta", "Z"}, {"--module", "alpha", "B"}, {"--module", "alpha", "A"}} {
		if _, err := secretRun(t, strings.NewReader(cliSecret), append([]string{"set", "--db", db}, args...)...); err != nil {
			t.Fatal(err)
		}
	}
	out, err := secretRun(t, strings.NewReader(""), "list", "--db", db)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	want := []string{"module:local/alpha  A", "module:local/alpha  B", "module:local/zeta   Z", "node                BOT"}
	if len(lines) != len(want) {
		t.Fatalf("list:\n%s", out)
	}
	year := time.Now().Format("2006-")
	for i, line := range lines {
		if !strings.HasPrefix(line, want[i]) || !strings.Contains(line, year) || !strings.HasSuffix(line, "cli") {
			t.Errorf("line %d = %q, want %q with a date and who", i, line, want[i])
		}
	}

	if out, err := secretRun(t, strings.NewReader(""), "rm", "--db", db, "--module", "alpha", "A"); err != nil || out != "module:local/alpha: A is removed\n" {
		t.Fatalf("rm: %q, %v", out, err)
	}
	if out, err := secretRun(t, strings.NewReader(""), "rm", "--db", db, "--module", "alpha", "A"); err != nil || out != "module:local/alpha: no secret A\n" {
		t.Fatalf("rm again: %q, %v", out, err)
	}
	for _, name := range []string{"B", "Z"} {
		module := map[string]string{"B": "alpha", "Z": "zeta"}[name]
		if _, err := secretRun(t, strings.NewReader(""), "rm", "--db", db, "--module", module, name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := secretRun(t, strings.NewReader(""), "rm", "--db", db, "BOT"); err != nil {
		t.Fatal(err)
	}
	if out, err := secretRun(t, strings.NewReader(""), "list", "--db", db); err != nil || out != "no secrets are stored\n" {
		t.Fatalf("an empty list: %q, %v", out, err)
	}
}

// rows is every stored byte of the secrets, for comparing before and after.
func rows(t *testing.T, conn *sql.DB) string {
	t.Helper()
	var out strings.Builder
	rs, err := conn.Query(`SELECT scope, name, hex(nonce), hex(ciphertext) FROM secrets ORDER BY scope, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rs.Close() }()
	for rs.Next() {
		var a, b, c, d string
		if err := rs.Scan(&a, &b, &c, &d); err != nil {
			t.Fatal(err)
		}
		out.WriteString(a + " " + b + " " + c + " " + d + "\n")
	}
	return out.String()
}

// R-CFG-10: with a key that does not fit, set fails and says what to do,
// list still lists, and the stored rows are not touched.
func TestSecretCommandsOnALockedStore(t *testing.T) {
	clearEnv(t)
	db := filepath.Join(t.TempDir(), "g.db")
	if _, err := secretRun(t, strings.NewReader(cliSecret), "set", "--db", db, "--module", "demo-reader", "DEMO_APP_ID"); err != nil {
		t.Fatal(err)
	}
	_, conn := storeOf(t, db)
	before := rows(t, conn)

	t.Setenv(vault.KeyEnv, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, vault.KeySize)))
	_, err := secretRun(t, strings.NewReader(cliSecret), "set", "--db", db, "--module", "demo-reader", "OTHER")
	if !errors.Is(err, vault.ErrLocked) || !strings.Contains(err.Error(), "/secrets reset") {
		t.Fatalf("set on a locked store: %v", err)
	}
	out, err := secretRun(t, strings.NewReader(""), "list", "--db", db)
	if err != nil || !strings.Contains(out, "DEMO_APP_ID") || !strings.Contains(out, "the secret store is locked") {
		t.Fatalf("list on a locked store: %q, %v", out, err)
	}
	if after := rows(t, conn); after != before {
		t.Fatalf("a locked store changed its rows:\n%s\nwas:\n%s", after, before)
	}
}

// Ruling 2 of the plan: the command writes beside a running node, which
// holds the database's lock (ADR-0019).
func TestSecretSetWorksBesideARunningNode(t *testing.T) {
	clearEnv(t)
	db := filepath.Join(t.TempDir(), "g.db")
	unlock, err := node.LockDB(db)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := secretRun(t, strings.NewReader(cliSecret), "set", "--db", db, "BOT"); err != nil {
		t.Fatalf("set beside a node that holds the lock: %v", err)
	}
}

// The node's own keys cannot be set or removed from the command line.
func TestSecretCommandsRefuseReservedNames(t *testing.T) {
	clearEnv(t)
	db := filepath.Join(t.TempDir(), "g.db")
	if _, err := secretRun(t, strings.NewReader(cliSecret), "set", "--db", db, "bus.chain"); !errors.Is(err, vault.ErrReserved) {
		t.Fatalf("set: %v", err)
	}
	if _, err := secretRun(t, strings.NewReader(""), "rm", "--db", db, "bus.revoked"); !errors.Is(err, vault.ErrReserved) {
		t.Fatalf("rm: %v", err)
	}
}
