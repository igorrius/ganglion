// Package redact removes secrets from text before it is stored or shown:
// the journal, the logs, the questions the owner is asked, and the
// diagnostics (design section 4, rule 5).
package redact

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

const redacted = "[redacted]"

var (
	// secretName matches a key whose value is a secret.
	secretName = regexp.MustCompile(`(?i)token|secret|password|passwd|key|authorization|cookie`)
	// secretPair finds name=value, name: value and "name": "value" in text
	// that is not valid JSON; the value is the quoted string or the run up
	// to a separator.
	secretPair = regexp.MustCompile(`(?i)([\w.-]*(?:token|secret|password|passwd|key|authorization|cookie)[\w.-]*)("?\s*[=:]\s*)("(?:[^"\\]|\\.)*"|'[^']*'|[^\s,}&]+)`)
	// headerLine is a credential header, whole line: its value may hold
	// spaces and several pairs (a cookie).
	headerLine = regexp.MustCompile(`(?i)(\b(?:authorization|cookie|set-cookie)\s*:\s*)[^\r\n]+`)
	// bearer is a bearer credential wherever it appears.
	bearer = regexp.MustCompile(`(?i)(bearer\s+)[\w.~+/=-]+`)
	// userinfo is the password in https://user:password@host/.
	userinfo = regexp.MustCompile(`(?i)(https?://[^/\s:@]+:)[^@\s/]+@`)
	// botToken is a Telegram Bot API token, with or without the "bot"
	// prefix of a request URL.
	botToken = regexp.MustCompile(`(?:bot)?\d{6,}:[A-Za-z0-9_-]{30,}|bot\d+:[A-Za-z0-9_-]+`)
)

// String removes secrets from s: the values of keys named like *token*,
// *secret*, *password*, *passwd*, *key*, *authorization* or *cookie*,
// whether s is JSON or plain text; the same pairs, bearer tokens and
// passwords in addresses inside JSON strings; and Bot API tokens
// anywhere. It is applied before anything is journalled
// or logged (design section 4, rule 5).
func String(s string) string {
	if out, ok := redactJSON(s); ok {
		return out
	}
	return redactText(s)
}

// redactText redacts secrets written in text: credential headers, bearer
// tokens, passwords in addresses, name=value pairs and Bot API tokens.
func redactText(s string) string {
	s = headerLine.ReplaceAllString(s, "${1}"+redacted)
	s = bearer.ReplaceAllString(s, "${1}"+redacted)
	s = userinfo.ReplaceAllString(s, "${1}"+redacted+"@")
	s = secretPair.ReplaceAllString(s, "${1}${2}"+redacted)
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
	case string:
		return redactText(t)
	}
	return v
}

// minValue is the shortest secret Values hides: a shorter value would
// blank out ordinary words, and no real token or key is that short.
const minValue = 6

// Values returns a function that replaces every exact occurrence of the
// given secret values with "[redacted]": the secrets the node read, which
// no pattern may recognise (a provider key named by key_env). Empty and
// very short values are skipped; a longer value wins over its prefix.
func Values(vs ...string) func(string) string {
	keep := make([]string, 0, len(vs))
	for _, v := range vs {
		if len(v) >= minValue {
			keep = append(keep, v)
		}
	}
	if len(keep) == 0 {
		return func(s string) string { return s }
	}
	slices.SortFunc(keep, func(a, b string) int { return len(b) - len(a) })
	pairs := make([]string, 0, 2*len(keep))
	for _, v := range keep {
		pairs = append(pairs, v, redacted)
	}
	r := strings.NewReplacer(pairs...)
	return r.Replace
}
