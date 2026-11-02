// Package secrets is where a node reads its secrets: the bot token and
// the providers' API keys. P1 reads the environment and *_FILE files
// (Docker and Kubernetes secrets); an encrypted store comes later behind
// the same interface (decision D-3). Secrets never come from flags: argv
// shows in ps.
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
