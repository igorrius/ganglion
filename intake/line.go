package intake

import (
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Kind is what a request or a line carries.
type Kind string

// Kinds of request.
const (
	KindOAuth  Kind = "oauth"  // a one-time authorization code
	KindSecret Kind = "secret" // a value sealed to the request's key
)

// Limits of a line (R-INT-3, R-INT-4).
const (
	maxLine     = 4096 // bytes of one pasted line
	maxCode     = 2048 // decoded bytes of an OAuth code
	maxIssuer   = 512  // decoded bytes of an issuer
	maxValue    = 2048 // bytes of a secret through the page
	pointLen    = 65   // an uncompressed P-256 point: the HPKE encapsulated key
	tagLen      = 16   // the AES-GCM tag
	minSealed   = pointLen + 1 + tagLen
	maxSealed   = pointLen + maxValue + tagLen
	lineVersion = "1"
)

// The ways a line is refused. None of them carries the line or a part of it.
var (
	// ErrNotALine: the text is not one intake line of any version.
	ErrNotALine = errors.New("intake: not an intake line")
	// ErrVersion: a gauthN or gsecN line with N other than 1.
	ErrVersion = errors.New("intake: unknown line version")
	// ErrMalformed: an intake line of this version that does not parse.
	ErrMalformed = errors.New("intake: malformed line")
)

var (
	prefixRE = regexp.MustCompile(`(?i)^g(auth|sec)([0-9]{1,3})\.`)
	// anywhereRE finds an intake prefix with something after it anywhere in
	// a text, in any case.
	anywhereRE = regexp.MustCompile(`(?i)g(?:auth|sec)[0-9]{1,3}\.[A-Za-z0-9_-]`)
	idRE       = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)
	b64        = base64.RawURLEncoding.Strict()
)

// Line is a parsed intake line.
type Line struct {
	Kind Kind
	// ID is the request the line answers: the state of an OAuth line, the
	// request id of a secret line.
	ID string
	// Code and Issuer are the OAuth code and the issuer ("" when the page
	// sent none).
	Code, Issuer string
	// Sealed is a secret line's HPKE encapsulated key followed by the
	// ciphertext.
	Sealed []byte
}

// invisible are the characters a phone or a copy may put into a line that
// cannot be seen: they are dropped before a line is read.
var invisible = strings.NewReplacer("\u200b", "", "\u200c", "", "\u200d", "", "\u2060", "", "\ufeff", "")

// one returns text with invisible characters dropped and trimmed, and
// whether it is a single token: no whitespace or control character inside.
func one(text string) (string, bool) {
	t := strings.TrimSpace(invisible.Replace(text))
	if t == "" || len(t) > maxLine {
		return "", false
	}
	bad := strings.ContainsFunc(t, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
	return t, !bad
}

// Holds reports whether text holds an intake line of any version anywhere:
// a gauthN. or gsecN. prefix, in any case, with something after it, once
// invisible characters are dropped. The transport uses it to take a message
// out of the inbox path (R-INT-5) and to hide a line in a reply or a quote.
// It is wider than a valid line on purpose: a newer page's line, a damaged
// one, one with words around it or in a caption still carries a code or a
// ciphertext, so it is taken too and refused by Parse, never handed to the
// inbox and the model.
func Holds(text string) bool {
	return anywhereRE.MatchString(invisible.Replace(text))
}

// Parse reads an intake line: the whole text, once invisible characters and
// surrounding whitespace are dropped; the prefix may be in any case (a
// phone may capitalise it). The errors are ErrNotALine (the text is not one
// line alone), ErrVersion and ErrMalformed; none holds any part of text.
func Parse(text string) (Line, error) {
	t, ok := one(text)
	if !ok {
		return Line{}, ErrNotALine
	}
	m := prefixRE.FindStringSubmatch(t)
	if m == nil || len(m[0]) == len(t) {
		return Line{}, ErrNotALine
	}
	if m[2] != lineVersion {
		return Line{}, ErrVersion
	}
	parts := strings.Split(t, ".")
	if strings.EqualFold(m[1], "auth") {
		return parseOAuth(parts)
	}
	return parseSecret(parts)
}

// parseOAuth reads gauth1.<state>.<b64u(code)>[.<b64u(iss)>].
func parseOAuth(parts []string) (Line, error) {
	if len(parts) != 3 && len(parts) != 4 {
		return Line{}, ErrMalformed
	}
	if !idRE.MatchString(parts[1]) {
		return Line{}, ErrMalformed
	}
	code, err := decode(parts[2], 1, maxCode)
	if err != nil {
		return Line{}, err
	}
	l := Line{Kind: KindOAuth, ID: parts[1], Code: string(code)}
	if len(parts) == 4 {
		iss, err := decode(parts[3], 1, maxIssuer)
		if err != nil || !utf8.Valid(iss) {
			return Line{}, ErrMalformed
		}
		l.Issuer = string(iss)
	}
	return l, nil
}

// parseSecret reads gsec1.<id>.<b64u(enc||ciphertext)>.
func parseSecret(parts []string) (Line, error) {
	if len(parts) != 3 || !idRE.MatchString(parts[1]) {
		return Line{}, ErrMalformed
	}
	sealed, err := decode(parts[2], minSealed, maxSealed)
	if err != nil {
		return Line{}, err
	}
	return Line{Kind: KindSecret, ID: parts[1], Sealed: sealed}, nil
}

// decode reads strict unpadded base64url that decodes to min..max bytes.
func decode(s string, minLen, maxLen int) ([]byte, error) {
	b, err := b64.DecodeString(s)
	if err != nil || len(b) < minLen || len(b) > maxLen {
		return nil, ErrMalformed
	}
	return b, nil
}
