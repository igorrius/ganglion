package journal

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

const redacted = "[redacted]"

var (
	// secretName matches a key whose value is a secret.
	secretName = regexp.MustCompile(`(?i)token|secret|password|key`)
	// secretPair finds name=value, name: value and "name": "value" in text
	// that is not valid JSON; the value is the quoted string or the run up
	// to a separator.
	secretPair = regexp.MustCompile(`(?i)([\w.-]*(?:token|secret|password|key)[\w.-]*)("?\s*[=:]\s*)("(?:[^"\\]|\\.)*"|'[^']*'|[^\s,}&]+)`)
	// botToken is a Telegram Bot API token, with or without the "bot"
	// prefix of a request URL.
	botToken = regexp.MustCompile(`(?:bot)?\d{6,}:[A-Za-z0-9_-]{30,}|bot\d+:[A-Za-z0-9_-]+`)
)

// Redact removes secrets from s: the values of keys named like *token*,
// *secret*, *password* or *key*, whether s is JSON or plain text, and Bot
// API tokens anywhere. It is applied before anything is journalled
// (design section 4, rule 5).
func Redact(s string) string {
	if out, ok := redactJSON(s); ok {
		s = out
	} else {
		s = secretPair.ReplaceAllString(s, "${1}${2}"+redacted)
	}
	return botToken.ReplaceAllString(s, redacted)
}

// redactJSON redacts s when it is a JSON document; ok is false otherwise.
func redactJSON(s string) (string, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return "", false
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(redactValue(v)); err != nil {
		return "", false
	}
	return strings.TrimSuffix(buf.String(), "\n"), true
}

// redactValue replaces the value of every secret key, at any depth.
func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			if secretName.MatchString(k) {
				t[k] = redacted
			} else {
				t[k] = redactValue(val)
			}
		}
	case []any:
		for i, val := range t {
			t[i] = redactValue(val)
		}
	}
	return v
}
