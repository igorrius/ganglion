package mcpadmin_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/mcpadmin"
	"github.com/igorrius/ganglion/mcpclient"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
)

// fakeVault holds values by scope and name and refuses a long one, as the
// store does.
type fakeVault map[secrets.Scope]map[string]string

func (v fakeVault) Get(_ context.Context, scope secrets.Scope, name string) (string, error) {
	return v[scope][name], nil
}

func (v fakeVault) Set(_ context.Context, scope secrets.Scope, name, value, _ string) error {
	if len(value) > vault.MaxValue {
		return vault.ErrTooLong
	}
	if v[scope] == nil {
		v[scope] = map[string]string{}
	}
	v[scope][name] = value
	return nil
}

func (v fakeVault) RemoveScope(_ context.Context, scope secrets.Scope) (int, error) {
	n := len(v[scope])
	delete(v, scope)
	return n, nil
}

// A sign-in's record longer than the store takes in one value is kept in
// parts and read back whole; Remove drops the whole scope mcp:<server>.
func TestVaultStoreKeepsALongValueInParts(t *testing.T) {
	ctx := context.Background()
	v := fakeVault{}
	s := mcpadmin.VaultStore{V: v}
	short, long := `{"client":"c"}`, `{"token":"`+strings.Repeat("x", 3*vault.MaxValue)+`"}`
	for name, value := range map[string]string{"short": short, "oauth": long} {
		if err := s.Set(ctx, "shop", name, value); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Get(ctx, "shop", name); err != nil || got != value {
			t.Fatalf("%s: got %d bytes, %v", name, len(got), err)
		}
	}
	if len(v["mcp:shop"]) != 2+4 {
		t.Fatalf("stored names %d", len(v["mcp:shop"]))
	}
	if err := s.Set(ctx, "shop", "oauth", short); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(ctx, "shop", "oauth"); got != short {
		t.Fatalf("a shorter value read back as %d bytes", len(got))
	}
	if err := s.Set(ctx, "shop", "big", strings.Repeat("x", 17*vault.MaxValue)); err == nil {
		t.Fatal("a value past 16 parts was stored")
	}
	if err := s.Remove(ctx, "shop"); err != nil || len(v["mcp:shop"]) != 0 {
		t.Fatalf("Remove: %v, left %v", err, v["mcp:shop"])
	}
}

// A key goes in its header; without one the server needs it entered again.
func TestStoreKeysAuth(t *testing.T) {
	ctx := context.Background()
	store := mcpadmin.VaultStore{V: fakeVault{}}
	auth := mcpadmin.StoreKeys{Store: store}.Auth("other", "X-API-Key")
	if _, _, err := auth.Header(ctx); !errors.Is(err, mcpclient.ErrUnauthorized) {
		t.Fatalf("no key: %v", err)
	}
	if err := store.Set(ctx, "other", "X-API-Key", "k-123"); err != nil {
		t.Fatal(err)
	}
	if name, value, err := auth.Header(ctx); err != nil || name != "X-API-Key" || value != "k-123" {
		t.Fatalf("Header = %q %q %v", name, value, err)
	}
}
