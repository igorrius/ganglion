package redact

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	for _, tc := range []struct{ name, in, gone string }{
		{"json key", `{"api_key":"sk-1","q":"x"}`, "sk-1"},
		{"nested", `{"auth":{"Access_Token":"tok-9"},"q":"x"}`, "tok-9"},
		{"object value", `{"secrets":{"a":"hush-7"}}`, "hush-7"},
		{"array", `[{"password":"pw-3"}]`, "pw-3"},
		{"text", "curl --data password=hunter2 https://x", "hunter2"},
		{"bot url", "https://api.telegram.org/bot123456:ABC-def_ghi/getMe", "ABC-def_ghi"},
		{"truncated json", `{"q":"x","secret":"abc-5`, "abc-5"},
		{"query string in a json value", `{"url":"https://x/a?api_key=S3CR&q=1"}`, "S3CR"},
		{"bearer header line", "Authorization: Bearer B3ARER", "B3ARER"},
		{"bearer in json headers", `{"headers":{"Authorization":"Bearer B3ARER"}}`, "B3ARER"},
		{"bearer in a json string", `{"note":"send Bearer B3ARER now"}`, "B3ARER"},
		{"userinfo", "fetch https://user:P4SS@host/path", "P4SS"},
		{"userinfo in json", `{"url":"https://user:P4SS@host/"}`, "P4SS"},
		{"cookie key", `{"Cookie":"sid=C00K; other=1"}`, "C00K"},
		{"cookie header line", "Cookie: a=1; sid=C00K", "C00K"},
		{"passwd key", `{"passwd":"PW5"}`, "PW5"},
		{"passwd text", "passwd=PW5", "PW5"},
		{"authorization key", `{"authorization":"Basic AUTH1"}`, "AUTH1"},
	} {
		if got := String(tc.in); strings.Contains(got, tc.gone) {
			t.Errorf("%s: %q still holds %q", tc.name, got, tc.gone)
		}
	}
	if got := String(`{"q":"hello","n":3}`); got != `{"n":3,"q":"hello"}` && got != `{"q":"hello","n":3}` {
		t.Errorf("clean input changed: %q", got)
	}
	if got := String("plain words"); got != "plain words" {
		t.Errorf("plain text changed: %q", got)
	}
}

// Every call is counted by tool, decision and result, and timed by tool;
// a name the model made up is counted as "unknown", never as itself.

func TestValuesReplacesExactSecrets(t *testing.T) {
	hide := Values("s3cr3t-value", "", "another-key-42")
	got := hide("auth failed for s3cr3t-value and another-key-42, s3cr3t-value again")
	if strings.Contains(got, "s3cr3t-value") || strings.Contains(got, "another-key-42") {
		t.Fatalf("a secret survived: %q", got)
	}
	if got != "auth failed for [redacted] and [redacted], [redacted] again" {
		t.Fatalf("got %q", got)
	}
	if got := hide("nothing here"); got != "nothing here" {
		t.Fatalf("changed plain text: %q", got)
	}
	if got := Values()("x"); got != "x" {
		t.Fatalf("no values changed text: %q", got)
	}
}

// A secret that is a prefix of another is not left half shown.
func TestValuesLongestFirst(t *testing.T) {
	if got := Values("abcdef", "abcdefghij")("key abcdefghij"); got != "key [redacted]" {
		t.Fatalf("got %q", got)
	}
}

// A token has a shape of its own: it is hidden wherever it stands, with no
// key name or header beside it.
func TestTokenShapesAreHiddenAnywhere(t *testing.T) {
	long := strings.Repeat("Ab3dE", 10) // 50 characters, three classes
	for _, tc := range []struct{ name, in, gone string }{
		{"openai style", "the key is sk-test-0000000000000000 ok", "sk-test-0000000000000000"},
		{"anthropic style", "sk-ant-api03-Test0000_0000-0000000000", "Test0000_0000-0000000000"},
		{"github classic", "token ghp_000000000000000000000000000000000000 end", "ghp_000000000000000000000000000000000000"},
		{"github fine grained", "github_pat_11TEST0000_000000000000000000000000", "11TEST0000_000000000000000000000000"},
		{"github oauth", "gho_000000000000000000000000000000000000", "gho_000000000000000000000000000000000000"},
		{"slack", "xoxb-000000000000-testtesttest", "xoxb-000000000000-testtesttest"},
		{"aws access key id", "id AKIA0000000000TEST00 here", "AKIA0000000000TEST00"},
		{"google api key", "AIza" + strings.Repeat("t0", 17) + "tes", "AIza" + strings.Repeat("t0", 17)},
		{"jwt", "eyJhbGciOiJub25lIn0.eyJzdWIiOiJ0ZXN0In0.c2lnbmF0dXJl", "eyJzdWIiOiJ0ZXN0In0"},
		{"bare mixed token", "see " + long + " for it", long},
		{"in json value", `{"note":"use sk-test-0000000000000000 now"}`, "sk-test-0000000000000000"},
		{"pem block", "x\n-----BEGIN RSA PRIVATE KEY-----\nMIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX\n-----END RSA PRIVATE KEY-----\ny", "MIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX"},
		{"pem block cut before its end", "-----BEGIN PRIVATE KEY-----\nMIIBOgIBAAJBAKj34GkxFhD90vcNLYLInFEX\nMIIBOgIBAAJB", "MIIBOgIBAAJB"},
	} {
		if got := String(tc.in); strings.Contains(got, tc.gone) || !strings.Contains(got, redacted) {
			t.Errorf("%s: %q still holds %q", tc.name, got, tc.gone)
		}
	}
}

// Text that only looks long is left alone: commit ids, digests, paths and
// words are not secrets.
func TestOrdinaryLongTextIsKept(t *testing.T) {
	for _, in := range []string{
		"commit 0123456789abcdef0123456789abcdef01234567",
		"sha256 " + strings.Repeat("0123456789abcdef", 4),
		"/var/lib/ganglion/some/very/long/path/to/a/file-with-a-long-name.txt",
		"internationalizationAndLocalizationOfTheApplication",
		"the task sk-1 and ask-me-later are fine",
		"ghost_town and AKIA are words",
	} {
		if got := String(in); got != in {
			t.Errorf("%q became %q", in, got)
		}
	}
}
