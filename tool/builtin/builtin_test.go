package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/policy"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
)

// states is the agent's state store: a conversation exists when it has a
// non-empty state.
type states map[string]json.RawMessage

func (s states) State(_ context.Context, conv string) (json.RawMessage, error) { return s[conv], nil }

// failingStates cannot read the store.
type failingStates struct{}

func (failingStates) State(context.Context, string) (json.RawMessage, error) {
	return nil, errors.New("disk on fire")
}

func listed(principals ...string) builtin.ListedFunc {
	return func(_ context.Context, p string) (bool, error) {
		return slices.Contains(principals, p), nil
	}
}

// private is telegram.PrivateConv in miniature.
func private(principal string) (transport, conv string, ok bool) {
	id, ok := strings.CutPrefix(principal, "telegram:user:")
	return "telegram", "telegram:" + id, ok
}

func convs() builtin.Convs {
	return builtin.Convs{
		States:  states{"telegram:42": json.RawMessage(`{"v":2}`), "telegram:empty": nil},
		Listed:  listed("telegram:user:7"),
		Private: private,
		Names:   names,
	}
}

// names knows one person on the list.
func names(_ context.Context, p string) (string, error) {
	if p == "telegram:user:7" {
		return "Olena", nil
	}
	return "", nil
}

func call(name string, args string) tool.Call {
	return tool.Call{TurnID: "E1", N: 2, Conv: "telegram:42", Transport: "telegram", Principal: "telegram:user:42", Admin: true,
		Name: name, Args: json.RawMessage(args)}
}

func TestMessageSendUnknownConv(t *testing.T) {
	send := builtin.NewMessageSend(convs())
	for _, tc := range []struct{ name, conv string }{
		{"no state at all", "telegram:999"},
		{"an empty state", "telegram:empty"},
		{"a principal not on the list", "telegram:user:8"},
		{"nothing", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"conv": tc.conv, "text": "hi"})
			res, err := send.Run(context.Background(), call("message.send", string(args)))
			if !errors.Is(err, builtin.ErrUnknownConv) {
				t.Fatalf("err %v, want ErrUnknownConv", err)
			}
			if tc.conv != "" && !strings.Contains(err.Error(), tc.conv) {
				t.Errorf("the error %q does not name the conv", err)
			}
			if len(res.Out) != 0 {
				t.Fatalf("a failed call put %+v in Out", res.Out)
			}
		})
	}
}

func TestMessageSendBadArguments(t *testing.T) {
	send := builtin.NewMessageSend(convs())
	for _, args := range []string{`{`, `[1]`, `{"conv":"telegram:42"}`, `{"conv":"telegram:42","text":"  "}`, `{"conv":3,"text":"x"}`} {
		res, err := send.Run(context.Background(), call("message.send", args))
		if err == nil || len(res.Out) != 0 {
			t.Errorf("%s: result %+v, err %v; want an error and no message", args, res, err)
		}
	}
}

func TestMessageSendStateErrorSendsNothing(t *testing.T) {
	c := convs()
	c.States = failingStates{}
	res, err := builtin.NewMessageSend(c).Run(context.Background(), call("message.send", `{"conv":"telegram:5","text":"x"}`))
	if err == nil || errors.Is(err, builtin.ErrUnknownConv) || len(res.Out) != 0 {
		t.Fatalf("result %+v, err %v; want a plain error and no message", res, err)
	}
}

func TestMessageSendKey(t *testing.T) {
	send := builtin.NewMessageSend(convs())
	for _, tc := range []struct {
		name, conv string
		callConv   string // the conversation of the call, when not the usual
		want       event.Outgoing
	}{
		{"a known conversation", "telegram:42", "", event.Outgoing{Transport: "telegram", Conv: "telegram:42"}},
		{"the conversation of the call, even before its state exists", "telegram:new", "telegram:new", event.Outgoing{Transport: "telegram", Conv: "telegram:new"}},
		{"a principal's private chat", "telegram:user:7", "", event.Outgoing{Transport: "telegram", Conv: "telegram:7"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"conv": tc.conv, "text": "dinner at 7"})
			c := call("message.send", string(args))
			if tc.callConv != "" {
				c.Conv = tc.callConv
			}
			res, err := send.Run(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			tc.want.IdemKey, tc.want.Text = "E1:2:send", "dinner at 7"
			if len(res.Out) != 1 || res.Out[0].IdemKey != c.Key()+":send" || res.Out[0].Transport != tc.want.Transport ||
				res.Out[0].Conv != tc.want.Conv || res.Out[0].Text != tc.want.Text {
				t.Fatalf("Out = %+v, want one %+v", res.Out, tc.want)
			}
		})
	}
}

func TestMessageSendOnlyKnownStateConvUsesTheCallTransport(t *testing.T) {
	c := convs()
	c.States = states{"matrix:room": json.RawMessage(`{"v":2}`)}
	cl := call("message.send", `{"conv":"matrix:room","text":"x"}`)
	cl.Transport = "matrix"
	res, err := builtin.NewMessageSend(c).Run(context.Background(), cl)
	if err != nil || len(res.Out) != 1 || res.Out[0].Transport != "matrix" || res.Out[0].Conv != "matrix:room" {
		t.Fatalf("result %+v, err %v", res, err)
	}
}

// In a scheduled task's turn the call's own conversation is the task's;
// a message to it goes to the task's reply target.
func TestMessageSendInATaskGoesToItsReplyTarget(t *testing.T) {
	c := call("message.send", `{"conv":"task:j1","text":"USD 41.2"}`)
	c.Conv, c.Grant = "task:j1", &tool.Grant{Tools: []string{"message.send"}, ReplyTo: "telegram:42"}
	res, err := builtin.NewMessageSend(convs()).Run(context.Background(), c)
	if err != nil || len(res.Out) != 1 || res.Out[0].Conv != "telegram:42" || res.Out[0].Transport != "telegram" {
		t.Fatalf("result %+v, %v", res, err)
	}
}

func TestMessageSendSpec(t *testing.T) {
	s := builtin.NewMessageSend(convs()).Spec()
	if s.Name != "message.send" || s.Class != tool.ClassSideEffect || !s.AdminOnly {
		t.Fatalf("spec %+v", s)
	}
	assertSchema(t, s, "conv", "text")
}

// assertSchema checks that the input schema is an object that requires
// exactly the named properties.
func assertSchema(t *testing.T, s tool.Spec, required ...string) {
	t.Helper()
	var schema struct {
		Type       string                     `json:"type"`
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(s.InputSchema, &schema); err != nil {
		t.Fatalf("%s: schema: %v", s.Name, err)
	}
	if schema.Type != "object" || strings.Join(schema.Required, ",") != strings.Join(required, ",") {
		t.Fatalf("%s: schema %s", s.Name, s.InputSchema)
	}
	for _, r := range required {
		if _, ok := schema.Properties[r]; !ok {
			t.Errorf("%s: no property %q", s.Name, r)
		}
	}
	if s.Description == "" {
		t.Errorf("%s: no description", s.Name)
	}
}

// lamp is a service with one switch, "load.a", set to off and reported on.
func lamp(t *testing.T) *cells.Service {
	t.Helper()
	s := cells.NewService(cells.NewMemStore())
	ctx := context.Background()
	for _, d := range []cells.Def{{ID: "load.a", Kind: cells.KindSwitch}, {ID: "grid.v", Kind: cells.KindSensor}} {
		if err := s.Define(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.SetDesired(ctx, "load.a", json.RawMessage(`"off"`), "test", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Report(ctx, "load.a", json.RawMessage(`"on"`), cells.CauseExternal, "test"); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCellsGet(t *testing.T) {
	get := builtin.NewCellsGet(lamp(t))
	res, err := get.Run(context.Background(), call("cells.get", `{"id":"load.a"}`))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"id":"load.a","kind":"switch","desired":"off","reported":"on","in_sync":false}`; res.Text != want {
		t.Fatalf("text %s, want %s", res.Text, want)
	}
	res, err = get.Run(context.Background(), call("cells.get", `{"id":"grid.v"}`))
	if err != nil || res.Text != `{"id":"grid.v","kind":"sensor","desired":null,"reported":null,"in_sync":false}` {
		t.Fatalf("sensor: %s, %v", res.Text, err)
	}
	if res.External {
		t.Error("a cell's value is not outside content")
	}
}

func TestCellsGetUnknownAndBad(t *testing.T) {
	get := builtin.NewCellsGet(lamp(t))
	_, err := get.Run(context.Background(), call("cells.get", `{"id":"nope"}`))
	if !errors.Is(err, cells.ErrNotFound) || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("unknown cell: %v", err)
	}
	for _, args := range []string{`{`, `{}`, `{"id":""}`} {
		if _, err := get.Run(context.Background(), call("cells.get", args)); err == nil {
			t.Errorf("%s: no error", args)
		}
	}
}

func TestCellsSet(t *testing.T) {
	svc := lamp(t)
	set := builtin.NewCellsSet(svc)
	c := call("cells.set", `{"id":"load.a","value":"on"}`)
	res, err := set.Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	cell, _ := svc.Get(context.Background(), "load.a")
	if string(cell.Desired) != `"on"` {
		t.Fatalf("desired %s", cell.Desired)
	}
	if !strings.Contains(res.Text, `"desired":"on"`) {
		t.Errorf("result %q does not show the new desired value", res.Text)
	}
	// The source of the change is the call key.
	hist, err := svc.History(context.Background(), "load.a", 1)
	if err != nil || len(hist) != 1 || hist[0].Source != c.Key() || hist[0].Field != cells.FieldDesired {
		t.Fatalf("history %+v, %v", hist, err)
	}
}

func TestCellsSetRefuses(t *testing.T) {
	svc := lamp(t)
	set := builtin.NewCellsSet(svc)
	for name, args := range map[string]string{
		"not on or off": `{"id":"load.a","value":"toggle"}`,
		"a boolean":     `{"id":"load.a","value":true}`,
		"upper case":    `{"id":"load.a","value":"ON"}`,
		"no value":      `{"id":"load.a"}`,
		"no id":         `{"value":"on"}`,
		"bad json":      `{`,
	} {
		if _, err := set.Run(context.Background(), call("cells.set", args)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := set.Run(context.Background(), call("cells.set", `{"id":"nope","value":"on"}`)); !errors.Is(err, cells.ErrNotFound) {
		t.Errorf("unknown cell: %v", err)
	}
	if _, err := set.Run(context.Background(), call("cells.set", `{"id":"grid.v","value":"on"}`)); !errors.Is(err, cells.ErrWrongKind) {
		t.Errorf("a sensor: %v", err)
	}
	if cell, _ := svc.Get(context.Background(), "load.a"); string(cell.Desired) != `"off"` {
		t.Errorf("a refused call changed the cell: %s", cell.Desired)
	}
}

func TestCellsSpecs(t *testing.T) {
	svc := lamp(t)
	get, set := builtin.NewCellsGet(svc).Spec(), builtin.NewCellsSet(svc).Spec()
	if get.Name != "cells.get" || get.Class != tool.ClassRead || !get.AdminOnly {
		t.Errorf("get %+v", get)
	}
	if set.Name != "cells.set" || set.Class != tool.ClassSideEffect || !set.AdminOnly {
		t.Errorf("set %+v", set)
	}
	assertSchema(t, get, "id")
	assertSchema(t, set, "id", "value")
}

// asker parks a call without storing it.
type asker struct{ asked []string }

func (a *asker) Parked(context.Context, tool.Call) (string, error) { return "", nil }

func (a *asker) Ask(_ context.Context, _ tool.Call, q string) (string, error) {
	a.asked = append(a.asked, q)
	return "pend-1", nil
}

func (a *asker) Outgoing(context.Context, string) (event.Outgoing, error) {
	return event.Outgoing{IdemKey: "pend-1:q", Text: "Approve?"}, nil
}

// The tools the owner has not confirmed do nothing: the real handlers,
// behind the real policy.
func TestCellsSetNeedsConfirm(t *testing.T) {
	svc := lamp(t)
	reg, err := tool.NewRegistry(router.Local{}, builtin.NewCellsGet(svc), builtin.NewCellsSet(svc), builtin.NewMessageSend(convs()))
	if err != nil {
		t.Fatal(err)
	}
	ask := &asker{}
	run := tool.Chain(reg.Run, policy.New(policy.DefaultConfig(), reg, ask).Middleware())

	res, err := run(context.Background(), call("cells.set", `{"id":"load.a","value":"on"}`))
	if err != nil || res.Pending != "pend-1" {
		t.Fatalf("cells.set = %+v, %v; want it parked", res, err)
	}
	if cell, _ := svc.Get(context.Background(), "load.a"); string(cell.Desired) != `"off"` {
		t.Fatalf("the cell changed before the owner answered: %s", cell.Desired)
	}

	// Sending a message is confirmed too, even to the conversation of the call.
	res, err = run(context.Background(), call("message.send", `{"conv":"telegram:42","text":"hi"}`))
	if err != nil || res.Pending == "" {
		t.Fatalf("message.send = %+v, %v; want it parked", res, err)
	}
	for _, o := range res.Out {
		if o.Text == "hi" {
			t.Fatalf("the message went out before the owner answered: %+v", res.Out)
		}
	}

	// A read does not ask.
	res, err = run(context.Background(), call("cells.get", `{"id":"load.a"}`))
	if err != nil || res.Pending != "" || !strings.Contains(res.Text, `"id":"load.a"`) {
		t.Fatalf("cells.get = %+v, %v", res, err)
	}
}
