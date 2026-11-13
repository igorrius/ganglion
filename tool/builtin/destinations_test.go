package builtin_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/groups"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
)

const (
	forum   = "telegram:-1001"
	k7      = "telegram:-1001/45"
	admin42 = "telegram:user:42"
)

// routingFake is the node's file, the topics and the module host of the
// routing tools.
type routingFake struct {
	dests     []settings.Destination
	groups    []settings.Group
	topics    map[string][]groups.Topic
	mods      []settings.Module
	routed    map[string][]string
	forgotten []string
	removeErr error
}

func (f *routingFake) Destinations() ([]settings.Destination, error) {
	return slices.Clone(f.dests), nil
}

func (f *routingFake) SetDestination(d settings.Destination) error {
	f.dests = append(slices.DeleteFunc(f.dests, func(x settings.Destination) bool { return x.Name == d.Name }), d)
	return nil
}

func (f *routingFake) RemoveDestination(name string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	f.dests = slices.DeleteFunc(f.dests, func(x settings.Destination) bool { return x.Name == name })
	return nil
}

func (f *routingFake) Groups() ([]settings.Group, error) { return slices.Clone(f.groups), nil }

func (f *routingFake) RemoveGroup(chat int64) error {
	f.groups = slices.DeleteFunc(f.groups, func(g settings.Group) bool { return g.Chat == chat })
	return nil
}

func (f *routingFake) Topics(_ context.Context, group string) ([]groups.Topic, error) {
	return f.topics[group], nil
}

func (f *routingFake) Topic(_ context.Context, group string, thread int64) (groups.Topic, bool, error) {
	for _, t := range f.topics[group] {
		if t.Thread == thread {
			return t, true, nil
		}
	}
	return groups.Topic{}, false, nil
}

func (f *routingFake) ForgetTopics(_ context.Context, group string) error {
	f.forgotten = append(f.forgotten, group)
	return nil
}

func (f *routingFake) NotifyTo(_ context.Context, module string, names []string) error {
	if f.routed == nil {
		f.routed = map[string][]string{}
	}
	f.routed[module] = names
	return nil
}

// parseConv is telegram.ParseConv in miniature.
func parseConv(conv string) (chat, thread int64, err error) {
	rest, ok := strings.CutPrefix(conv, "telegram:")
	if !ok {
		return 0, 0, errors.New("not a telegram conversation")
	}
	c, th, _ := strings.Cut(rest, "/")
	if chat, err = strconv.ParseInt(c, 10, 64); err != nil {
		return 0, 0, err
	}
	if th != "" {
		if thread, err = strconv.ParseInt(th, 10, 64); err != nil {
			return 0, 0, err
		}
	}
	return chat, thread, nil
}

func newRouting(f *routingFake) map[string]tool.Handler {
	r := builtin.Routing{Dests: f, Groups: f, Topics: f, Router: f, Parse: parseConv, Private: private,
		Modules: func() ([]settings.Module, error) { return f.mods, nil },
		Key:     func(chat int64) string { return "telegram:" + strconv.FormatInt(chat, 10) }}
	out := map[string]tool.Handler{}
	for _, h := range r.Tools() {
		out[h.Spec().Name] = h
	}
	return out
}

func family() *routingFake {
	return &routingFake{
		groups: []settings.Group{{Chat: -1001, Name: "Family *one*", Role: settings.GroupRoleFamily}},
		topics: map[string][]groups.Topic{forum: {{Group: forum, Thread: 45, Title: "K7 [x](http://evil.example)"}}},
	}
}

func ask(t *testing.T, h tool.Handler, c tool.Call) (string, error) {
	t.Helper()
	q, ok := h.(tool.Questioner)
	if !ok {
		t.Fatalf("%s asks no question", h.Spec().Name)
	}
	return q.Question(context.Background(), c)
}

func routeCall(name, args string) tool.Call {
	c := call(name, args)
	c.Speak = texts.Speaker{Lang: texts.English}
	return c
}

// Every routing tool is the admin's, and every one that changes something
// asks first.
func TestRoutingToolsAreTheAdminsAndConfirmed(t *testing.T) {
	tools := newRouting(family())
	if len(tools) != 5 {
		t.Fatalf("%d tools", len(tools))
	}
	for name, h := range tools {
		s := h.Spec()
		if !s.AdminOnly {
			t.Errorf("%s is offered to everyone", name)
		}
		if name != "destination.list" && s.Policy != tool.PolicyConfirm {
			t.Errorf("%s changes the routing without asking", name)
		}
		if err := tool.CheckArgs(s, []byte(`{"unknown":1}`)); err == nil {
			t.Errorf("%s takes unknown arguments", name)
		}
	}
}

func TestDestinationSetNamesATopic(t *testing.T) {
	f := family()
	set := newRouting(f)["destination.set"]
	c := routeCall("destination.set", `{"name":"family-k7","conv":"`+k7+`"}`)
	q, err := ask(t, set, c)
	if err != nil {
		t.Fatal(err)
	}
	// The question names the place by its titles, escaped: a title is outside text.
	for _, want := range []string{"«family-k7»", `K7 \[x\](http://evil.example)`, `Family \*one\*`} {
		if !strings.Contains(q, want) {
			t.Errorf("the question lacks %q: %q", want, q)
		}
	}
	if _, err := set.Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if len(f.dests) != 1 || f.dests[0] != (settings.Destination{Name: "family-k7", Conv: k7}) {
		t.Fatalf("destinations %+v", f.dests)
	}
	// A topic the node has not seen is named by its number; the group and
	// the caller's own private chat are places too.
	for conv, want := range map[string]string{forum + "/8": "topic number 8", forum: "the group", "telegram:42": "your private chat"} {
		q, err := ask(t, set, routeCall("destination.set", `{"name":"x","conv":"`+conv+`"}`))
		if err != nil || !strings.Contains(q, want) {
			t.Errorf("%s: %q, %v; want %q", conv, q, err, want)
		}
	}
}

// Abuse: a destination cannot point at a group nobody connected, at a
// stranger's or another person's private chat, or at another transport.
func TestDestinationSetRefusesOtherPlaces(t *testing.T) {
	f := family()
	set := newRouting(f)["destination.set"]
	for _, conv := range []string{"telegram:-2002", "telegram:-2002/5", "telegram:7", "telegram:666", "matrix:-1001", "nowhere", ""} {
		c := routeCall("destination.set", `{"name":"x","conv":"`+conv+`"}`)
		if _, err := ask(t, set, c); err == nil {
			t.Errorf("a question for %q", conv)
		}
		if _, err := set.Run(context.Background(), c); err == nil {
			t.Errorf("a destination at %q", conv)
		}
	}
	if len(f.dests) != 0 {
		t.Fatalf("destinations %+v", f.dests)
	}
}

func TestDestinationRemove(t *testing.T) {
	f := family()
	f.dests = []settings.Destination{{Name: "family-k7", Conv: k7}}
	remove := newRouting(f)["destination.remove"]
	if _, err := ask(t, remove, routeCall("destination.remove", `{"name":"nowhere"}`)); err == nil {
		t.Fatal("a question for a name that is no destination")
	}
	c := routeCall("destination.remove", `{"name":"family-k7"}`)
	if q, err := ask(t, remove, c); err != nil || !strings.Contains(q, "family-k7") {
		t.Fatalf("question %q, %v", q, err)
	}
	// While a module uses it the file refuses, and the tool passes that on.
	f.removeErr = settings.ErrDestinationUsed
	if _, err := remove.Run(context.Background(), c); !errors.Is(err, settings.ErrDestinationUsed) || len(f.dests) != 1 {
		t.Fatalf("err %v, destinations %+v", err, f.dests)
	}
	f.removeErr = nil
	if _, err := remove.Run(context.Background(), c); err != nil || len(f.dests) != 0 {
		t.Fatalf("err %v, destinations %+v", err, f.dests)
	}
}

func TestModuleNotifyToRoutesAModule(t *testing.T) {
	f := family()
	f.dests = []settings.Destination{{Name: "family-k7", Conv: k7}}
	route := newRouting(f)["module.notify_to"]
	c := routeCall("module.notify_to", `{"module":"battery-alerts","destinations":["family-k7","owner"]}`)
	if q, err := ask(t, route, c); err != nil || !strings.Contains(q, "battery-alerts") || !strings.Contains(q, "family-k7, owner") {
		t.Fatalf("question %q, %v", q, err)
	}
	if _, err := route.Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if got := f.routed["battery-alerts"]; !slices.Equal(got, []string{"family-k7", "owner"}) {
		t.Fatalf("routed %v", got)
	}
	// An empty list is the owner alone.
	if _, err := route.Run(context.Background(), routeCall("module.notify_to", `{"module":"battery-alerts","destinations":[]}`)); err != nil {
		t.Fatal(err)
	}
	if got := f.routed["battery-alerts"]; !slices.Equal(got, []string{"owner"}) {
		t.Fatalf("routed %v after an empty list", got)
	}
	// Abuse: a raw conversation key is no destination, so a module cannot be
	// pointed at a place nobody named.
	for _, args := range []string{`{"module":"battery-alerts","destinations":["telegram:-2002"]}`, `{"module":"","destinations":["owner"]}`} {
		if _, err := route.Run(context.Background(), routeCall("module.notify_to", args)); err == nil {
			t.Errorf("%s was accepted", args)
		}
	}
}

func TestGroupDisconnect(t *testing.T) {
	f := family()
	f.dests = []settings.Destination{{Name: "family-k7", Conv: k7}}
	disconnect := newRouting(f)["group.disconnect"]
	c := routeCall("group.disconnect", `{"group":"`+forum+`"}`)
	// A destination points at one of its topics: refused, naming it.
	if _, err := ask(t, disconnect, c); err == nil || !strings.Contains(err.Error(), "family-k7") {
		t.Fatalf("err = %v, want a refusal naming the destination", err)
	}
	f.dests = nil
	if q, err := ask(t, disconnect, c); err != nil || !strings.Contains(q, `Family \*one\*`) {
		t.Fatalf("question %q, %v", q, err)
	}
	if _, err := disconnect.Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if len(f.groups) != 0 || !slices.Equal(f.forgotten, []string{forum}) {
		t.Fatalf("groups %+v, forgotten %v", f.groups, f.forgotten)
	}
	for _, g := range []string{forum, k7, "telegram:42", "x"} {
		if _, err := disconnect.Run(context.Background(), routeCall("group.disconnect", `{"group":"`+g+`"}`)); err == nil {
			t.Errorf("group.disconnect accepted %q", g)
		}
	}
}

// The list shows the keys the other tools take; titles reach the model
// only inside the wrapper for outside content.
func TestDestinationListWrapsTitles(t *testing.T) {
	f := family()
	f.dests = []settings.Destination{{Name: "family-k7", Conv: k7}}
	f.mods = []settings.Module{{Name: "local/battery-alerts", NotifyTo: []string{"family-k7"}}, {Name: "local/deye-reader"}}
	list := newRouting(f)["destination.list"]
	if s := list.Spec(); s.Class != tool.ClassExternal || s.Policy != tool.PolicyAuto {
		t.Fatalf("spec %+v", s)
	}
	res, err := list.Run(context.Background(), routeCall("destination.list", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- family-k7: " + k7, "- owner:", forum + "/45  topic", "- local/battery-alerts: family-k7", "- local/deye-reader: owner"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("the list lacks %q:\n%s", want, res.Text)
		}
	}
	if plain := tool.StripExternal(res.Text); strings.Contains(plain, "K7") || strings.Contains(plain, "Family") {
		t.Fatalf("a title is outside the wrapper:\n%s", plain)
	}
}

// Abuse: a scheduled run never changes where messages go.
func TestRoutingRefusesAScheduledRun(t *testing.T) {
	f := family()
	for name, h := range newRouting(f) {
		c := routeCall(name, `{}`)
		c.Grant = &tool.Grant{}
		if _, err := h.Run(context.Background(), c); err == nil || !strings.Contains(err.Error(), "scheduled run") {
			t.Errorf("%s in a scheduled run: %v", name, err)
		}
	}
}

// message.send reaches a destination by its name (G-153).
func TestMessageSendToADestination(t *testing.T) {
	cv := convs()
	cv.Dests = func(name string) (string, string, bool, error) {
		return "telegram", k7, name == "family-k7", nil
	}
	send := builtin.NewMessageSend(cv)
	c := routeCall("message.send", `{"conv":"family-k7","text":"battery 85%"}`)
	if q, err := send.Question(context.Background(), c); err != nil || !strings.Contains(q, "family-k7") || !strings.Contains(q, k7) {
		t.Fatalf("question %q, %v", q, err)
	}
	res, err := send.Run(context.Background(), c)
	if err != nil || len(res.Out) != 1 || res.Out[0].Conv != k7 || res.Out[0].Transport != "telegram" || res.Out[0].Text != "battery 85%" {
		t.Fatalf("result %+v, %v", res, err)
	}
	// The forum as a whole is still no conversation the node knows.
	if _, err := send.Run(context.Background(), routeCall("message.send", `{"conv":"`+forum+`","text":"x"}`)); !errors.Is(err, builtin.ErrUnknownConv) {
		t.Fatalf("err = %v, want ErrUnknownConv", err)
	}
}
