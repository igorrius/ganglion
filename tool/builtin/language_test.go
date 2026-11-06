package builtin_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/policy"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
)

type chooser struct {
	principal string
	lang      texts.Lang
	calls     int
}

func (c *chooser) Choose(_ context.Context, principal string, l texts.Lang) (bool, error) {
	c.calls++
	changed := c.lang != l
	c.principal, c.lang = principal, l
	return changed, nil
}

func languageCall(args string, sp texts.Speaker) tool.Call {
	c := call("language.set", args)
	c.Speak = sp
	return c
}

func TestLanguageSetClean(t *testing.T) {
	ch := &chooser{}
	c := languageCall(`{"code":"uk"}`, texts.Speaker{})
	reg, err := tool.NewRegistry(router.Local{}, builtin.NewLanguageSet(ch))
	if err != nil {
		t.Fatal(err)
	}
	ask := &asker{}
	res, err := tool.Chain(reg.Run, policy.New(policy.DefaultConfig(), reg, ask).Middleware())(context.Background(), c)
	if err != nil || res.Pending != "" || len(ask.asked) != 0 {
		t.Fatalf("%+v, asked %q, err %v", res, ask.asked, err)
	}
	if ch.lang != "uk" || ch.principal != c.Principal {
		t.Fatalf("chose %+v", ch)
	}
}

func TestLanguageSetTaintedAsksInBothLanguages(t *testing.T) {
	ch := &chooser{}
	c := languageCall(`{"code":"uk"}`, texts.Speaker{Lang: "ru"})
	c.Tainted = true
	q, err := confirmed(t, c, builtin.NewLanguageSet(ch))
	if err != nil {
		t.Fatal(err)
	}
	if want := "Писати до вас мовою Українська?\nПисать вам на языке Українська?"; q != want {
		t.Fatalf("%q, want %q", q, want)
	}
	if ch.calls != 0 {
		t.Fatal("applied before the press")
	}
}

func TestLanguageSetSameTextOneLine(t *testing.T) {
	q, err := builtin.NewLanguageSet(&chooser{}).Question(context.Background(), languageCall(`{"code":"uk"}`, texts.Speaker{Lang: "uk"}))
	if err != nil || strings.Contains(q, "\n") {
		t.Fatalf("%q, %v", q, err)
	}
}

func TestLanguageSetNoFile(t *testing.T) {
	q, err := builtin.NewLanguageSet(&chooser{}).Question(context.Background(), languageCall(`{"code":"pl"}`, texts.Speaker{Lang: "ru"}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(q, "Switch to Polski? The bot's own buttons stay in English for now.\n") || !strings.Contains(q, "Polski?") {
		t.Fatalf("%q", q)
	}
}

func TestLanguageSetTakesOnlyACode(t *testing.T) {
	spec := builtin.NewLanguageSet(&chooser{}).Spec()
	var schema struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		AdditionalProperties *bool                      `json:"additionalProperties"`
	}
	if err := json.Unmarshal(spec.InputSchema, &schema); err != nil {
		t.Fatal(err)
	}
	if len(schema.Properties) != 1 || schema.Properties["code"] == nil || schema.AdditionalProperties == nil || *schema.AdditionalProperties {
		t.Fatalf("%s", spec.InputSchema)
	}
	ch := &chooser{}
	if _, err := builtin.NewLanguageSet(ch).Run(context.Background(), languageCall(`{"code":"uk","principal":"someone"}`, texts.Speaker{})); err == nil || ch.calls != 0 {
		t.Fatalf("err %v, calls %d", err, ch.calls)
	}
}

func TestLanguageSetUnknownCode(t *testing.T) {
	ch := &chooser{}
	tl := builtin.NewLanguageSet(ch)
	for _, args := range []string{`{"code":"xx"}`, `{"code":""}`, `{}`} {
		if _, err := tl.Run(context.Background(), languageCall(args, texts.Speaker{})); err == nil {
			t.Errorf("%s: no error", args)
		}
		if _, err := tl.Question(context.Background(), languageCall(args, texts.Speaker{})); err == nil {
			t.Errorf("%s: asked", args)
		}
	}
	if ch.calls != 0 {
		t.Fatalf("%d writes", ch.calls)
	}
}

func TestLanguageSetIsAdminOnlyUntilPermissionsExist(t *testing.T) {
	spec := builtin.NewLanguageSet(&chooser{}).Spec()
	if !spec.AdminOnly || !spec.LocalSafe {
		t.Fatalf("%+v", spec)
	}
	reg, err := tool.NewRegistry(router.Local{}, builtin.NewLanguageSet(&chooser{}))
	if err != nil {
		t.Fatal(err)
	}
	if got := reg.Specs(false); len(got) != 0 {
		t.Fatalf("offered to a person who is not an admin: %+v", got)
	}
	d, _ := policy.Decide(policy.DefaultConfig().MaxDepth, spec, tool.Call{Name: "language.set", Args: json.RawMessage(`{"code":"uk"}`), Admin: false})
	if d != tool.PolicyForbidden {
		t.Fatalf("policy %s", d)
	}
}

func TestLanguageSetNotInScheduledRun(t *testing.T) {
	ch := &chooser{}
	c := languageCall(`{"code":"uk"}`, texts.Speaker{})
	c.Grant = &tool.Grant{Tools: []string{"language.set"}}
	if _, err := builtin.NewLanguageSet(ch).Run(context.Background(), c); err == nil || ch.calls != 0 {
		t.Fatalf("err %v, calls %d", err, ch.calls)
	}
}

func TestLanguageSetRerunChangesNothingTwice(t *testing.T) {
	ch := &chooser{}
	tl := builtin.NewLanguageSet(ch)
	c := languageCall(`{"code":"uk"}`, texts.Speaker{})
	r1, err := tl.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := tl.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Text == "" || r2.Text == "" || ch.lang != "uk" || len(r1.Out)+len(r2.Out) != 0 {
		t.Fatalf("%+v %+v", r1, r2)
	}
}
