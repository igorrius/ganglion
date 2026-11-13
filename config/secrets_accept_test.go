package config

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/internal/testinputs"
	"github.com/igorrius/ganglion/modhost"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/runner/runnertest"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
	"github.com/samber/do/v2"
)

// tokenManifest is a module whose tool answers with its granted secret.
const tokenManifest = `name: probe
version: 1
capabilities:
  secret.get: [PROBE_TOKEN]
triggers:
  - tool: token
    about: Reads the module's token.
    output: {value: string, ok: boolean}
config:
  op_tool_token: {type: string, default: '{"op":"secret","name":"PROBE_TOKEN"}'}
`

// acceptSecret is the value the acceptance test stores.
const acceptSecret = "accept-stored-secret-7719"

// secretNode is a node on a database file with two modules that both name
// the secret PROBE_TOKEN: local/probe and local/other.
type secretNode struct {
	b   Bootstrap
	i   do.Injector
	reg *tool.Registry
}

func startSecretNode(t *testing.T, b Bootstrap) *secretNode {
	t.Helper()
	ctx := context.Background()
	i := New(ctx, b, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	n := &secretNode{b: b, i: i, reg: do.MustInvoke[*tool.Registry](i)}
	return n
}

// install puts the two modules into the node's store and starts them.
func (n *secretNode) install(t *testing.T, wasm []byte) {
	t.Helper()
	ctx := context.Background()
	st := do.MustInvoke[*modstore.Store](n.i)
	for _, name := range []string{"probe", "other"} {
		manifest := []byte(strings.Replace(tokenManifest, "name: probe", "name: "+name, 1))
		build := modstore.Build{Module: "local/" + name, Wasm: wasm, Manifest: manifest, Language: "go", By: "telegram:user:42"}
		if o, err := st.InstallLocal(ctx, build, modstore.Digest(wasm, manifest)); err != nil || o.Result != modstore.ResultInstalled {
			t.Fatalf("install %s: %+v, %v", name, o, err)
		}
		if err := do.MustInvoke[*modhost.Host](n.i).Reload(ctx, "local/"+name); err != nil {
			t.Fatal(err)
		}
	}
}

// token is what the module's secret.get answers: the value and whether it
// is set.
func (n *secretNode) token(t *testing.T, module string) (string, bool) {
	t.Helper()
	h, ok := n.reg.Lookup(module + ".token")
	if !ok {
		t.Fatalf("the tool of %s is not in the registry", module)
	}
	res, err := h.Run(context.Background(), tool.Call{Name: module + ".token", Args: json.RawMessage(`{}`), Admin: true})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Value string `json:"value"`
		OK    bool   `json:"ok"`
	}
	if err := json.Unmarshal([]byte(res.Text), &out); err != nil {
		t.Fatalf("the tool answered %q: %v", res.Text, err)
	}
	return out.Value, out.OK
}

// secretSet is what `ganglion secret set` does: another connection to the
// node's database, the key from the file beside it, one Set.
func secretSet(t *testing.T, b Bootstrap, scope secrets.Scope, name, value string) {
	t.Helper()
	ctx := context.Background()
	key, created, err := vault.LoadKey(secrets.None{}, b.MasterKey)
	if err != nil || created {
		t.Fatalf("the command's key: created %v, %v", created, err)
	}
	conn, err := sqlitedb.Open(ctx, b.DB)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	v, err := vault.Open(ctx, conn, key, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Set(ctx, scope, name, value, "cli"); err != nil {
		t.Fatal(err)
	}
}

// Task 6.5's "done when" (ADR-0077, R-CFG-9, R-CFG-10): a module's secret
// set from the command line is read by that module with no restart and by
// no other; a copy of the database without the key yields no value; with
// the wrong key the node starts locked and says so.
func TestTheSecretStoreAcceptance(t *testing.T) {
	testinputs.Build(t, "../runner/runnertest/probe/wasm", "GOOS=wasip1", "GOARCH=wasm")
	wasm, err := runnertest.BuildProbe()
	if err != nil {
		t.Fatal(err)
	}
	fake := telegramtest.NewServer(t, testToken)
	model := providertest.NewServer(t, "sk-test")
	ctx := context.Background()
	b := testConfig(t)
	b.BotToken, b.BotAPIURL, b.Admin = testToken, fake.URL(), "telegram:user:42"
	b.ModulesDir, b.MasterKey = t.TempDir(), vault.KeyPath(b.DB)
	b.Agent = AgentSettings{Models: parseModels(t, "providers:\n  - {name: or, type: openai, base_url: \""+model.URL()+
		"\", key: env:OR_KEY}\nmodels:\n  - {provider: or, name: m1, context: 64000}\npurposes: {chat: [{model: m1}]}\n"),
		Keys: map[string]string{"or": "sk-test"}, TZ: time.UTC, Workspace: t.TempDir()}
	n := startSecretNode(t, b)
	n.install(t, wasm)

	t.Run("set from the command line, read with no restart", func(t *testing.T) {
		if v, ok := n.token(t, "probe"); ok || v != "" {
			t.Fatalf("before it is set: %q, %v", v, ok)
		}
		secretSet(t, b, secrets.ModuleScope("local/probe"), "PROBE_TOKEN", acceptSecret)
		if v, ok := n.token(t, "probe"); !ok || v != acceptSecret {
			t.Fatalf("after it is set, with nothing restarted: got the value %v, set %v", v == acceptSecret, ok)
		}
	})

	t.Run("no other module reads it", func(t *testing.T) {
		if v, ok := n.token(t, "other"); ok || v != "" {
			t.Fatalf("another module that names the same secret got: set %v, the value %v", ok, v == acceptSecret)
		}
	})

	// A copy of the database in another folder, without the key file.
	copied := filepath.Join(t.TempDir(), "node.db")
	if _, err := do.MustInvoke[Database](n.i).ExecContext(ctx, `VACUUM INTO ?`, copied); err != nil {
		t.Fatal(err)
	}

	t.Run("a database copy without the key yields no value", func(t *testing.T) {
		raw, err := os.ReadFile(copied)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(raw, []byte(acceptSecret)) {
			t.Fatal("the database file holds the value in the clear")
		}
		key, created, err := vault.LoadKey(secrets.None{}, vault.KeyPath(copied))
		if err != nil || !created {
			t.Fatalf("a new key beside the copy: created %v, %v", created, err)
		}
		conn, err := sqlitedb.Open(ctx, copied)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = conn.Close() }()
		v, err := vault.Open(ctx, conn, key, time.Now)
		if err != nil || !v.Locked() {
			t.Fatalf("the copy on another key: locked %v, %v", v != nil && v.Locked(), err)
		}
		if got, err := v.Get(ctx, secrets.ModuleScope("local/probe"), "PROBE_TOKEN"); err != nil || got != "" {
			t.Fatalf("Get on the copy: empty %v, %v", got == "", err)
		}
	})

	t.Run("with the wrong key the node starts locked and says so", func(t *testing.T) {
		wrong := b
		wrong.DB, wrong.MasterKey = copied, vault.KeyPath(copied) // the key the subtest above made
		w := startSecretNode(t, wrong)
		if err := do.MustInvoke[*modhost.Host](w.i).Reload(ctx, "local/probe"); err != nil {
			t.Fatal(err)
		}
		if v, ok := w.token(t, "probe"); ok || v != "" {
			t.Fatalf("on a locked store the module got: set %v, the value %v", ok, v == acceptSecret)
		}
		var said bool
		for _, s := range do.MustInvoke[[]status.Source](w.i) {
			cs, _ := s.Status(ctx, status.Scope{})
			for _, c := range cs {
				said = said || (c.Line == texts.StatusSecretsLocked && c.State == status.Fail)
			}
		}
		if !said {
			t.Fatal("/status does not say the store is locked")
		}
		// The first node, on the right key, is not disturbed.
		if v, ok := n.token(t, "probe"); !ok || v != acceptSecret {
			t.Fatalf("the node on the right key lost the secret: set %v", ok)
		}
	})
}
