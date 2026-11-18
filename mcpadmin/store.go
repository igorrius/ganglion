package mcpadmin

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
)

// scopePrefix starts the secret store's scope of a server (R-MCP-5).
const scopePrefix = "mcp:"

// Scope is the secret store's scope of the server called server:
// mcp:<server>, which holds its sign-in and its key.
func Scope(server string) secrets.Scope { return secrets.Scope(scopePrefix + server) }

// Vault is the secret store as the servers' sign-ins use it (vault.Vault).
type Vault interface {
	Get(ctx context.Context, scope secrets.Scope, name string) (string, error)
	Set(ctx context.Context, scope secrets.Scope, name, value, by string) error
	RemoveScope(ctx context.Context, scope secrets.Scope) (int, error)
}

// setBy is who the store records as having set a server's value.
const setBy = "node"

// chunked starts the value of a name that is stored in parts: the marker,
// then the number of parts, named <name>.0, <name>.1 and on. A sign-in's
// record (the registration with its tokens) can be longer than the store
// takes in one value (vault.MaxValue); no value the node stores starts
// with a control character.
const chunked = "\x01parts:"

// maxParts bounds a value's parts: 16 of 4 KiB, past the longest record
// mcpclient keeps.
const maxParts = 16

// VaultStore is mcpclient.Store over the secret store: each server's values
// in its own scope, mcp:<server>, a long one in parts.
type VaultStore struct {
	V Vault
}

// Get implements mcpclient.Store.
func (s VaultStore) Get(ctx context.Context, server, name string) (string, error) {
	v, err := s.V.Get(ctx, Scope(server), name)
	if err != nil || !strings.HasPrefix(v, chunked) {
		return v, err
	}
	n, err := strconv.Atoi(strings.TrimPrefix(v, chunked))
	if err != nil || n < 1 || n > maxParts {
		return "", fmt.Errorf("mcpadmin: the stored %s of %s is damaged", name, server)
	}
	var b strings.Builder
	for i := range n {
		part, err := s.V.Get(ctx, Scope(server), partName(name, i))
		if err != nil {
			return "", err
		}
		b.WriteString(part)
	}
	return b.String(), nil
}

// Set implements mcpclient.Store: a value the store takes whole is one
// value; a longer one goes in parts, written before the head that names
// them.
func (s VaultStore) Set(ctx context.Context, server, name, value string) error {
	if len(value) <= vault.MaxValue && !strings.HasPrefix(value, chunked) {
		return s.V.Set(ctx, Scope(server), name, value, setBy)
	}
	parts := split(value, vault.MaxValue)
	if len(parts) > maxParts {
		return fmt.Errorf("mcpadmin: the %s of %s is too long to store", name, server)
	}
	for i, p := range parts {
		if err := s.V.Set(ctx, Scope(server), partName(name, i), p, setBy); err != nil {
			return err
		}
	}
	return s.V.Set(ctx, Scope(server), name, chunked+strconv.Itoa(len(parts)), setBy)
}

// partName is the name of a value's part i.
func partName(name string, i int) string { return name + "." + strconv.Itoa(i) }

// split cuts s into parts of at most n bytes.
func split(s string, n int) []string {
	var out []string
	for len(s) > n {
		out = append(out, s[:n])
		s = s[n:]
	}
	return append(out, s)
}

// Remove implements mcpclient.Store: the whole scope.
func (s VaultStore) Remove(ctx context.Context, server string) error {
	_, err := s.V.RemoveScope(ctx, Scope(server))
	return err
}
