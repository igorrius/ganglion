package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/transcript"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/transport"
	"github.com/igorrius/ganglion/turns"
)

// command answers a chat command without a model; arg is the text after
// the command word.
type command func(ctx context.Context, t *turn, arg string) error

// commandEntry is one row of the command table: the command word, what
// answers it, and its description per language for the transport's
// command hints; an alias has none and is not listed (rulings R43, R45).
type commandEntry struct {
	word string
	run  command
	hint map[transport.Lang]string
}

// commandPrefix starts a user-role note of a command that showed the
// owner something the model should know of: "[command] ...".
const commandPrefix = "[command] "

// listTool is the tool /tasks runs: the list the model gets, with the
// same messages and buttons (ruling R47).
const listTool = "schedule.list"

// commandTable is the agent's commands, in the order they are listed: the
// one source of what the agent answers and of the hints people see.
func (a *Agent) commandTable() []commandEntry {
	return []commandEntry{
		{"/new", a.newSession, map[transport.Lang]string{
			transport.LangDefault: "Start a new conversation", transport.LangRussian: "Начать новый разговор",
			transport.LangUkrainian: "Почати нову розмову"}},
		{"/reset", a.newSession, nil},
		{"/sessions", a.listSessions, map[transport.Lang]string{
			transport.LangDefault: "List earlier conversations", transport.LangRussian: "Показать прошлые разговоры",
			transport.LangUkrainian: "Показати попередні розмови"}},
		{"/resume", a.resumeSession, map[transport.Lang]string{
			transport.LangDefault:   "Go back to an earlier conversation: /resume <number>",
			transport.LangRussian:   "Вернуться к прошлому разговору: /resume <номер>",
			transport.LangUkrainian: "Повернутися до попередньої розмови: /resume <номер>"}},
		{"/tasks", a.listTasks, map[transport.Lang]string{
			transport.LangDefault: "Reminders and scheduled tasks", transport.LangRussian: "Напоминания и задачи по расписанию",
			transport.LangUkrainian: "Нагадування та завдання за розкладом"}},
		{"/last", a.lastTurn, map[transport.Lang]string{
			transport.LangDefault: "What the last turn did", transport.LangRussian: "Что сделал последний ход",
			transport.LangUkrainian: "Що зробив останній хід"}},
		{"/status", a.showStatus, map[transport.Lang]string{
			transport.LangDefault: "How the node is doing", transport.LangRussian: "Как дела у ноды",
			transport.LangUkrainian: "Як справи у вузла"}},
		{"/restart", a.restart, map[transport.Lang]string{
			transport.LangDefault:   "Restart the node: settings apply",
			transport.LangRussian:   "Перезапустить ноду: настройки применятся",
			transport.LangUkrainian: "Перезапустити вузол: налаштування застосуються"}},
		{"/metrics", a.showMetrics, map[transport.Lang]string{
			transport.LangDefault:   "Agent metrics as a picture: /metrics [all | ganglion | runtime | process | filter]",
			transport.LangRussian:   "Метрики агента картинкой: /metrics [all | ganglion | runtime | process | фильтр]",
			transport.LangUkrainian: "Метрики агента картинкою: /metrics [all | ganglion | runtime | process | фільтр]"}},
		{"/modules", a.modules, map[transport.Lang]string{
			transport.LangDefault:   "Modules: /modules, then /modules <name>, config, off, on, remove or rollback",
			transport.LangRussian:   "Модули: /modules, затем /modules <имя>, config, off, on, remove или rollback",
			transport.LangUkrainian: "Модулі: /modules, потім /modules <назва>, config, off, on, remove або rollback"}},
		{"/cells", a.listCells, map[transport.Lang]string{
			transport.LangDefault: "Cells: sensors and switches with their values", transport.LangRussian: "Ячейки: датчики и выключатели со значениями",
			transport.LangUkrainian: "Комірки: датчики та вимикачі зі значеннями"}},
		{"/commands", a.commandsSwitch, map[transport.Lang]string{
			transport.LangDefault:   "Host commands: /commands on or off",
			transport.LangRussian:   "Команды на компьютере: /commands on или off",
			transport.LangUkrainian: "Команди на комп'ютері: /commands on або off"}},
	}
}

// Commands are the commands a transport lists for people, from the
// command table: name without the slash and description per language.
func (a *Agent) Commands() []transport.Command {
	var out []transport.Command
	for _, c := range a.commandTable() {
		if c.hint != nil {
			out = append(out, transport.Command{Name: strings.TrimPrefix(c.word, "/"), Description: c.hint})
		}
	}
	return out
}

const (
	// tasksNote starts the history's note that /tasks showed the list
	// (ruling R47, in the form of ruling R42's notes).
	tasksNote     = commandPrefix + "the owner listed the reminders and tasks with /tasks: "
	sessionsShown = 5
)

// parseCommand returns the command word of text ("/new@bot" → "/new") and
// the rest, or "" when text is not a command.
func parseCommand(text string) (string, string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	word, rest, _ := strings.Cut(text, " ")
	word, _, _ = strings.Cut(word, "@")
	return strings.ToLower(word), strings.TrimSpace(rest)
}

// command runs the message's command from the table; other messages go
// on to the model, unknown commands included. A scheduled task's
// instruction and a forward are never commands: the sender did not write
// them as one.
func (a *Agent) command(ctx context.Context, t *turn) (bool, error) {
	if t.grant != nil {
		return false, nil
	}
	run, arg, ok := a.commandIn(t.msg)
	if !ok {
		return false, nil
	}
	t.record.Outcome = turns.OutcomeCommand
	return true, run(ctx, t, arg)
}

// commandIn finds the command a person's message runs: a forward is never
// one, as the sender did not write it as one.
func (a *Agent) commandIn(m event.Message) (run command, arg string, ok bool) {
	if m.Forwarded {
		return nil, "", false
	}
	name, arg := parseCommand(m.Text)
	run, ok = a.commands[name]
	return run, arg, ok
}

// newSession archives the session; the next real message opens a new one
// and sets its id and start.
func (a *Agent) newSession(ctx context.Context, t *turn, _ string) error {
	if err := a.archive(ctx, t.event.Conv, t.state, "new"); err != nil {
		return err
	}
	t.state = State{V: StateVersion}
	t.reply(a.say(ctx, t, texts.ReplyNewSession, nil))
	return nil
}

// listSessions shows the last archived sessions, newest first.
func (a *Agent) listSessions(ctx context.Context, t *turn, _ string) error {
	list, err := a.d.Archive.List(ctx, t.event.Conv, sessionsShown, t.state.Session)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		t.reply(a.say(ctx, t, texts.ReplyNoSessions, nil))
		return nil
	}
	lines := make([]string, len(list))
	for i, s := range list {
		lines[i] = a.say(ctx, t, texts.ReplySessionRow, texts.Vars{"n": strconv.Itoa(i + 1), "span": a.formatSpan(s),
			"turns": strconv.Itoa(s.Turns), "title": s.Title})
	}
	t.reply(strings.Join(lines, "\n"))
	return nil
}

// resumeSession brings back session number arg of /sessions, archiving
// the current one. Calls it was waiting for that are answered or expired
// since are dropped from its pending list, and commands that ended since
// from its commands: their end was told as late while it was archived
// (R-CMD-4).
func (a *Agent) resumeSession(ctx context.Context, t *turn, arg string) error {
	list, err := a.d.Archive.List(ctx, t.event.Conv, sessionsShown, t.state.Session)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 || n > len(list) {
		t.reply(a.say(ctx, t, texts.ReplyNoSuchResume, nil))
		return nil
	}
	chosen, err := a.d.Archive.Get(ctx, t.event.Conv, list[n-1].Session)
	if err != nil {
		return err
	}
	if t.state.Session != chosen.Session {
		if err := a.archive(ctx, t.event.Conv, t.state, "resume"); err != nil {
			return err
		}
	}
	if chosen.Pending, err = a.d.Pending.Waiting(ctx, chosen.Pending); err != nil {
		return err
	}
	if len(chosen.Commands) > 0 {
		if chosen.Commands, err = a.d.Commands.Running(ctx, chosen.Commands); err != nil {
			return err
		}
	}
	chosen.Last = t.now
	t.state = chosen
	t.reply(a.say(ctx, t, texts.ReplyResumed, texts.Vars{"title": list[n-1].Title}))
	return nil
}

func (a *Agent) archive(ctx context.Context, conv string, st State, by string) error {
	if len(st.Messages) == 0 {
		return nil
	}
	if err := a.d.Archive.Put(ctx, conv, st, by); err != nil {
		return err
	}
	a.observeEnded(ctx, by)
	return nil
}

// spanLayout has no month name, so it reads the same in every language.
const spanLayout = "02.01 15:04"

func (a *Agent) formatSpan(s SessionInfo) string {
	start, end := s.Started.In(a.cfg.Location), s.Ended.In(a.cfg.Location)
	if start.YearDay() == end.YearDay() && start.Year() == end.Year() {
		return start.Format(spanLayout) + "–" + end.Format("15:04")
	}
	return start.Format(spanLayout) + "–" + end.Format(spanLayout)
}

// listTasks shows the owner's reminders and tasks, one message each with
// its Details, Delete and Run now buttons, without a model turn: it runs
// schedule.list, the model's own tool, as the turn's first call through
// the same chain (tracing, the journal, the policy), so there is one code
// path (ruling R47). Only an admin gets it. The history notes what was
// shown, so the next turn knows.
func (a *Agent) listTasks(ctx context.Context, t *turn, _ string) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if _, ok := a.d.Tools.Lookup(listTool); !ok || !t.admin {
		t.reply(a.say(ctx, t, texts.ReplyNotAvailable, nil))
		return nil
	}
	shown := len(t.out)
	text, err := a.useTool(ctx, t, provider.ToolCall{Name: listTool, Args: json.RawMessage(`{}`)}, false)
	if err != nil {
		return fmt.Errorf("agent: /tasks: %w", err)
	}
	if len(t.out) == shown {
		t.reply(a.say(ctx, t, texts.ReplyNoTasks, nil))
	}
	t.note(tasksNote + transcript.Cap(text))
	return nil
}
