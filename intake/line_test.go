package intake

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func b64u(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

const id22 = "AAECAwQFBgcICQoLDA0ODw"

func TestIsLine(t *testing.T) {
	sealed := b64u(make([]byte, 90))
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"oauth", "gauth1." + id22 + "." + b64u([]byte("code")), true},
		{"secret", "gsec1." + id22 + "." + sealed, true},
		{"surrounding whitespace", "  gsec1." + id22 + "." + sealed + "\n", true},
		{"a newer version", "gsec2." + id22 + "." + sealed, true},
		{"padding still taken", "gsec1." + id22 + "." + sealed + "=", true},
		{"prose", "hello there", false},
		{"prefix only", "gsec1.", false},
		{"no dot", "gsec1", false},
		{"a space inside", "gsec1." + id22 + ".ab cd", true},
		{"two lines", "gsec1." + id22 + ".abcd\ngsec1." + id22 + ".abcd", true},
		{"a line then words", "gsec1." + id22 + ".abcd please", true},
		{"too long", "gsec1." + id22 + "." + strings.Repeat("a", 4100), true},
		{"unknown kind", "gfoo1." + id22 + ".abcd", false},
		{"empty", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsLine(tc.text); got != tc.want {
				t.Errorf("IsLine = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParse(t *testing.T) {
	code := []byte("a:b/c+\x00\xff")
	sealed := make([]byte, 100)
	tests := []struct {
		name string
		text string
		want Line
		err  error
	}{
		{"oauth with issuer", "gauth1." + id22 + "." + b64u(code) + "." + b64u([]byte("https://auth.example.com")),
			Line{Kind: KindOAuth, ID: id22, Code: string(code), Issuer: "https://auth.example.com"}, nil},
		{"oauth without issuer", "gauth1." + id22 + "." + b64u(code),
			Line{Kind: KindOAuth, ID: id22, Code: string(code)}, nil},
		{"secret", "gsec1." + id22 + "." + b64u(sealed), Line{Kind: KindSecret, ID: id22, Sealed: sealed}, nil},
		{"padding refused", "gauth1." + id22 + "." + b64u(code) + "=", Line{}, ErrMalformed},
		{"version 2", "gsec2." + id22 + "." + b64u(sealed), Line{}, ErrVersion},
		{"version 0", "gauth0." + id22 + "." + b64u(code), Line{}, ErrVersion},
		{"version 01", "gauth01." + id22 + "." + b64u(code), Line{}, ErrVersion},
		{"oauth extra part", "gauth1." + id22 + "." + b64u(code) + "." + b64u(code) + "." + b64u(code), Line{}, ErrMalformed},
		{"secret extra part", "gsec1." + id22 + "." + b64u(sealed) + ".x", Line{}, ErrMalformed},
		{"oauth two parts", "gauth1." + id22, Line{}, ErrMalformed},
		{"short id", "gauth1.abc." + b64u(code), Line{}, ErrMalformed},
		{"id with a bad character", "gauth1.AAECAwQFBgcICQoLDA0OD+." + b64u(code), Line{}, ErrMalformed},
		{"empty code", "gauth1." + id22 + ".", Line{}, ErrMalformed},
		{"empty issuer", "gauth1." + id22 + "." + b64u(code) + ".", Line{}, ErrMalformed},
		{"code too long", "gauth1." + id22 + "." + b64u(make([]byte, maxCode+1)), Line{}, ErrMalformed},
		{"issuer not utf-8", "gauth1." + id22 + "." + b64u(code) + "." + b64u([]byte{0xff, 0xfe}), Line{}, ErrMalformed},
		{"sealed too short", "gsec1." + id22 + "." + b64u(make([]byte, minSealed-1)), Line{}, ErrMalformed},
		{"sealed too long", "gsec1." + id22 + "." + b64u(make([]byte, maxSealed+1)), Line{}, ErrMalformed},
		{"not base64", "gsec1." + id22 + ".!!!!", Line{}, ErrMalformed},
		{"a newline inside", "gauth1." + id22 + ".ab\ncd", Line{}, ErrNotALine},
		{"two lines", "gauth1." + id22 + ".abcd\ngauth1." + id22 + ".abcd", Line{}, ErrNotALine},
		{"prose", "hello", Line{}, ErrNotALine},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.text)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if err != nil {
				if strings.Contains(err.Error(), tc.text) || strings.Contains(err.Error(), id22) {
					t.Errorf("the error holds the line: %q", err)
				}
				return
			}
			if got.Kind != tc.want.Kind || got.ID != tc.want.ID || got.Code != tc.want.Code || got.Issuer != tc.want.Issuer || string(got.Sealed) != string(tc.want.Sealed) {
				t.Errorf("Parse = %+v, want %+v", got, tc.want)
			}
		})
	}
}
