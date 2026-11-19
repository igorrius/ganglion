package mcpadmin_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/mcpadmin"
	"github.com/igorrius/ganglion/mcpclient/mcpclienttest"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
)

// connectCall is mcp.connect's call for the rig's server with extra arguments.
func (r *rig) connectCall(extra map[string]any) tool.Call {
	args := map[string]any{"name": "shop", "url": r.url}
	maps.Copy(args, extra)
	return call(mcpadmin.ToolConnect, args)
}

// The owner's sentence reaches the card as rules and others, and one press
// of Connect applies the whole proposal (R-MCP-3, #282).
func TestConnectWithRulesIsOneCardOnePress(t *testing.T) {
	r := newRig(t, false)
	res, err := r.tool(mcpadmin.ToolConnect).Run(context.Background(), r.connectCall(map[string]any{
		"rules": map[string]any{"search_products": "auto", "shop.add_to_cart": "confirm"}, "others": "forbidden"}))
	if err != nil {
		t.Fatal(err)
	}
	card := res.Out[0].Text
	for _, want := range []string{"Run by themselves (the server marks them read-only):\n- `search_products`",
		"Ask you first:\n- `add_to_cart`", "Never offered:\n- `place_order`"} {
		if !strings.Contains(card, want) {
			t.Errorf("the card lacks %q:\n%s", want, card)
		}
	}
	r.press(t, res.Out[0], "Connect")
	r.waitState(t, "shop connected")
	s, _ := r.line(t)
	want := map[string]settings.MCPPolicy{"search_products": settings.MCPPolicyAuto, "add_to_cart": settings.MCPPolicyConfirm,
		"place_order": settings.MCPPolicyForbidden}
	for k, v := range want {
		if s.Tools[k] != v {
			t.Errorf("%s = %q, want %q", k, s.Tools[k], v)
		}
	}
}

// A name the server does not have is refused with an error that names it,
// and no card or button is made.
func TestConnectRefusesAnUnknownToolName(t *testing.T) {
	r := newRig(t, false)
	res, err := r.tool(mcpadmin.ToolConnect).Run(context.Background(), r.connectCall(map[string]any{
		"rules": map[string]any{"search_products": "auto", "purchase_history": "auto"}, "others": "forbidden"}))
	if err == nil || !strings.Contains(err.Error(), "purchase_history") || len(res.Out) != 0 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if len(r.buttons.m) != 0 {
		t.Fatal("buttons were stored for a refused call")
	}
	if _, err := r.tool(mcpadmin.ToolConnect).Run(context.Background(), r.connectCall(map[string]any{
		"rules": map[string]any{"search_products": "sometimes"}})); err == nil {
		t.Fatal("a bad policy was accepted")
	}
	if _, err := r.tool(mcpadmin.ToolConnect).Run(context.Background(), r.connectCall(map[string]any{"others": "never"})); err == nil {
		t.Fatal("a bad others was accepted")
	}
}

// manyTools is a server of n tools named t00.., none read-only.
func (r *rig) manyTools(n int, name func(i int) string) {
	tools := make([]json.RawMessage, n)
	for i := range tools {
		tools[i] = mcpclienttest.Tool(name(i), fmt.Sprintf("Does thing %d on the shop's side with a long description line.", i), "", false)
	}
	r.mcp.Update(func(o *mcpclienttest.Options) { o.Tools = tools })
}

// A tool that runs by itself and is not marked read-only is always named,
// even among many tools; forbidden ones are a count.
func TestANonReadOnlyAutoIsAlwaysShownByName(t *testing.T) {
	r := newRig(t, false)
	r.manyTools(42, func(i int) string { return fmt.Sprintf("tool_%02d", i) })
	res, err := r.tool(mcpadmin.ToolConnect).Run(context.Background(), r.connectCall(map[string]any{
		"rules": map[string]any{"tool_07": "auto", "tool_31": "auto", "tool_40": "confirm"}, "others": "forbidden"}))
	if err != nil {
		t.Fatal(err)
	}
	card := res.Out[0].Text
	for _, want := range []string{"although the server does not mark them read-only:\n- `tool_07`", "- `tool_31`", "- `tool_40`",
		"Never offered: 39 tools."} {
		if !strings.Contains(card, want) {
			t.Errorf("the card lacks %q:\n%s", want, card)
		}
	}
	if strings.Contains(card, "`tool_01`") || strings.Contains(card, "more") {
		t.Errorf("a forbidden tool is named or the card folds:\n%s", card)
	}
	if n := len([]rune(card)); n > 4096 {
		t.Fatalf("the card is %d runes", n)
	}
}

// With 100 such tools the lines lose their descriptions before any name is
// hidden, and the card stays under Telegram's limit; with names that do not
// fit either, there is no card.
func TestAutoToolsAreNeverFolded(t *testing.T) {
	r := newRig(t, false)
	r.manyTools(100, func(i int) string { return fmt.Sprintf("tool_%03d", i) })
	res, err := r.tool(mcpadmin.ToolConnect).Run(context.Background(), r.connectCall(map[string]any{"others": "auto"}))
	if err != nil {
		t.Fatal(err)
	}
	card := res.Out[0].Text
	for i := range 100 {
		if !strings.Contains(card, fmt.Sprintf("`tool_%03d`", i)) {
			t.Fatalf("tool_%03d is not on the card", i)
		}
	}
	if n := len([]rune(card)); n > 4096 || strings.Contains(card, "more") {
		t.Fatalf("the card is %d runes:\n%s", n, card)
	}

	r.manyTools(128, func(i int) string { return fmt.Sprintf("a_very_long_tool_name_for_the_shop_number_%03d", i) })
	res, err = r.tool(mcpadmin.ToolConnect).Run(context.Background(), r.connectCall(map[string]any{"others": "auto"}))
	if err == nil || len(res.Out) != 0 || !strings.Contains(err.Error(), "narrow") {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

// Few forbidden tools are named, many are a count.
func TestForbiddenToolsAreNamedWhenFew(t *testing.T) {
	r := newRig(t, false)
	res, err := r.tool(mcpadmin.ToolConnect).Run(context.Background(), r.connectCall(map[string]any{
		"rules": map[string]any{"place_order": "forbidden"}}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Out[0].Text, "Never offered:\n- `place_order`") {
		t.Fatalf("card:\n%s", res.Out[0].Text)
	}
}

// Several changes are one question and one run; the call is checked as a
// whole, so one bad name changes nothing (R-MCP-9).
func TestRuleWithSeveralChangesIsOneCardOnePress(t *testing.T) {
	r := newRig(t, false)
	connected(t, r)
	rt := r.tool(mcpadmin.ToolRule)
	c := call(mcpadmin.ToolRule, map[string]any{"server": "shop", "others": "forbidden",
		"changes": []map[string]any{{"tool": "search_products", "policy": "auto"}, {"tool": "shop.add_to_cart", "policy": "auto"}}})
	q, err := rt.(tool.Questioner).Question(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Change the rules of 2 tools of `shop`?", "`shop.add_to_cart` — «asks you first» → «runs by itself»",
		"`shop.place_order` — «asks you first» → «never offered»"} {
		if !strings.Contains(q, want) {
			t.Errorf("the question lacks %q:\n%s", want, q)
		}
	}
	if _, err := rt.Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	s, _ := r.line(t)
	if s.Tools["search_products"] != settings.MCPPolicyAuto || s.Tools["add_to_cart"] != settings.MCPPolicyAuto ||
		s.Tools["place_order"] != settings.MCPPolicyForbidden {
		t.Fatalf("line = %+v", s.Tools)
	}

	for name, args := range map[string]map[string]any{
		"unknown among good": {"server": "shop", "changes": []map[string]any{{"tool": "place_order", "policy": "confirm"}, {"tool": "nope", "policy": "auto"}}},
		"twice":              {"server": "shop", "changes": []map[string]any{{"tool": "place_order", "policy": "confirm"}, {"tool": "shop.place_order", "policy": "auto"}}},
	} {
		_, err := rt.Run(context.Background(), call(mcpadmin.ToolRule, args))
		if err == nil {
			t.Errorf("%s: accepted", name)
		}
		if s, _ := r.line(t); s.Tools["place_order"] != settings.MCPPolicyForbidden {
			t.Errorf("%s: a refused call changed the file: %+v", name, s.Tools)
		}
	}
}

// Many changes are grouped in the question, except those that turn a tool on.
func TestRuleQuestionGroupsManyChanges(t *testing.T) {
	r := newRig(t, false)
	r.manyTools(30, func(i int) string { return fmt.Sprintf("tool_%02d", i) })
	res, err := r.tool(mcpadmin.ToolConnect).Run(context.Background(), r.connectCall(nil))
	if err != nil {
		t.Fatal(err)
	}
	r.press(t, res.Out[0], "Connect")
	r.waitState(t, "shop connected")
	c := call(mcpadmin.ToolRule, map[string]any{"server": "shop", "others": "forbidden",
		"changes": []map[string]any{{"tool": "tool_03", "policy": "auto"}}})
	q, err := r.tool(mcpadmin.ToolRule).(tool.Questioner).Question(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q, "`shop.tool_03`") || !strings.Contains(q, "- 29 more tools") || strings.Contains(q, "tool_04") {
		t.Fatalf("question:\n%s", q)
	}
}

// A question that would pass the policy's cut shows every auto change by
// name, or the call is refused: the owner never presses Yes on a cut list
// (R-MCP-9). Checked in both a short language and a longer one.
func TestRuleQuestionNeverHidesAnAutoChange(t *testing.T) {
	for _, lang := range []texts.Lang{texts.English, texts.Lang("ru")} {
		t.Run(string(lang), func(t *testing.T) {
			r := newRig(t, false)
			r.manyTools(45, func(i int) string { return fmt.Sprintf("tool_%02d", i) })
			res, err := r.tool(mcpadmin.ToolConnect).Run(context.Background(), r.connectCall(nil))
			if err != nil {
				t.Fatal(err)
			}
			r.press(t, res.Out[0], "Connect")
			r.waitState(t, "shop connected")
			c := call(mcpadmin.ToolRule, map[string]any{"server": "shop", "others": "auto",
				"changes": []map[string]any{{"tool": "tool_00", "policy": "forbidden"}}})
			c.Speak = texts.Speaker{Lang: lang, Cat: texts.Shipped()}
			q, err := r.tool(mcpadmin.ToolRule).(tool.Questioner).Question(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			if n := len([]rune(q)); n > tool.MaxQuestionRunes {
				t.Fatalf("the question is %d runes and would be cut", n)
			}
			for i := 1; i < 45; i++ {
				if !strings.Contains(q, fmt.Sprintf("shop.tool_%02d", i)) {
					t.Fatalf("tool_%02d is not named:\n%s", i, q)
				}
			}
		})
	}
	// Names that do not fit either: refused.
	r := newRig(t, false)
	r.manyTools(128, func(i int) string { return fmt.Sprintf("a_very_long_tool_name_for_the_shop_number_%03d", i) })
	res, err := r.tool(mcpadmin.ToolConnect).Run(context.Background(), r.connectCall(nil))
	if err != nil {
		t.Fatal(err)
	}
	r.press(t, res.Out[0], "Connect")
	r.waitState(t, "shop connected")
	c := call(mcpadmin.ToolRule, map[string]any{"server": "shop", "others": "auto",
		"changes": []map[string]any{{"tool": "a_very_long_tool_name_for_the_shop_number_000", "policy": "forbidden"}}})
	if _, err := r.tool(mcpadmin.ToolRule).(tool.Questioner).Question(context.Background(), c); err == nil {
		t.Fatal("a question that cannot name every auto tool was worded")
	}
}

// A call with others applies only to the line its question was worded
// against: a tool added by a later card is never turned on unseen.
func TestRuleWithOthersRefusesAStaleQuestion(t *testing.T) {
	r := newRig(t, false)
	connected(t, r)
	rt := r.tool(mcpadmin.ToolRule)
	c := call(mcpadmin.ToolRule, map[string]any{"server": "shop", "others": "auto",
		"changes": []map[string]any{{"tool": "place_order", "policy": "forbidden"}}})
	if _, err := rt.(tool.Questioner).Question(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	// The line changes before the press.
	r.manyTools(4, func(i int) string { return fmt.Sprintf("new_%d", i) })
	res, err := r.tool(mcpadmin.ToolConnect).Run(context.Background(), r.connectCall(nil))
	if err != nil {
		t.Fatal(err)
	}
	r.press(t, res.Out[0], "Connect")
	if _, err := rt.Run(context.Background(), c); err == nil {
		t.Fatal("a stale question was applied")
	}
	if s, _ := r.line(t); s.Tools["new_0"] != settings.MCPPolicyConfirm {
		t.Fatalf("line = %+v", s.Tools)
	}
	// A press with no question asked refuses as well.
	if _, err := rt.Run(context.Background(), c); err == nil {
		t.Fatal("a call that was never asked was applied")
	}
}

// "…and N more" counts only the lines of its own section.
func TestMoreCountsOnlyTheSectionsOwnLines(t *testing.T) {
	r := newRig(t, false)
	r.manyTools(120, func(i int) string { return fmt.Sprintf("tool_%03d_with_a_name_that_is_a_bit_long", i) })
	rules := map[string]any{}
	for i := 100; i < 120; i++ {
		rules[fmt.Sprintf("tool_%03d_with_a_name_that_is_a_bit_long", i)] = "forbidden"
	}
	res, err := r.tool(mcpadmin.ToolConnect).Run(context.Background(), r.connectCall(map[string]any{"rules": rules}))
	if err != nil {
		t.Fatal(err)
	}
	card := res.Out[0].Text
	i := strings.Index(card, "…and ")
	if i < 0 {
		t.Fatalf("the card does not fold:\n%s", card)
	}
	var n int
	if _, err := fmt.Sscanf(card[i:], "…and %d more", &n); err != nil || n <= 0 || n > 100 {
		t.Fatalf("the count is %d (%v), the section has at most 100 tools:\n%s", n, err, card)
	}
	listed := strings.Count(card, "- `tool_")
	if listed+n != 100 {
		t.Fatalf("listed %d and %d more: not the 100 that ask", listed, n)
	}
}
