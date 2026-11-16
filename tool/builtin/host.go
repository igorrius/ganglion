package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/igorrius/ganglion/host"
	"github.com/igorrius/ganglion/hostcmd"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
)

// HostStatsTool is the name of host.stats.
const HostStatsTool = "host.stats"

// StatsReader reads the machine's and the node's numbers (host.Stats).
type StatsReader interface {
	Read(ctx context.Context) host.Report
}

// HostStats is host.stats (R-CMD-5): memory, load, uptime, disk and the
// node's own files, read in Go with no subprocess, so it works where
// host.exec is off. The numbers and the node's own paths are not outside
// content, so it does not taint, and it sends nothing off the node but its
// answer, like metrics.show.
type HostStats struct{ s StatsReader }

// NewHostStats returns host.stats over s.
func NewHostStats(s StatsReader) *HostStats { return &HostStats{s: s} }

// Spec implements tool.Handler.
func (*HostStats) Spec() tool.Spec {
	return tool.Spec{Name: HostStatsTool,
		Description: "This machine's memory, load and uptime, free disk space, the node's own memory, and the sizes of the node's own files. Needs no command.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Class:       tool.ClassRead, Policy: tool.PolicyAuto, AdminOnly: true, LocalSafe: true}
}

// Run implements tool.Handler.
func (h *HostStats) Run(ctx context.Context, _ tool.Call) (tool.Result, error) {
	return tool.Result{Text: host.Render(h.s.Read(ctx))}, nil
}

// HostExecTool is the name of host.exec.
const HostExecTool = "host.exec"

// Commander runs host commands (host.Runner).
type Commander interface {
	Exec(ctx context.Context, q host.Request) (host.Answer, error)
}

// Ignorer tells whether git ignores a file a command names (host.Ignored).
type Ignorer interface {
	Ignored(ctx context.Context, dir string, argv []string) (repo string, ok bool)
}

// Gate says whether commands may run now: the sandbox is on and the kill
// switch is not off (R-CMD-1, R-CMD-3). It fails closed.
type Gate interface {
	Open() bool
}

// backgroundText is what the model reads of a command still running.
const backgroundText = "still running; it goes on in the background, and its result comes back to this conversation " +
	"as a note when it ends. Tell the person it runs, never that it is done"

// errUnapproved refuses a command that needs a press and has none: the
// policy stops it first, so reaching here means a bug, not a model's choice.
var errUnapproved = errors.New("host.exec: this command needs the owner's press")

// errCommandsOff refuses a call while commands are off.
var errCommandsOff = errors.New("host.exec: commands are off on this node")

// HostExec is host.exec (R-CMD-1): a program and its arguments, no shell,
// in the sandbox, as the node's user; output capped, redacted and wrapped as
// outside content. The policy decides by the class table; the tool checks
// again, so no path around the policy runs what it would not.
type HostExec struct {
	run   Commander
	ig    Ignorer
	gate  Gate
	home  string
	ticks PlanTicks
	log   *slog.Logger
}

// PlanTicks ticks the step of the conversation's plan that lists a command
// that ended (plan.Store).
type PlanTicks interface {
	Ran(ctx context.Context, conv string, cmd tool.Command, ok bool) error
}

// NoPlanTicks is PlanTicks for a node with no plans: it ticks nothing.
type NoPlanTicks struct{}

// Ran implements PlanTicks.
func (NoPlanTicks) Ran(context.Context, string, tool.Command, bool) error { return nil }

// HostExecOption configures host.exec.
type HostExecOption func(*HostExec)

// WithPlanTicks ticks a plan's step when its command ends here (#38); a
// failed tick is logged to log, and the command's answer stands.
func WithPlanTicks(p PlanTicks, log *slog.Logger) HostExecOption {
	return func(h *HostExec) { h.ticks, h.log = p, log }
}

// NewHostExec returns host.exec. home is the folder a call without dir runs in.
func NewHostExec(run Commander, ig Ignorer, gate Gate, home string, opts ...HostExecOption) *HostExec {
	h := &HostExec{run: run, ig: ig, gate: gate, home: home, ticks: NoPlanTicks{}, log: slog.New(slog.DiscardHandler)}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Spec implements tool.Handler.
func (h *HostExec) Spec() tool.Spec {
	return tool.Spec{Name: HostExecTool,
		Description: "Run a program on this machine as the node's user. argv is the program and its arguments as a list: " +
			"there is no shell, so pipes, ;, globs, $(…) and ~ mean nothing; give full paths (home is " + h.home + "). " +
			"Commands that only read sizes, names and state run at once. One that shows what is inside files, changes " +
			"anything or runs a script waits for the person's press, which shows a fixed line on what it does and your " +
			"reason; a script must be written out (bash -c, python3 -c). For work with several steps, show a plan whose " +
			"steps carry their commands: one press approves them all. A command an approved plan or a task's grant lists runs " +
			"without a press only with its argv and dir repeated exactly as approved. sudo, docker run and anything that starts programs " +
			"outside the command never run. Output is capped and is outside data. A command still running after 20 s goes " +
			"on in the background, and its result comes back later in this conversation.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"argv":{"type":"array","items":{"type":"string"},"minItems":1,"maxItems":64,"description":"the program and its arguments"},` +
			`"dir":{"type":"string","description":"the absolute folder to run in; home by default"},` +
			`"timeout":{"type":"integer","minimum":1,"maximum":900,"description":"seconds before it is stopped; 900 at most"},` +
			`"reason":{"type":"string","description":"one line: why this command, shown to the person as your words"}},` +
			`"required":["argv","reason"],"additionalProperties":false}`),
		Class: tool.ClassSideEffect, Policy: tool.PolicyConfirm, AdminOnly: true}
}

// Offered implements tool.Gated: only while commands may run.
func (h *HostExec) Offered() bool { return h.gate.Open() }

// Run implements tool.Handler.
func (h *HostExec) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	x, err := hostcmd.ParseExec(c.Args)
	if err != nil {
		return tool.Result{}, err
	}
	if !h.gate.Open() {
		return tool.Result{}, errCommandsOff
	}
	if err := h.allowed(c, x.Command); err != nil {
		return tool.Result{}, err
	}
	a, err := h.run.Exec(ctx, host.Request{Key: c.Key(), Conv: c.Conv, Transport: c.Transport, Principal: c.Principal,
		Depth: c.Depth, Argv: x.Command.Argv, Dir: h.dir(x.Command), Timeout: time.Duration(x.Timeout) * time.Second})
	if err != nil {
		return tool.Result{}, err
	}
	if !a.Done {
		return tool.Result{Text: backgroundText, External: true, Job: a.Job.Key}, nil
	}
	if err := h.ticks.Ran(ctx, c.Conv, x.Command, a.Job.OK()); err != nil {
		h.log.Warn("host.exec: the plan's step was not ticked", "conv", c.Conv, "err", err)
	}
	return tool.Result{Text: a.Job.Text, External: true}, nil
}

// allowed checks again what the policy decided: forbidden never; a command
// that needs a press only pressed, planned or granted.
func (h *HostExec) allowed(c tool.Call, cmd tool.Command) error {
	return h.allowedAs(hostcmd.Classify(cmd.Argv).Class, c, cmd)
}

// allowedAs is allowed for a command of class cl. Only a read command runs
// without a press: any other class, one this check does not know included,
// fails closed and needs one.
func (h *HostExec) allowedAs(cl hostcmd.Class, c tool.Call, cmd tool.Command) error {
	switch {
	case cl == hostcmd.Forbidden:
		return fmt.Errorf("host.exec: %s never runs: %w", hostcmd.Show(cmd.Argv), tool.ErrForbidden)
	case cl == hostcmd.Read || c.Approved:
		return nil
	case c.Planned != nil && c.Planned(cmd):
		return nil
	case c.Grant != nil:
		return h.granted(c, cmd)
	default:
		return errUnapproved
	}
}

// granted checks a task's command that needs a press against its grant: the
// same check as the policy's, "" read as home on both sides, so a run that
// reached the tool past the policy is still refused (R-POL-8).
func (h *HostExec) granted(c tool.Call, cmd tool.Command) error {
	if slices.ContainsFunc(c.Grant.Commands, func(g tool.Command) bool { return g.SameAs(cmd, h.home) }) {
		return nil
	}
	return fmt.Errorf("host.exec: %s is not in this task's grant: %w", hostcmd.Show(cmd.Argv), tool.ErrOutOfGrant)
}

func (h *HostExec) dir(cmd tool.Command) string {
	if cmd.Dir == "" {
		return h.home
	}
	return cmd.Dir
}

// Question implements tool.Questioner: the exact command, the fixed line of
// its class, a warning for a file git ignores, and apart from them, the
// agent's reason, marked as its words (R-CMD-6). A question that would hide
// a secret or be cut is refused, so the owner sees all they approve.
func (h *HostExec) Question(ctx context.Context, c tool.Call) (string, error) {
	x, err := hostcmd.ParseExec(c.Args)
	if err != nil {
		return "", err
	}
	return whole(HostExecTool, c.Speak.Text(texts.AskHostExec, h.questionVars(ctx, c.Speak, x.Command, x.Reason)))
}

// questionVars are a command's lines in a question; plan.show uses them too.
func (h *HostExec) questionVars(ctx context.Context, sp texts.Speaker, cmd tool.Command, reason string) texts.Vars {
	e := hostcmd.Classify(cmd.Argv)
	warning := ""
	if e.Class == hostcmd.Content {
		if repo, ok := h.ig.Ignored(ctx, h.dir(cmd), cmd.Argv); ok {
			warning = "\n" + sp.Text(texts.CmdGitIgnored, texts.Vars{"repo": shown(repo)})
		}
	}
	if reason == "" {
		reason = "—"
	}
	where := sp.Text(texts.CmdIn, texts.Vars{"command": shown(cmd.Argv...), "dir": shown(h.dir(cmd))})
	return texts.Vars{"command": where, "line": sp.Text(e.Line, nil), "warning": warning,
		"reason": sp.Text(texts.CmdReason, texts.Vars{"reason": text.CodeSlashWords(reason)})}
}

// shown is argv as a person reads it in a question: written by hostcmd.Show,
// so a character that hides text is escaped and a space cannot split an
// argument, in one code span that a backtick cannot end early.
func shown(argv ...string) string { return text.CodeSpan(hostcmd.Show(argv)) }
