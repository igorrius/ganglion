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

func (f *routingFake) NameTopic(_ context.Context, group string, thread int64, title string) error {
	if f.topics == nil {
		f.topics = map[string][]groups.Topic{}
	}
	f.topics[group] = append(f.topics[group], groups.Topic{Group: group, Thread: thread, Title: title})
	return nil
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
	out := map[string]tool.Handler{}
	for _, h := range routingOf(f).Tools() {
		out[h.Spec().Name] = h
	}
	return out
}

// link is telegram.ParseLink in miniature.
func link(s string) (string, bool) {
	rest, ok := strings.CutPrefix(s, "https://t.me/c/")
	if !ok {
		return "", false
	}
	id, thread, ok := strings.Cut(rest, "/")
	return "telegram:-100" + id + "/" + thread, ok
}

func routingOf(f *routingFake) builtin.Routing {
	return builtin.Routing{Dests: f, Groups: f, Topics: f, Router: f, Parse: parseConv, Private: private, Link: link,
		Modules: func() ([]settings.Module, error) { return f.mods, nil },
		Key:     func(chat int64) string { return "telegram:" + strconv.FormatInt(chat, 10) }}
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
	// The key of a forum as a whole is its General topic, and is called that.
	for conv, want := range map[string]string{forum + "/8": "topic number 8", forum: "the General topic", "telegram:42": "your private chat"} {
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
	route := newRouting(f)["module.notify_to"]
	// A topic by its key and the owner: one question that names the places
	// by their titles, one run that also writes the destination nobody had
	// to name.
	c := routeCall("module.notify_to", `{"module":"battery-alerts","destinations":["`+k7+`","owner"]}`)
	q, err := ask(t, route, c)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"battery-alerts", `the topic K7 \[x\](http://evil.example) of the group Family \*one\*`, "your private chat"} {
		if !strings.Contains(q, want) {
			t.Errorf("the question lacks %q: %q", want, q)
		}
	}
	if len(f.dests) != 0 {
		t.Fatalf("a question wrote destinations %+v", f.dests)
	}
	if _, err := route.Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	const made = "chat-1001-topic-45"
	if len(f.dests) != 1 || f.dests[0] != (settings.Destination{Name: made, Conv: k7}) {
		t.Fatalf("destinations %+v", f.dests)
	}
	if got := f.routed["battery-alerts"]; !slices.Equal(got, []string{made, "owner"}) {
		t.Fatalf("routed %v", got)
	}
	// The same place again reuses its destination; so does its link, and a
	// place named twice counts once.
	for _, args := range []string{
		`{"module":"deye","destinations":["` + k7 + `"]}`,
		`{"module":"deye","destinations":["` + made + `"]}`,
		`{"module":"deye","destinations":["https://t.me/c/1/45","` + k7 + `"]}`,
	} {
		f.routed = nil
		if _, err := route.Run(context.Background(), routeCall("module.notify_to", args)); err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		if got := f.routed["deye"]; !slices.Equal(got, []string{made}) || len(f.dests) != 1 {
			t.Fatalf("%s: routed %v, destinations %+v", args, got, f.dests)
		}
	}
	// An empty list is the owner alone.
	if _, err := route.Run(context.Background(), routeCall("module.notify_to", `{"module":"battery-alerts","destinations":[]}`)); err != nil {
		t.Fatal(err)
	}
	if got := f.routed["battery-alerts"]; !slices.Equal(got, []string{"owner"}) {
		t.Fatalf("routed %v after an empty list", got)
	}
	// Abuse: a group nobody connected, a stranger's chat and a made-up name
	// are no places, so a module cannot be pointed at them.
	for _, args := range []string{
		`{"module":"battery-alerts","destinations":["telegram:-2002"]}`,
		`{"module":"battery-alerts","destinations":["https://t.me/c/2/5"]}`,
		`{"module":"battery-alerts","destinations":["telegram:666"]}`,
		`{"module":"battery-alerts","destinations":["nowhere"]}`,
		`{"module":"","destinations":["owner"]}`,
	} {
		if _, err := route.Run(context.Background(), routeCall("module.notify_to", args)); err == nil {
			t.Errorf("%s was accepted", args)
		}
	}
	if len(f.dests) != 1 {
		t.Fatalf("a refused route wrote destinations %+v", f.dests)
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
}

// message.send reaches a topic of a connected group by its key or by its
// link, with nothing named first, and the question says where in words;
// a group nobody connected stays out of reach.
func TestMessageSendToAPlace(t *testing.T) {
	r := routingOf(family())
	cv := convs()
	cv.Place, cv.Called = r.Place, r.Called
	send := builtin.NewMessageSend(cv)
	for _, target := range []string{k7, "https://t.me/c/1/45"} {
		c := routeCall("message.send", `{"conv":"`+target+`","text":"battery 85%"}`)
		if q, err := send.Question(context.Background(), c); err != nil || !strings.Contains(q, "the topic K7") {
			t.Fatalf("%s: question %q, %v", target, q, err)
		}
		res, err := send.Run(context.Background(), c)
		if err != nil || len(res.Out) != 1 || res.Out[0].Conv != k7 || res.Out[0].Transport != "telegram" {
			t.Fatalf("%s: result %+v, %v", target, res, err)
		}
	}
	for _, target := range []string{"telegram:-2002/5", "https://t.me/c/2/5"} {
		if _, err := send.Run(context.Background(), routeCall("message.send", `{"conv":"`+target+`","text":"x"}`)); !errors.Is(err, builtin.ErrUnknownConv) {
			t.Errorf("%s: err = %v, want ErrUnknownConv", target, err)
		}
	}
}

// The live case (G-153, 2026-11-13): the owner names a topic the node has
// not seen and sends its link. One call routes the module; the question
// shows the owner's name for the topic beside its number; after the press
// the node knows the topic by that name, and the answer gives the model no
// internal name to tidy up.
func TestRouteToATopicTheNodeHasNotSeen(t *testing.T) {
	f := family()
	tools := newRouting(f)
	route := tools["module.notify_to"]
	c := routeCall("module.notify_to", `{"module":"battery-alerts","destinations":["https://t.me/c/1/8","owner"],"titles":{"https://t.me/c/1/8":"K7 *new*"}}`)
	q, err := ask(t, route, c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q, `the topic «K7 \*new\*» (number 8) of the group Family \*one\*`) || !strings.Contains(q, "your private chat") {
		t.Fatalf("question %q", q)
	}
	if _, ok, _ := f.Topic(context.Background(), forum, 8); ok {
		t.Fatal("a question stored the title: only the owner's press may")
	}
	res, err := route.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := f.Topic(context.Background(), forum, 8); !ok || got.Title != "K7 *new*" {
		t.Fatalf("topic %+v %v", got, ok)
	}
	if strings.Contains(res.Text, "chat-1001") || !strings.Contains(res.Text, "Everything is set up") {
		t.Fatalf("the answer shows an internal name or invites more steps: %q", res.Text)
	}
	// From now on the list shows the topic, and a question names it plainly.
	list, err := tools["destination.list"].Run(context.Background(), routeCall("destination.list", `{}`))
	if err != nil || !strings.Contains(list.Text, forum+"/8  topic") {
		t.Fatalf("list %q, %v", list.Text, err)
	}
	q, err = ask(t, route, routeCall("module.notify_to", `{"module":"deye","destinations":["`+forum+`/8"]}`))
	if err != nil || !strings.Contains(q, "the topic K7") || strings.Contains(q, "number 8") {
		t.Fatalf("question after the topic is known: %q, %v", q, err)
	}
	// A title given for a topic the node has seen changes nothing: what
	// Telegram said stands.
	if _, err := route.Run(context.Background(), routeCall("module.notify_to", `{"module":"deye","destinations":["`+k7+`"],"titles":{"`+k7+`":"Spoofed"}}`)); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := f.Topic(context.Background(), forum, 45); strings.Contains(got.Title, "Spoofed") {
		t.Fatalf("a given title replaced a seen one: %+v", got)
	}
}

// The list ends with what to do, since a tool's answer is read every time:
// never the group's own key for a named topic, ask for the link, speak in
// titles.
func TestDestinationListSaysWhatToDo(t *testing.T) {
	res, err := newRouting(family())["destination.list"].Run(context.Background(), routeCall("destination.list", `{}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Never use it when the owner named a topic", "Copy topic link", `"titles"`, "never by keys, numbers or destination names"} {
		if !strings.Contains(res.Text, want) {
			t.Errorf("the list lacks %q", want)
		}
	}
}

// message.send to a topic the node has not seen: the owner's name for it
// is in the question and is remembered after the send.
func TestMessageSendLearnsATopicsTitle(t *testing.T) {
	f := family()
	r := routingOf(f)
	cv := convs()
	cv.Place, cv.Called, cv.Learn = r.Place, r.Called, r.Learn
	send := builtin.NewMessageSend(cv)
	c := routeCall("message.send", `{"conv":"https://t.me/c/1/8","text":"hi","title":"K7"}`)
	if q, err := send.Question(context.Background(), c); err != nil || !strings.Contains(q, "the topic «K7» (number 8)") {
		t.Fatalf("question %q, %v", q, err)
	}
	if _, err := send.Run(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if got, ok, _ := f.Topic(context.Background(), forum, 8); !ok || got.Title != "K7" {
		t.Fatalf("topic %+v %v", got, ok)
	}
}
