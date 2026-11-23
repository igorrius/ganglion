// Package secrets is where a node reads its secrets: the bot token and
// the providers' API keys, and where a module's secrets come from. This
// package holds the interface, the environment source (a variable or its
// *_FILE file, as Docker and Kubernetes hand secrets over) and the scopes
// of stored secrets; the encrypted store itself is secrets/vault
// (ADR-0077). Secrets never come from flags: argv shows in ps.
package secrets

import (
	"fmt"
	"os"
	"strings"
)

// Source returns the secret called name, or "" when it is not set.
// Errors name the secret and never include its value.
type Source interface {
	Secret(name string) (string, error)
}

// Env reads a secret from the variable name, or from the file named by
// name_FILE. Surrounding whitespace is trimmed: a value pasted with a
// newline or a CRLF .env line must not reach a transport, where
// whitespace defeats the token's redaction.
type Env struct {
	Getenv   func(string) string
	ReadFile func(string) ([]byte, error)
}

// OSEnv is Env over the process environment and file system.
func OSEnv() Env { return Env{Getenv: os.Getenv, ReadFile: os.ReadFile} }

// Secret implements Source.
func (e Env) Secret(name string) (string, error) {
	value, file := e.Getenv(name), e.Getenv(name+"_FILE")
	switch {
	case value != "" && file != "":
		return "", fmt.Errorf("both %s and %s_FILE are set; set one", name, name)
	case file == "":
		return strings.TrimSpace(value), nil
	}
	b, err := e.ReadFile(file)
	if err != nil {
		// os.ReadFile errors name the path, never the content.
		return "", fmt.Errorf("%s_FILE: %w", name, err)
	}
	v := strings.TrimSpace(string(b))
	if v == "" {
		return "", fmt.Errorf("%s_FILE: %s is empty", name, file)
	}
	return v, nil
}

// None is the Source of a node that reads no secrets beyond its file's:
// every secret is unset.
type None struct{}

// Secret implements Source.
func (None) Secret(string) (string, error) { return "", nil }

// Scope is who a stored secret belongs to (ADR-0077).
type Scope string

// ScopeNode holds the node's own secrets, named by store:NAME in the file.
const ScopeNode Scope = "node"

// modulePrefix starts the scope of a module.
const modulePrefix = "module:"

// ModuleScope is the scope of the module with this full name
// ("local/demo-reader" → "module:local/demo-reader").
func ModuleScope(module string) Scope { return Scope(modulePrefix + module) }

// serverPrefix starts the scope of a remote MCP server.
const serverPrefix = "mcp:"

// ServerScope is the scope of the remote MCP server with this name
// ("shop" → "mcp:shop"): its sign-in and the key of its header
// (docs/design/mcp-client-and-secret-intake-design.md, section 6).
func ServerScope(server string) Scope { return Scope(serverPrefix + server) }

// reservedPrefix starts the names of the node's own keys in its scope: the
// bus's trust chain (ADR-0113). No person, tool or module reads, sets or
// removes them; only the code that owns them does.
const reservedPrefix = "bus."

// Reserved reports whether name in scope is one of the node's own keys,
// which only their owner may touch.
func Reserved(scope Scope, name string) bool {
	return scope == ScopeNode && strings.HasPrefix(name, reservedPrefix)
}

// maxName is the longest name of a stored secret.
const maxName = 128

// ValidName reports whether name can name a stored secret:
// ^[A-Za-z_][A-Za-z0-9_.-]{0,127}$.
func ValidName(name string) bool {
	if name == "" || len(name) > maxName {
		return false
	}
	for i := range len(name) {
		if !nameByte(name[i], i == 0) {
			return false
		}
	}
	return true
}

// nameByte reports whether c may stand in a name; the first byte is a
// letter or an underscore.
func nameByte(c byte, first bool) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c == '_':
		return true
	case first:
		return false
	}
	return c >= '0' && c <= '9' || c == '.' || c == '-'
}

// First reads each source in order and returns the first value that is
// not empty. An error of a source ends the search and is returned.
type First []Source

// Secret implements Source.
func (f First) Secret(name string) (string, error) {
	for _, src := range f {
		v, err := src.Secret(name)
		if err != nil || v != "" {
			return v, err
		}
	}
	return "", nil
}
