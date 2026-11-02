package node

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func files(m map[string]string) func(string) ([]byte, error) {
	return func(name string) ([]byte, error) {
		if v, ok := m[name]; ok {
			return []byte(v), nil
		}
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}
}

func TestConfigFromEnv(t *testing.T) {
	cfg, err := ConfigFromEnv(env(nil), files(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg != (Config{DB: "ganglion.db", BotAPIURL: "https://api.telegram.org"}) {
		t.Fatalf("defaults = %+v", cfg)
	}
	cfg, err = ConfigFromEnv(env(map[string]string{
		"GANGLION_DB":        "/data/g.db",
		"TELEGRAM_BOT_TOKEN": "7001:abc",
		"TELEGRAM_API_URL":   "http://127.0.0.1:9",
		"GANGLION_ADMIN":     "telegram:user:123456",
	}), files(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{DB: "/data/g.db", BotToken: "7001:abc", BotAPIURL: "http://127.0.0.1:9", Admin: "telegram:user:123456"}
	if cfg != want {
		t.Fatalf("cfg = %+v, want %+v", cfg, want)
	}
	for _, bad := range []string{"123456", "telegram:123456", "telegram:user:", "telegram:user:12 34", "@igor"} {
		_, err := ConfigFromEnv(env(map[string]string{"GANGLION_ADMIN": bad}), files(nil))
		if err == nil || !strings.Contains(err.Error(), "GANGLION_ADMIN") || !strings.Contains(err.Error(), "telegram:user:") {
			t.Errorf("GANGLION_ADMIN=%q: %v, want an error naming the variable and the format", bad, err)
		}
	}
}

func TestConfigTokenFromFile(t *testing.T) {
	cfg, err := ConfigFromEnv(env(map[string]string{"TELEGRAM_BOT_TOKEN_FILE": "/run/secrets/tg"}),
		files(map[string]string{"/run/secrets/tg": "7001:from-file\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BotToken != "7001:from-file" {
		t.Fatalf("token %q, want the trailing newline trimmed", cfg.BotToken)
	}
	_, err = ConfigFromEnv(env(map[string]string{"TELEGRAM_BOT_TOKEN_FILE": "/run/secrets/missing"}), files(nil))
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN_FILE") || !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: %v, want an error naming the variable", err)
	}
	_, err = ConfigFromEnv(env(map[string]string{"TELEGRAM_BOT_TOKEN_FILE": "/run/secrets/empty"}),
		files(map[string]string{"/run/secrets/empty": "\n"}))
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty file: %v", err)
	}
}

func TestConfigSecretFileContentNotInErrors(t *testing.T) {
	// A file holding junk around the token must not echo it back.
	_, err := ConfigFromEnv(env(map[string]string{"TELEGRAM_BOT_TOKEN_FILE": "/f", "GANGLION_ADMIN": "bad"}),
		files(map[string]string{"/f": "7001:very-secret\n"}))
	if err == nil || strings.Contains(err.Error(), "very-secret") {
		t.Fatalf("error %v", err)
	}
}

func TestConfigTokenBothFormsRejected(t *testing.T) {
	_, err := ConfigFromEnv(env(map[string]string{"TELEGRAM_BOT_TOKEN": "7001:a", "TELEGRAM_BOT_TOKEN_FILE": "/f"}),
		files(map[string]string{"/f": "7001:b"}))
	if err == nil || !strings.Contains(err.Error(), "TELEGRAM_BOT_TOKEN") || !strings.Contains(err.Error(), "both") {
		t.Fatalf("both forms: %v", err)
	}
}

func TestConfigTokenWhitespaceTrimmed(t *testing.T) {
	cfg, err := ConfigFromEnv(env(map[string]string{"TELEGRAM_BOT_TOKEN": " 7001:abc\r\n"}), files(nil))
	if err != nil || cfg.BotToken != "7001:abc" {
		t.Fatalf("token %q, %v; want surrounding whitespace trimmed", cfg.BotToken, err)
	}
}
