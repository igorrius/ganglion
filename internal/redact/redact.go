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
	// privateKey is a PEM private key block; one cut before its end
	// (output that was capped) is hidden to the end of the text.
	privateKey = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----(?s:.*?)(?:-----END [A-Z ]*PRIVATE KEY-----|\z)`)
	// tokenShape is a credential with a shape of its own, found with no
	// key name beside it: OpenAI and Anthropic keys (sk-...), GitHub tokens
	// (ghp_, gho_, ghu_, ghs_, ghr_, github_pat_), Slack tokens, AWS access
	// key ids, Google API keys and JSON web tokens.
	tokenShape = regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}` +
		`|\bgh[pousr]_[A-Za-z0-9]{20,}|\bgithub_pat_[A-Za-z0-9_]{20,}` +
		`|\bxox[abprs]-[A-Za-z0-9-]{10,}` +
		`|\bAKIA[0-9A-Z]{16}\b` +
		`|\bAIza[0-9A-Za-z_-]{35}` +
		`|\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]*`)
	// bareRun is a candidate for a bare token: a long run of token letters.
	bareRun = regexp.MustCompile(`[A-Za-z0-9_-]{32,}`)
)

// bareToken reports whether run looks like a random token: an upper case
// letter, a lower case letter and a digit together. A commit id or a digest
// (one case, hex), a word (no digit) and a path (not a run) do not.
func bareToken(run string) bool {
	var upper, lower, digit bool
	for _, r := range run {
		switch {
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= '0' && r <= '9':
			digit = true
		}
	}
	return upper && lower && digit
}

// String removes secrets from s: the values of keys named like *token*,
// *secret*, *password*, *passwd*, *key*, *authorization* or *cookie*,
// whether s is JSON or plain text; the same pairs, bearer tokens and
// passwords in addresses inside JSON strings; and Bot API tokens, PEM
// private keys and tokens of a known shape (sk-..., ghp_..., Slack, AWS and
// Google keys, JSON web tokens) or of a random look (32 characters or more
// with upper case, lower case and digits) anywhere. It is applied before anything is journalled
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
	s = botToken.ReplaceAllString(s, redacted)
	s = privateKey.ReplaceAllString(s, redacted)
	s = tokenShape.ReplaceAllString(s, redacted)
	return hideBare(s)
}

// hideBare replaces the bare tokens in s. A run that follows a slash is a
// path element (a directory named after a test, a file), not a token.
func hideBare(s string) string {
	var b strings.Builder
	last := 0
	for _, m := range bareRun.FindAllStringIndex(s, -1) {
		if m[0] > 0 && (s[m[0]-1] == '/' || s[m[0]-1] == '\\') || !bareToken(s[m[0]:m[1]]) {
			continue
		}
		b.WriteString(s[last:m[0]])
		b.WriteString(redacted)
		last = m[1]
	}
	b.WriteString(s[last:])
	return b.String()
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
// no pattern may recognise (a provider key). Empty and
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
