package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/igorrius/ganglion/links"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
)

// This file is the admin's tools for the links between modules (R-BUS-5):
// listing them, and proposing and removing one. A link is configuration:
// the owner's press writes the node's file and nothing is rebuilt. The tools
// are the admin's, and a scheduled run never changes them.

// Names of the link tools.
const (
	toolLinkList   = "link.list"
	toolLinkAdd    = "link.add"
	toolLinkRemove = "link.remove"
)

// LinkFile is the links: section of the node's file
// (confighist.LinksFile).
type LinkFile interface {
	Links() ([]settings.Link, error)
	AddLink(l settings.Link) error
	RemoveLink(l settings.Link) error
}

// LinkModulesFunc returns the active local modules with their ports, by
// short name (modstore.Store.LinkModules).
type LinkModulesFunc func(ctx context.Context) (map[string]links.Module, error)

// Linking is what the link tools work on.
type Linking struct {
	File    LinkFile
	Modules LinkModulesFunc
}

// Placeholders of the link questions.
const (
	varFrom = "from"
	varTo   = "to"
)

// linkArgs are the arguments of link.add and link.remove.
type linkArgs struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Tools are the link tools.
func (l Linking) Tools() []tool.Handler {
	confirm := func(name, about string, q, run routeFunc) tool.Handler {
		return &routeTool{spec: tool.Spec{Name: name, Description: about, InputSchema: json.RawMessage(linkSchema),
			Class: tool.ClassSideEffect, Policy: tool.PolicyConfirm, AdminOnly: true}, question: q, run: run}
	}
	return []tool.Handler{
		&routeTool{spec: tool.Spec{Name: toolLinkList,
			Description: "List the links between modules, each marked when it does not work, and the ports of every running local module: " +
				"its outputs (emits, and notify when it may message the owner), inputs (accepts), the calls it serves and the calls it makes, " +
				"each with its shape. Call it before link.add: a link joins an output to an input of the same shape, or a calling port to a serving one.",
			InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
			Class:       tool.ClassRead, Policy: tool.PolicyAuto, AdminOnly: true}, run: l.list},
		confirm(toolLinkAdd,
			"Connect two modules: from is an output (emits, or notify) or a calling port of one module, to is an input (accepts) or a serving port "+
				"of another, each written module.port with the module's short name, such as battery-reader.reading. The ends must exist, be of "+
				"matching kinds (emits to accepts, calls to serves) with equal shapes, and the links must not form a loop or a chain deeper than "+
				"4 modules; otherwise the answer says why and nothing is asked. It is the node's setting: no module is rebuilt. "+
				"Call link.list first. The owner confirms, seeing both ends and the shape.", l.addQuestion, l.add),
		confirm(toolLinkRemove,
			"Remove a link between two modules; from and to are as link.list shows them. The modules keep running; nothing is rebuilt. The owner confirms.",
			l.removeQuestion, l.remove),
	}
}

// linkSchema is the arguments of link.add and link.remove.
const linkSchema = `{"type":"object","properties":{` +
	`"from":{"type":"string","description":"the producer or the caller: module.port, such as battery-reader.reading"},` +
	`"to":{"type":"string","description":"the receiver or the server: module.port, such as battery-alerts.reading"}},` +
	`"required":["from","to"],"additionalProperties":false}`

// argsOf reads the link of a call.
func (Linking) argsOf(c tool.Call) (settings.Link, error) {
	var a linkArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return settings.Link{}, fmt.Errorf("arguments: %w", err)
	}
	return settings.Link{From: strings.TrimSpace(a.From), To: strings.TrimSpace(a.To)}, nil
}

// refusal checks the new link against the active modules and the links
// there are now, at the question and again at the press; the error is the
// node's reason, for the model.
func (l Linking) refusal(ctx context.Context, link settings.Link) (map[string]links.Module, error) {
	mods, err := l.Modules(ctx)
	if err != nil {
		return nil, err
	}
	existing, err := l.File.Links()
	if err != nil {
		return nil, err
	}
	if slices.Contains(existing, link) {
		return nil, fmt.Errorf("%s is already a link; see link.list", link)
	}
	if ps := links.Added(mods, linksOf(existing), links.Link(link)); len(ps) > 0 {
		words := make([]string, len(ps))
		for i, p := range ps {
			words[i] = p.Words()
		}
		return nil, errors.New("the node refuses this link: " + strings.Join(words, "; ") + "; see link.list for the ports")
	}
	return mods, nil
}

func linksOf(ls []settings.Link) []links.Link {
	out := make([]links.Link, len(ls))
	for i, l := range ls {
		out[i] = links.Link(l)
	}
	return out
}

func (l Linking) addQuestion(ctx context.Context, c tool.Call) (tool.Result, error) {
	link, err := l.argsOf(c)
	if err != nil {
		return tool.Result{}, err
	}
	mods, err := l.refusal(ctx, link)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: c.Speak.Text(texts.AskLinkAdd, texts.Vars{varFrom: texts.Escape(link.From), varTo: texts.Escape(link.To),
		"shape": texts.Escape(links.Describe(mods, links.Link(link)))})}, nil
}

func (l Linking) add(ctx context.Context, c tool.Call) (tool.Result, error) {
	link, err := l.argsOf(c)
	if err != nil {
		return tool.Result{}, err
	}
	if _, err := l.refusal(ctx, link); err != nil { // the modules may have changed since the question
		return tool.Result{}, err
	}
	if err := l.File.AddLink(link); err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: "the link " + link.String() + " is written; no module was rebuilt, and nothing travels over it yet"}, nil
}

// existing is the link a remove names: it must be in the file.
func (l Linking) existing(c tool.Call) (settings.Link, error) {
	link, err := l.argsOf(c)
	if err != nil {
		return settings.Link{}, err
	}
	all, err := l.File.Links()
	if err != nil {
		return settings.Link{}, err
	}
	if !slices.Contains(all, link) {
		return settings.Link{}, errors.New("no such link; see link.list")
	}
	return link, nil
}

func (l Linking) removeQuestion(_ context.Context, c tool.Call) (tool.Result, error) {
	link, err := l.existing(c)
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: c.Speak.Text(texts.AskLinkRemove, texts.Vars{varFrom: texts.Escape(link.From), varTo: texts.Escape(link.To)})}, nil
}

func (l Linking) remove(_ context.Context, c tool.Call) (tool.Result, error) {
	link, err := l.existing(c)
	if err != nil {
		return tool.Result{}, err
	}
	if err := l.File.RemoveLink(link); err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: "the link " + link.String() + " is removed"}, nil
}

// list answers link.list: the ports of the modules, then the links with the
// node's verdict on each.
func (l Linking) list(ctx context.Context, _ tool.Call) (tool.Result, error) {
	mods, err := l.Modules(ctx)
	if err != nil {
		return tool.Result{}, err
	}
	existing, err := l.File.Links()
	if err != nil {
		return tool.Result{}, err
	}
	var b strings.Builder
	b.WriteString("Ports of the running local modules (module, then each port with its shape):\n")
	names := make([]string, 0, len(mods))
	for n := range mods {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		writePorts(&b, n, mods[n])
	}
	if len(names) == 0 {
		b.WriteString("(no local module)\n")
	}
	b.WriteString("\nLinks:\n")
	problems := links.Check(mods, linksOf(existing))
	for _, link := range existing {
		fmt.Fprintf(&b, "- %s%s\n", link, linkVerdict(problems, links.Link(link)))
	}
	if len(existing) == 0 {
		b.WriteString("(none)\n")
	}
	for _, p := range problems {
		if p.Link == (links.Link{}) {
			fmt.Fprintf(&b, "Also: %s\n", p.Words())
		}
	}
	return tool.Result{Text: b.String()}, nil
}

// linkVerdict is " (does not work: reason)" for a link with a problem, and
// "" for one that works.
func linkVerdict(ps []links.Problem, l links.Link) string {
	var why []string
	for _, p := range ps {
		if p.Link == l {
			why = append(why, strings.TrimPrefix(p.Words(), l.String()+": "))
		}
	}
	if len(why) == 0 {
		return ""
	}
	return " (does not work: " + strings.Join(why, "; ") + ")"
}

// writePorts writes the ports of one module, one line per kind that has any.
func writePorts(b *strings.Builder, name string, m links.Module) {
	fmt.Fprintf(b, "- %s\n", name)
	events := func(kind string, ports map[string]runner.Shape) {
		for _, p := range sortedKeys(ports) {
			fmt.Fprintf(b, "    %s %s.%s: %s\n", kind, name, p, ports[p])
		}
	}
	calls := func(kind string, ports map[string]runner.PortCall) {
		for _, p := range sortedKeys(ports) {
			fmt.Fprintf(b, "    %s %s.%s: in %s, out %s\n", kind, name, p, ports[p].In, ports[p].Out)
		}
	}
	events("emits", m.Emits)
	events("accepts", m.Accepts)
	calls("serves", m.Serves)
	calls("calls", m.Calls)
}

// sortedKeys is the keys of m in order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
