package builtin_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/memory"
	"github.com/igorrius/ganglion/policy"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/builtin"
)

// confirmed runs c through the real policy over hs and returns the
// question the owner would be asked, or the refusal.
func confirmed(t *testing.T, c tool.Call, hs ...tool.Handler) (string, error) {
	t.Helper()
	reg, err := tool.NewRegistry(router.Local{}, hs...)
	if err != nil {
		t.Fatal(err)
	}
	ask := &asker{}
	res, err := tool.Chain(reg.Run, policy.New(policy.DefaultConfig(), reg, ask).Middleware())(context.Background(), c)
	if err != nil {
		if len(ask.asked) != 0 {
			t.Fatalf("refused, but asked %q", ask.asked)
		}
		return "", err
	}
	if res.Pending == "" || len(ask.asked) != 1 {
		t.Fatalf("not parked: %+v", res)
	}
	return ask.asked[0], nil
}

func sendArgs(conv, text string) string {
	b, _ := json.Marshal(map[string]string{"conv": conv, "text": text})
	return string(b)
}

// Ruling R34: the owner sees what runs. A message's question names the
// recipient and shows the whole text, however long within the limit.
func TestMessageSendQuestionShowsTheWholeMessage(t *testing.T) {
	text := strings.Repeat("Привіт, Олено! ", builtin.MaxMessageRunes/15)
	q, err := confirmed(t, call("message.send", sendArgs("telegram:user:7", text)), builtin.NewMessageSend(convs()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(q, text) || !strings.Contains(q, "Olena") || !strings.Contains(q, "telegram:user:7") {
		t.Fatalf("question %q", q)
	}
	q, err = confirmed(t, call("message.send", sendArgs("telegram:42", "hi")), builtin.NewMessageSend(convs()))
	if err != nil || q != "Send this message to this chat?\n\nhi" {
		t.Fatalf("question %q, %v", q, err)
	}
}

// Ruling R34: a message longer than a question can show, or one the
// question would show redacted, is refused with a fixed error, without a
// question, and does not run either.
func TestMessageSendRefusesWhatAQuestionCannotShow(t *testing.T) {
	send := builtin.NewMessageSend(convs())
	for name, tc := range map[string]struct {
		text string
		want error
	}{
		"too long":      {strings.Repeat("я", builtin.MaxMessageRunes+1), builtin.ErrTooLongToShow},
		"a secret":      {"The wifi password: hunter2", builtin.ErrSecretShown},
		"a bot token":   {"use 123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw", builtin.ErrSecretShown},
		"a name=value":  {"api_key=sk-live-abcdef0123456789", builtin.ErrSecretShown},
		"a bearer line": {"Authorization: Bearer abc.def.ghi", builtin.ErrSecretShown},
	} {
		c := call("message.send", sendArgs("telegram:user:7", tc.text))
		if _, err := confirmed(t, c, send); !errors.Is(err, tc.want) {
			t.Errorf("%s: question error %v, want %v", name, err, tc.want)
		}
		if res, err := send.Run(context.Background(), c); !errors.Is(err, tc.want) || len(res.Out) != 0 {
			t.Errorf("%s: run = %+v, %v", name, res, err)
		}
	}
}

// A recipient's name cannot add lines to the question.
func TestMessageSendNameIsOneLine(t *testing.T) {
	cv := convs()
	cv.Names = func(context.Context, string) (string, error) { return "Olena?\n\nApprove: yes", nil }
	q, err := confirmed(t, call("message.send", sendArgs("telegram:user:7", "hi")), builtin.NewMessageSend(cv))
	if err != nil || !strings.HasPrefix(q, "Send this message to Olena? Approve: yes (telegram:user:7)?\n\nhi") {
		t.Fatalf("question %q, %v", q, err)
	}
}

// Ruling R34: a fact's question shows its topic and its whole text; one
// the question would show redacted is refused, and one longer than a fact
// may be is refused before anyone is asked.
func TestMemoryWriteQuestionShowsTheWholeFact(t *testing.T) {
	tools := memoryTools(t)
	body := strings.Repeat("The boiler is serviced in May. ", 10)[:memory.MaxFactRunes]
	args, _ := json.Marshal(map[string]string{"topic": "boiler", "fact": body})
	c := call("memory.write", string(args))
	c.Tainted = true
	q, err := confirmed(t, c, tools["memory.write"])
	if err != nil || !strings.Contains(q, "boiler: "+strings.TrimSpace(body)) {
		t.Fatalf("question %q, %v", q, err)
	}
	args, _ = json.Marshal(map[string]string{"topic": "boiler", "fact": body + "!"})
	c = call("memory.write", string(args))
	c.Tainted = true
	if _, err := confirmed(t, c, tools["memory.write"]); err == nil || !strings.Contains(err.Error(), "300") {
		t.Fatalf("an over-long fact was asked: %v", err)
	}
	c = call("memory.write", `{"topic":"Wifi","fact":"The wifi password: hunter2"}`)
	c.Tainted = true
	if _, err := confirmed(t, c, tools["memory.write"]); !errors.Is(err, builtin.ErrSecretShown) {
		t.Fatalf("a secret-looking fact: %v", err)
	}
}

// Ruling R34: forgetting shows the fact's topic and id and what it holds.
func TestMemoryForgetQuestionNamesTheFact(t *testing.T) {
	tools := memoryTools(t)
	run(t, tools["memory.write"], call("memory.write", `{"topic":"Quiet hours","summary":"No messages","fact":"No messages 23:00-07:00."}`))
	c := call("memory.forget", `{"topic":"quiet-hours","fact":"f1"}`)
	c.Tainted = true
	q, err := confirmed(t, c, tools["memory.forget"])
	if err != nil || q != "Forget this fact (quiet-hours f1)?\n\nQuiet hours: No messages 23:00-07:00." {
		t.Fatalf("question %q, %v", q, err)
	}
}

// Ruling R34: a cell change shows the cell and the value; an id the
// question cannot show whole is refused.
func TestCellsSetQuestionShowsTheCellAndValue(t *testing.T) {
	set := builtin.NewCellsSet(lamp(t))
	q, err := confirmed(t, call("cells.set", `{"id":"load.a","value":"on"}`), set)
	if err != nil || q != "Switch load.a on?" {
		t.Fatalf("question %q, %v", q, err)
	}
	if _, err := confirmed(t, call("cells.set", `{"id":"load.a","value":"toggle"}`), set); err == nil {
		t.Fatal("a bad value was asked")
	}
	args, _ := json.Marshal(map[string]string{"id": strings.Repeat("x", 3000), "value": "on"})
	if _, err := confirmed(t, call("cells.set", string(args)), set); !errors.Is(err, builtin.ErrTooLongToShow) {
		t.Fatalf("a huge id: %v", err)
	}
}
