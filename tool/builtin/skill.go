package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/igorrius/ganglion/internal/buckets"
	"github.com/igorrius/ganglion/internal/redact"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/skills"
	"github.com/igorrius/ganglion/tool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// SkillReadTool is the name of the tool that reads a skill; the agent
// routes the rest of the turn by the skill it read (R-RTR-4).
const SkillReadTool = "skill.read"

// NewSkillTools gives the agent skill.read and one tool per script skill
// (R-RTR-5, ADR-0042), all for admins only.
func NewSkillTools(set skills.Set, run *skills.Runner, begun ScriptRuns) []tool.Handler {
	m := otel.Meter("ganglion/skills")
	runs, _ := m.Int64Counter("ganglion.skill.runs", metric.WithDescription("Skill script runs, by skill and result."))
	dur, _ := m.Float64Histogram("ganglion.skill.run.duration", metric.WithUnit("s"), buckets.Seconds,
		metric.WithDescription("How long a skill script ran, by skill."))
	out := []tool.Handler{&skillRead{set: set}}
	for _, name := range set.Names() {
		if sk, _ := set.Get(name); sk.Script != nil {
			out = append(out, &skillScript{sk: sk, run: run, begun: begun, runs: runs, dur: dur})
		}
	}
	return out
}

type skillRead struct{ set skills.Set }

func (r *skillRead) Spec() tool.Spec {
	return tool.Spec{Name: SkillReadTool,
		Description: "Read a skill from the list in your instructions before you follow it: its steps, and the tool of its script if it has one.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"name":{"type":"string","description":"the skill's name"}},` +
			`"required":["name"],"additionalProperties":false}`),
		Class: tool.ClassRead, Policy: tool.PolicyAuto, AdminOnly: true, LocalSafe: true}
}

func (r *skillRead) Run(_ context.Context, c tool.Call) (tool.Result, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return tool.Result{}, errors.New(`skill.read: arguments: want {"name": "<skill>"}`)
	}
	sk, ok := r.set.Get(a.Name)
	if !ok {
		return tool.Result{}, fmt.Errorf("skill.read: no skill named %q; the skills are %s", a.Name, strings.Join(r.set.Names(), ", "))
	}
	body := sk.Body
	if sk.Script != nil {
		body += fmt.Sprintf("\n\nIts script is the tool %s (effects: %s); pass what it needs as input.", sk.ToolName(), sk.Script.Effects)
	}
	return tool.Result{Text: body}, nil
}

// ScriptRuns records that a change script is about to run for a call's key
// (skills.Runs). Begin is false when that key has begun before.
type ScriptRuns interface {
	Begin(ctx context.Context, key, skill string) (bool, error)
}

type skillScript struct {
	sk    skills.Skill
	run   *skills.Runner
	begun ScriptRuns
	runs  metric.Int64Counter
	dur   metric.Float64Histogram
}

func (s *skillScript) Spec() tool.Spec {
	class, policy := tool.ClassExternal, tool.PolicyAuto
	if s.sk.Script.Effects == skills.EffectsChange {
		class, policy = tool.ClassSideEffect, tool.PolicyConfirm
	}
	readOnly := s.sk.Script.Effects != skills.EffectsChange
	return tool.Spec{Name: s.sk.ToolName(),
		Description: s.sk.Description + fmt.Sprintf(" (the script of skill %s; it reads input on its stdin; effects: %s)", s.sk.Name, s.sk.Script.Effects),
		InputSchema: json.RawMessage(`{"type":"object","properties":{"input":{"type":"string","description":"text the script reads on its stdin"}},` +
			`"additionalProperties":false}`),
		Class: class, Policy: policy, AdminOnly: true,
		// Only the local-only skill's own script: its owner asked for this, and
		// another skill's script may reach the network (ADR-0049).
		LocalSafe:       s.sk.Model.Privacy == skills.PrivacyLocalOnly,
		AskTaintedInput: readOnly,
	}
}

func (s *skillScript) input(c tool.Call) (string, error) {
	var a struct {
		Input string `json:"input"`
	}
	if err := json.Unmarshal(c.Args, &a); err != nil {
		return "", fmt.Errorf(`%s: arguments: want {"input": "…"}`, s.sk.ToolName())
	}
	return a.Input, nil
}

// Question is a script's confirmation: the skill, what it does,
// and the whole input it gets.
func (s *skillScript) Question(_ context.Context, c tool.Call) (string, error) {
	in, err := s.input(c)
	if err != nil {
		return "", err
	}
	return whole(s.sk.ToolName(), fmt.Sprintf("Run the script of skill %s?\n%s\nInput:\n%s", s.sk.Name, oneLine(s.sk.Description), quote(in)))
}

// quote prefixes every line of s with "> ", so an input's lines cannot
// read as the question's own.
func quote(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = "> " + l
	}
	return strings.Join(lines, "\n")
}

// errHead caps a failed script's stderr in its error.
const errHead = 500

// Run runs the script. Its output is outside content whatever its class:
// it may print what it read from anywhere. A change script runs at most once
// per call key (R-DLV-6).
func (s *skillScript) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	in, err := s.input(c)
	if err != nil {
		return tool.Result{}, err
	}
	if s.sk.Script.Effects == skills.EffectsChange {
		first, err := s.begun.Begin(ctx, c.Key(), s.sk.Name)
		if err != nil {
			return tool.Result{}, fmt.Errorf("%s: %w", s.sk.ToolName(), err)
		}
		if !first {
			s.runs.Add(ctx, 1, metric.WithAttributes(attribute.String("skill", s.sk.Name), attribute.String("result", "cut_short")))
			return tool.Result{}, fmt.Errorf("%s: this call started once and a restart or a failure cut its turn short; it may have done its work, so it is not run again. Tell the owner the outcome is unknown; they can ask for it again", s.sk.ToolName())
		}
	}
	started := time.Now()
	out, err := s.run.Run(ctx, s.sk, []byte(in))
	s.dur.Record(ctx, time.Since(started).Seconds(), metric.WithAttributes(attribute.String("skill", s.sk.Name)))
	result := "ok"
	defer func() {
		s.runs.Add(ctx, 1, metric.WithAttributes(attribute.String("skill", s.sk.Name), attribute.String("result", result)))
	}()
	if err != nil {
		result = "error"
		return tool.Result{}, fmt.Errorf("%s: %w", s.sk.ToolName(), err)
	}
	if out.Exit != 0 {
		// stderr is the script's own text, maybe a page it read: outside
		// content, never the node's words in an error.
		result = "exit"
		return tool.Result{Text: fmt.Sprintf("the script failed (exit %d): %s", out.Exit, text.Truncate(redact.String(out.Stderr), errHead)),
			External: true}, nil
	}
	res := out.Stdout
	if out.Truncated {
		res += "\n[output cut]"
	}
	return tool.Result{Text: res, External: true}, nil
}
