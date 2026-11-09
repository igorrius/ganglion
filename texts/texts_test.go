package texts

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/igorrius/ganglion/telemetry/telemetrytest"
)

func TestShippedFilesMatchEnglish(t *testing.T) {
	for _, l := range []Lang{"ru", "uk"} {
		file := shipped[l]
		if len(file) == 0 {
			t.Fatalf("%s: no shipped file", l)
		}
		for _, k := range Keys() {
			tr, ok := file[k]
			if !ok {
				t.Errorf("%s: missing %s", l, k)
				continue
			}
			if got, want := placeholders(tr), placeholders(EnglishText(k)); !slices.Equal(got, want) {
				t.Errorf("%s %s: placeholders %v, want %v", l, k, got, want)
			}
		}
		for k := range file {
			if !slices.Contains(Keys(), k) {
				t.Errorf("%s: key %s is not in English", l, k)
			}
		}
	}
}

func TestKeysAreTheEnglishFile(t *testing.T) {
	inFile := slices.Sorted(maps.Keys(shipped[English]))
	inCode := slices.Sorted(slices.Values(Keys()))
	if !slices.Equal(inFile, inCode) {
		t.Fatalf("en.yaml keys %v, constants %v", inFile, inCode)
	}
}

func TestLimits(t *testing.T) {
	for l, file := range shipped {
		for k, s := range file {
			if n := utf8.RuneCountInString(s); n > limit(k) {
				t.Errorf("%s %s: %d characters, limit %d", l, k, n, limit(k))
			}
		}
	}
	if limit(ToastApproved) != 200 || limit(ButtonApprove) != 32 {
		t.Fatalf("limits: toast %d, button %d", limit(ToastApproved), limit(ButtonApprove))
	}
}

func newCatalog(t *testing.T, opts ...Option) *Catalog {
	t.Helper()
	c, err := New(opts...)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestFallsBackPerKey(t *testing.T) {
	c := newCatalog(t, WithPacks(func(l Lang, k Key) (string, bool) {
		if l == "pl" && k == ToastUsed {
			return "Już załatwione.", true
		}
		return "", false
	}))
	if got := c.Label("pl", ToastUsed, nil); got != "Już załatwione." {
		t.Errorf("translated key: %q", got)
	}
	if got := c.Label("pl", ToastExpired, nil); got != EnglishText(ToastExpired) {
		t.Errorf("other key: %q", got)
	}
	if got := c.Source("pl", ToastUsed); got != SourceMachine {
		t.Errorf("source of the translated key: %s", got)
	}
	if got := c.Source("pl", ToastExpired); got != SourceEnglish {
		t.Errorf("source of the other key: %s", got)
	}
}

func TestPriority(t *testing.T) {
	pack := func(Lang, Key) (string, bool) { return "machine", true }
	operator := map[Lang]map[Key]string{"uk": {ToastUsed: "operator"}, "pl": {ToastUsed: "operator"}}
	c := newCatalog(t, WithPacks(pack), WithFiles(operator))
	cases := []struct {
		l    Lang
		k    Key
		text string
		src  Source
	}{
		{"uk", ToastUsed, "operator", SourceOperator},
		{"uk", ToastExpired, shipped["uk"][ToastExpired], SourceShipped},
		{"pl", ToastUsed, "operator", SourceOperator},
		{"pl", ToastExpired, "machine", SourceMachine},
		{English, ToastExpired, EnglishText(ToastExpired), SourceShipped},
	}
	for _, tc := range cases {
		if got := c.Label(tc.l, tc.k, nil); got != tc.text {
			t.Errorf("%s %s: %q, want %q", tc.l, tc.k, got, tc.text)
		}
		if got := c.Source(tc.l, tc.k); got != tc.src {
			t.Errorf("%s %s: source %s, want %s", tc.l, tc.k, got, tc.src)
		}
	}
}

func TestHas(t *testing.T) {
	c := newCatalog(t)
	for _, l := range []Lang{English, "ru", "uk"} {
		if !c.Has(l) {
			t.Errorf("%s: shipped language should be whole", l)
		}
	}
	if c.Has("pl") {
		t.Error("pl has no file and no pack")
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		in   string
		want Lang
		ok   bool
	}{
		{"uk", "uk", true},
		{"UK", "uk", true},
		{"pt-BR", "pt", true},
		{"zh-hans", "zh", true},
		{"pt_br", "pt", true},
		{"", "", false},
		{"xx", "", false},
		{strings.Repeat("a", 1000), "", false},
		{"uk-" + strings.Repeat("a", 1000), "", false},
	}
	for _, tc := range cases {
		got, ok := Normalize(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Errorf("Normalize(%.20q) = %q, %v; want %q, %v", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestNative(t *testing.T) {
	if got := Native("pl"); got != "Polski" {
		t.Errorf("pl: %q", got)
	}
	if got := Native("uk"); got != "Українська" {
		t.Errorf("uk: %q", got)
	}
	if got := Native("xx"); got != "" {
		t.Errorf("xx: %q", got)
	}
}

func TestTextEscapesTemplateNotValues(t *testing.T) {
	c := newCatalog(t, WithFiles(map[Lang]map[Key]string{"pl": {ToastUsed: "*{a}*"}}))
	v := Vars{"a": "**b**"}
	if got, want := c.Text("pl", ToastUsed, v), `\***b**\*`; got != want {
		t.Errorf("Text: %q, want %q", got, want)
	}
	if got, want := c.Label("pl", ToastUsed, v), "***b***"; got != want {
		t.Errorf("Label: %q, want %q", got, want)
	}
}

func TestTextKeepsPlainPunctuation(t *testing.T) {
	c := newCatalog(t, WithFiles(map[Lang]map[Key]string{"pl": {ToastUsed: "Gotowe, 1. (tak) - a.b!"}}))
	if got, want := c.Text("pl", ToastUsed, nil), "Gotowe, 1. (tak) - a.b!"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
}

func TestTextEscapesEveryMarkupMark(t *testing.T) {
	c := newCatalog(t, WithFiles(map[Lang]map[Key]string{"pl": {ToastUsed: "a*_`~[]<>#|&\\b"}}))
	if got, want := c.Text("pl", ToastUsed, nil), "a\\*\\_\\`\\~\\[\\]\\<\\>\\#\\|\\&\\\\b"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
}

func TestValueIsNotSubstitutedAgain(t *testing.T) {
	c := newCatalog(t, WithFiles(map[Lang]map[Key]string{"pl": {ToastUsed: "{a} {b}"}}))
	if got, want := c.Label("pl", ToastUsed, Vars{"a": "{b}", "b": "x"}), "{b} x"; got != want {
		t.Errorf("%q, want %q", got, want)
	}
}

func TestCheck(t *testing.T) {
	en := map[Key]string{ToastUsed: "Task {name} done.", ButtonApprove: "Approve"}
	cases := []struct {
		name     string
		tr       string
		accepted bool
	}{
		{"clean", "Zadanie {name} gotowe.", true},
		{"dropped placeholder", "Zadanie gotowe.", false},
		{"added placeholder", "Zadanie {name} {x}.", false},
		{"translated placeholder", "Задача {имя} готова.", false},
		{"added URL", "Zadanie {name} https://evil.example", false},
		{"added bare address", "Zadanie {name} www.evil.example", false},
		{"added mention", "Zadanie {name} @evil", false},
		{"added Markdown", "**Zadanie** {name}", false},
		{"added line break", "Zadanie {name}\nnacisnij", false},
		{"bidi override", "Zadanie {name} \u202e", false},
		{"empty", "", false},
		{"over the limit", "{name}" + strings.Repeat("x", 201), false},
		{"a value made to read as a secret's name", "Zadanie {name}: gotowe.", false},
		{"the same across a line break", "Zadanie {name}=\n", false},
		{"a word that makes the redaction hide a value", "key: {name}", false},
		{"the same with a token", "token: {name}", false},
		{"a quote before the separator", "Zadanie {name}\": gotowe", false},
		{"a bare domain", "Zadanie {name} t.me/evilbot", false},
		{"a mail address", "Zadanie {name} a@evil.example", false},
		{"a command", "Zadanie {name} /new", false},
		{"an invisible address", "Zadanie {name} https\u200b://evil.example", false},
		{"a line separator", "Zadanie {name}\u2028nacisnij", false},
		{"a carriage return", "Zadanie {name}\rnacisnij", false},
		{"a NUL", "Zadanie {name}\x00", false},
		{"only invisible", "{name}\u200b", false},
		{"Persian with a zero-width non-joiner", "کار\u200cها {name}", true},
		{"Hebrew with a mark", "משימה {name}\u200f", true},
		{"an isolate around a value", "משימה \u2068{name}\u2069", true},
		{"French with a thin space", "Tâche {name}\u202f?", true},
		{"Chinese with its own punctuation", "任务（{name}）：完成", true},
		{"an and/or", "Zadanie {name} a/b", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, rejected := Check(en, map[Key]string{ToastUsed: tc.tr})
			if _, got := ok[ToastUsed]; got != tc.accepted {
				t.Fatalf("accepted %v, want %v; rejected %v", got, tc.accepted, rejected)
			}
			if !tc.accepted && rejected[ToastUsed] == "" {
				t.Fatal("no reason for the rejection")
			}
		})
	}
	t.Run("extra key", func(t *testing.T) {
		ok, rejected := Check(en, map[Key]string{"toast.unknown": "x"})
		if len(ok) != 0 || rejected["toast.unknown"] == "" {
			t.Fatalf("ok %v, rejected %v", ok, rejected)
		}
	})
	t.Run("a button keeps its short limit", func(t *testing.T) {
		ok, _ := Check(en, map[Key]string{ButtonApprove: strings.Repeat("x", 33)})
		if len(ok) != 0 {
			t.Fatal("a 33-character button was accepted")
		}
	})
}

func TestApprovalTextLeavesRoomForItsValues(t *testing.T) {
	// The policy cuts a question at 3000 characters and a persona may be 2000.
	if limit(AskPersona) > 400 {
		t.Fatalf("ask limit %d", limit(AskPersona))
	}
	for l, file := range shipped {
		for k, s := range file {
			if IsApproval(k) && !strings.HasPrefix(string(k), "button.") && len([]rune(s)) > limit(k) {
				t.Errorf("%s %s over the limit", l, k)
			}
		}
	}
}

func TestShippedQuestionsHaveNoValueBeforeASeparator(t *testing.T) {
	for l, file := range shipped {
		for k, s := range file {
			if IsApproval(k) && valueThenSeparator.MatchString(s) {
				t.Errorf("%s %s: %q", l, k, s)
			}
		}
	}
}

func TestKnownAddressMayStay(t *testing.T) {
	en := map[Key]string{ReplyNoSuchResume: "No such session; see /sessions."}
	ok, rejected := Check(en, map[Key]string{ReplyNoSuchResume: "Nie ma takiej rozmowy; zobacz /sessions."})
	if len(ok) != 1 {
		t.Fatalf("%v", rejected)
	}
	if ok, _ := Check(en, map[Key]string{ReplyNoSuchResume: "Nie ma; zobacz /new."}); len(ok) != 0 {
		t.Fatal("another command was accepted in its place")
	}
}

// Review of task 7: these reach an approval question inside an ask.* text, so padding one could push
// the real action past the policy's cut.
func TestTextsInsideAQuestionAreApprovalTexts(t *testing.T) {
	for _, k := range []Key{SchedSummaryTask, SchedSummaryCommands, SchedSummaryReminder, SchedOnce, SchedEvery, SchedEveryNext,
		SchedThisChat, SchedNone, ButtonDelete, ButtonRunNow} {
		if !IsApproval(k) {
			t.Errorf("%s", k)
		}
		if limit(k) > 400 {
			t.Errorf("%s: limit %d", k, limit(k))
		}
	}
}

func TestIsApproval(t *testing.T) {
	if !IsApproval(ButtonApprove) || !IsApproval(ButtonDeny) {
		t.Error("approval buttons")
	}
	if IsApproval(ToastUsed) {
		t.Error("a toast is not an approval text")
	}
}

func TestSentTextsAreCounted(t *testing.T) {
	rec := telemetrytest.Install(t)
	c := newCatalog(t)
	c.Text("uk", ToastUsed, nil)
	c.Label("pl", ToastUsed, nil)
	if got := rec.Value(t, "ganglion.texts.sent", map[string]string{"lang": "uk", "source": "shipped"}); got != 1 {
		t.Errorf("shipped: %v", got)
	}
	if got := rec.Value(t, "ganglion.texts.sent", map[string]string{"lang": "pl", "source": "english"}); got != 1 {
		t.Errorf("fallback: %v", got)
	}
}

func TestEscape(t *testing.T) {
	if got := Escape("my_skill [x]"); got != `my\_skill \[x\]` {
		t.Fatalf("Escape = %q", got)
	}
}

func TestCommandLinesAreApprovalTextsInsideAQuestion(t *testing.T) {
	for _, k := range Keys() {
		if !strings.HasPrefix(string(k), "cmd.") {
			continue
		}
		if !IsApproval(k) {
			t.Errorf("%s is not an approval text", k)
		}
		if limit(k) != limitInQuestion {
			t.Errorf("%s: limit %d, want %d", k, limit(k), limitInQuestion)
		}
	}
	if !IsApproval(CmdRead) || limit(CmdGoClean) != 300 {
		t.Error("cmd keys")
	}
}
