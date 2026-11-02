package node

import (
	"fmt"
	"regexp"
	"strings"
)

// Config is the node configuration. Until D-3 (YAML node config, M4) it
// comes from environment variables.
type Config struct {
	DB        string // GANGLION_DB, default "ganglion.db"
	BotToken  string // TELEGRAM_BOT_TOKEN or the file named by TELEGRAM_BOT_TOKEN_FILE; empty → no Telegram transport
	BotAPIURL string // TELEGRAM_API_URL, default "https://api.telegram.org"
	Admin     string // GANGLION_ADMIN, e.g. "telegram:user:123"; seeded as admin at start
}

var principalRE = regexp.MustCompile(`^[a-z][a-z0-9]*:user:[0-9A-Za-z_.-]+$`)

// ConfigFromEnv reads the configuration through getenv; readFile reads
// secret files (os.ReadFile).
func ConfigFromEnv(getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error) {
	cfg := Config{
		DB:        getenv("GANGLION_DB"),
		BotAPIURL: getenv("TELEGRAM_API_URL"),
		Admin:     getenv("GANGLION_ADMIN"),
	}
	if cfg.DB == "" {
		cfg.DB = "ganglion.db"
	}
	if cfg.BotAPIURL == "" {
		cfg.BotAPIURL = "https://api.telegram.org"
	}
	if cfg.Admin != "" && !principalRE.MatchString(cfg.Admin) {
		return Config{}, fmt.Errorf("GANGLION_ADMIN=%q is not a principal; want <transport>:user:<id>, e.g. telegram:user:123456", cfg.Admin)
	}
	var err error
	if cfg.BotToken, err = secret(getenv, readFile, "TELEGRAM_BOT_TOKEN"); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// secret reads a secret from the variable name, or from the file named by
// name_FILE (as Docker and Kubernetes mount secrets). A trailing newline
// is trimmed. Errors name the variable and never include the file content.
func secret(getenv func(string) string, readFile func(string) ([]byte, error), name string) (string, error) {
	value, file := getenv(name), getenv(name+"_FILE")
	switch {
	case value != "" && file != "":
		return "", fmt.Errorf("both %s and %s_FILE are set; set one", name, name)
	case file == "":
		return value, nil
	}
	b, err := readFile(file)
	if err != nil {
		// os.ReadFile errors name the path, never the content.
		return "", fmt.Errorf("%s_FILE: %w", name, err)
	}
	v := strings.TrimRight(string(b), "\r\n")
	if v == "" {
		return "", fmt.Errorf("%s_FILE: %s is empty", name, file)
	}
	return v, nil
}
