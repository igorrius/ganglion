package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
)

// manifestFor is the reader's manifest under another name, with no required
// setting and ports: emits sends numbers, accepts takes them.
func manifestFor(name, emits, accepts string) string {
	m := strings.ReplaceAll(demoManifest, "demo-reader", name)
	m = strings.Replace(m, "inverter_sn: {type: string, required: true}", "inverter_sn: {type: string}", 1)
	ports := "ports:\n"
	if emits != "" {
		ports += "  emits: {" + emits + "}\n"
	}
	if accepts != "" {
		ports += "  accepts: {" + accepts + "}\n"
	}
	if ports == "ports:\n" {
		return m
	}
	return strings.Replace(m, "triggers:\n", ports+"triggers:\n", 1)
}

// linked gives e the link tools' parts over the real store and a fake file.
func (e *moduleEnv) linked() *linkFake {
	f := &linkFake{}
	e.m.Links = builtin.Linking{File: f, Modules: e.store.LinkModules, Changed: func(context.Context) { f.changed++ }}
	return f
}

// built writes the module's source, builds it with manifest and returns the
// build's digest.
func (e *moduleEnv) built(t *testing.T, name, manifest string) string {
	t.Helper()
	if _, err := e.write(t, name, "manifest.yaml", manifest); err != nil {
		t.Fatal(err)
	}
	art := artifact(manifest, 1024, 1)
	e.b.art = art
	res := e.buildNow(t, `{"name":"`+name+`"}`)
	var ans struct{ Build string }
	if err := json.Unmarshal([]byte(res.Text), &ans); err != nil || ans.Build != art.SHA256 {
		t.Fatalf("build answer %s: %v", res.Text, err)
	}
	return art.SHA256
}

func manyCall(n int, args string) tool.Call {
	c := call(builtin.ModuleInstallTool, args)
	c.N = n
	c.Speak = texts.Speaker{Lang: texts.English}
	return c
}

// two builds a sender and a taker whose ports a link joins.
func twoModules(t *testing.T) (*moduleEnv, *linkFake, string, string) {
	t.Helper()
	e := newModuleEnv(t)
	f := e.linked()
	a := e.built(t, "sender", manifestFor("sender", "reading: number", ""))
	b := e.built(t, "taker", manifestFor("taker", "", "reading: number"))
	return e, f, a, b
}

// ADR-0111: two builds and a link between them are one question, and one press
// installs both, in order, and writes the link.
func TestOneWindowAndOnePressInstallTwoBuildsAndTheLinkBetweenThem(t *testing.T) {
	e, f, a, b := twoModules(t)
	c := manyCall(5, `{"builds":["`+a+`","`+b+`"],"links":[{"from":"sender.reading","to":"taker.reading"}],"note":"pair"}`)
	q := question(t, e, c)
	for _, want := range []string{"local/sender", "local/taker", "Then it writes these links:", "sender.reading → taker.reading, carries number",
		"One press installs these modules, in this order: local/sender, local/taker."} {
		if !strings.Contains(q, want) {
			t.Fatalf("the window lacks %q:\n%s", want, q)
		}
	}
	if strings.Index(q, "local/sender (") > strings.Index(q, "local/taker (") {
		t.Fatalf("the modules are not in the order given:\n%s", q)
	}
	if len(f.ls) != 0 || len(e.reloads.names) != 0 {
		t.Fatal("the question changed something")
	}
	res, err := e.tool(builtin.ModuleInstallTool).Run(context.Background(), pressed(c))
	if err != nil {
		t.Fatal(err)
	}
	if got := e.reloads.names; len(got) != 2 || got[0] != "local/sender" || got[1] != "local/taker" {
		t.Fatalf("reloaded %v", got)
	}
	if len(f.ls) != 1 || f.ls[0] != (settings.Link{From: "sender.reading", To: "taker.reading"}) || f.changed != 1 {
		t.Fatalf("links %+v, host told %d times", f.ls, f.changed)
	}
	for _, want := range []string{"local/sender: version", "local/taker: version", "the link sender.reading → taker.reading is written"} {
		if !strings.Contains(res.Text, want) {
			t.Fatalf("answer lacks %q: %s", want, res.Text)
		}
	}
}

// A link the node would refuse, with the builds installed, answers the model
// with the node's reason and opens no window.
func TestARefusedLinkOpensNoWindow(t *testing.T) {
	e, f, a, b := twoModules(t)
	for name, tc := range map[string]struct{ link, want string }{
		"no port":   {`{"from":"sender.nope","to":"taker.reading"}`, "has no port nope"},
		"no module": {`{"from":"sender.reading","to":"ghost.reading"}`, "ghost is not installed"},
		"shape":     {`{"from":"sender.reading","to":"taker.reading_in"}`, "has no port reading_in"},
	} {
		t.Run(name, func(t *testing.T) {
			c := manyCall(6, `{"builds":["`+a+`","`+b+`"],"links":[`+tc.link+`]}`)
			_, err := e.tool(builtin.ModuleInstallTool).(tool.Questioner).Question(context.Background(), c)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
		})
	}
	if left, _ := filepath.Glob(filepath.Join(e.m.Builds, "*.window-*")); len(left) != 0 {
		t.Fatalf("a refused request kept a window: %v", left)
	}
	if len(f.ls) != 0 || len(e.reloads.names) != 0 {
		t.Fatal("a refusal changed something")
	}
}

// A press that fails halfway names what was installed, what failed and why,
// and what was not tried; what was installed stays.
func TestAFailureHalfwayNamesWhatStayed(t *testing.T) {
	e, f, a, b := twoModules(t)
	e.reloads.checkOf = map[string]error{"local/taker": errors.New("the module did not answer its health check")}
	c := manyCall(7, `{"builds":["`+a+`","`+b+`"],"links":[{"from":"sender.reading","to":"taker.reading"}]}`)
	question(t, e, c)
	_, err := e.tool(builtin.ModuleInstallTool).Run(context.Background(), pressed(c))
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"done: local/sender: version", "failed: ", "local/taker is not installed", "Not tried: link sender.reading → taker.reading"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error lacks %q: %v", want, err)
		}
	}
	if got := e.reloads.names; len(got) != 1 || got[0] != "local/sender" {
		t.Fatalf("reloaded %v: the first build stays installed and runs", got)
	}
	if len(f.ls) != 0 || f.changed != 0 {
		t.Fatalf("a link was written after a failed install: %+v", f.ls)
	}
}

// A request that is not the one the owner saw (a link written by another call
// since the question) installs nothing.
func TestARequestThatChangedSinceTheQuestionInstallsNothing(t *testing.T) {
	e, f, a, b := twoModules(t)
	c := manyCall(8, `{"builds":["`+a+`","`+b+`"],"links":[{"from":"sender.reading","to":"taker.reading"}]}`)
	question(t, e, c)
	f.ls = append(f.ls, settings.Link{From: "sender.reading", To: "taker.reading"})
	if _, err := e.tool(builtin.ModuleInstallTool).Run(context.Background(), pressed(c)); err == nil {
		t.Fatal("installed a request that is not the one asked")
	}
	if len(e.reloads.names) != 0 {
		t.Fatalf("installed %v", e.reloads.names)
	}
}

func TestInstallRequestLimits(t *testing.T) {
	e, _, a, b := twoModules(t)
	for name, tc := range map[string]struct{ args, want string }{
		"none":         {`{}`, "builds is one to five digests"},
		"both forms":   {`{"build":"` + a + `","builds":["` + b + `"]}`, "not both"},
		"six":          {`{"builds":["` + strings.Join([]string{a, b, a, b, a, b}, `","`) + `"]}`, "at most 5 builds"},
		"same module":  {`{"builds":["` + a + `","` + a + `"]}`, "comes twice"},
		"eleven links": {`{"builds":["` + a + `"],"links":[` + strings.TrimSuffix(strings.Repeat(`{"from":"a.b","to":"c.d"},`, 11), ",") + `]}`, "at most 10 links"},
		"same link":    {`{"builds":["` + a + `","` + b + `"],"links":[{"from":"sender.reading","to":"taker.reading"},{"from":"sender.reading","to":"taker.reading"}]}`, "comes twice"},
		"unknown key":  {`{"builds":["` + a + `"],"extra":1}`, "arguments"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.tool(builtin.ModuleInstallTool).(tool.Questioner).Question(context.Background(), manyCall(9, tc.args))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %v, want %q", err, tc.want)
			}
		})
	}
}

// Five modules with the rights of the reader fit one question.
func TestFiveModulesFitOneWindow(t *testing.T) {
	e := newModuleEnv(t)
	e.linked()
	var shas []string
	for i := range 5 {
		name := fmt.Sprintf("mod%d", i)
		shas = append(shas, e.built(t, name, manifestFor(name, "reading: number", "")))
	}
	c := manyCall(10, `{"builds":["`+strings.Join(shas, `","`)+`"]}`)
	q := question(t, e, c)
	if n := utf8.RuneCountInString(q); n > tool.MaxQuestionRunes {
		t.Fatalf("the window is %d runes", n)
	}
}

// A module whose source is not the node's own says so in the window; one the
// node wrote itself does not.
func TestWindowSaysWhenTheSourceIsNotTheNodesOwn(t *testing.T) {
	e := newModuleEnv(t)
	e.ownSource(t, "demo-reader")
	art := artifact(demoManifest, 1024, 1)
	e.build(t, art)
	if q := question(t, e, installCall(art.SHA256)); strings.Contains(q, "outside content") {
		t.Fatalf("the node's own source is called outside:\n%s", q)
	}
	c := writeCall(t, "demo-reader", "src/lib.rs", "pub fn g() {}\n")
	c.Tainted = true
	if _, err := e.tool(builtin.ModuleWriteTool).Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if q := question(t, e, installCall(art.SHA256)); !strings.Contains(q, "written after the agent read outside content") {
		t.Fatalf("the window does not say the source is outside:\n%s", q)
	}
}
