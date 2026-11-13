package config

import (
	"context"
	"log/slog"
	"slices"
	"testing"

	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/samber/do/v2"
)

// envOnly is the node's environment in these tests.
type envOnly map[string]string

func (e envOnly) Secret(name string) (string, error) { return e[name], nil }

// ADR-0077: a module reads its own scope of the store, then the node's
// environment; never another module's scope, never the node's.
func TestAModulesSecretsAreItsScopeThenTheEnvironment(t *testing.T) {
	ctx := context.Background()
	b := testConfig(t)
	b.Secrets = envOnly{"FROM_ENV": "env-value", "SHADOWED": "env-shadowed"}
	i := New(ctx, b, slog.New(slog.DiscardHandler))
	t.Cleanup(func() { _ = i.Shutdown() })
	v := do.MustInvoke[*vault.Vault](i)
	for _, s := range []struct {
		scope       secrets.Scope
		name, value string
	}{
		{secrets.ModuleScope("local/a"), "MINE", "a-value"},
		{secrets.ModuleScope("local/a"), "SHADOWED", "a-shadowed"},
		{secrets.ModuleScope("local/b"), "THEIRS", "b-value"},
		{secrets.ScopeNode, "NODES", "node-value"},
	} {
		if err := v.Set(ctx, s.scope, s.name, s.value, "cli"); err != nil {
			t.Fatal(err)
		}
	}
	src := secretsFor(b, v)("local/a")
	for name, want := range map[string]string{
		"MINE": "a-value", "SHADOWED": "a-shadowed", "FROM_ENV": "env-value", "THEIRS": "", "NODES": "", "NOWHERE": "",
	} {
		if got, err := src.Secret(name); err != nil || got != want {
			t.Errorf("local/a reads %s = %q, %v; want %q", name, got, err, want)
		}
	}

	// Removing a module drops its scope and no other.
	if err := forgetSecrets(v)(ctx, "local/a"); err != nil {
		t.Fatal(err)
	}
	left, err := v.List(ctx)
	if err != nil || len(left) != 2 {
		t.Fatalf("left after forgetting local/a: %v, %v", left, err)
	}
	for _, e := range left {
		if e.Scope == secrets.ModuleScope("local/a") {
			t.Fatalf("a secret of the removed module stays: %+v", e)
		}
	}
}

// The store's key is one of the node's own secrets: no manifest is granted
// the variable or its _FILE, and no skill script lists it.
func TestTheMasterKeyIsTheNodesOwnSecret(t *testing.T) {
	own := nodeSecrets(Bootstrap{})
	if !slices.Contains(own, vault.KeyEnv) {
		t.Fatalf("nodeSecrets = %v", own)
	}
	for _, name := range []string{vault.KeyEnv, vault.KeyEnv + "_FILE"} {
		if !modstore.NodeSecret(own, name) {
			t.Errorf("%s is not the node's own", name)
		}
	}
}
