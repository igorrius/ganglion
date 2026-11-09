package hostcmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/igorrius/ganglion/tool"
)

// Limits of an argv.
const (
	maxArgs     = 64
	maxArgRunes = 4000
	// MaxTimeout is the longest a command may be given, in seconds.
	MaxTimeout = 900
)

// ErrArgs is an argv, a folder or a step the node will not take.
var ErrArgs = errors.New("hostcmd: bad command")

// Exec is a host.exec call's arguments.
type Exec struct {
	Command tool.Command
	Timeout int    // seconds; 0 is the node's limit
	Reason  string // the model's one line, shown apart from the fixed line
}

// ParseExec reads host.exec's arguments strictly.
func ParseExec(raw json.RawMessage) (Exec, error) {
	var a struct {
		Argv    []string `json:"argv"`
		Dir     string   `json:"dir"`
		Timeout int      `json:"timeout"`
		Reason  string   `json:"reason"`
	}
	if err := strict(raw, &a); err != nil {
		return Exec{}, err
	}
	cmd := tool.Command{Argv: a.Argv, Dir: a.Dir}
	if err := check(cmd); err != nil {
		return Exec{}, err
	}
	if a.Timeout < 0 || a.Timeout > MaxTimeout {
		return Exec{}, fmt.Errorf("%w: timeout is 1 to %d seconds", ErrArgs, MaxTimeout)
	}
	return Exec{Command: cmd, Timeout: a.Timeout, Reason: oneLine(a.Reason)}, nil
}

// PlanStep is a step of plan.show: its words and, for a command, the command
// and the model's reason.
type PlanStep struct {
	Text    string
	Command *tool.Command
	Reason  string
}

// ParsePlan reads plan.show's steps: each a string, or an object with text
// and an optional argv, dir and reason.
func ParsePlan(raw json.RawMessage) ([]PlanStep, error) {
	var a struct {
		Steps []json.RawMessage `json:"steps"`
	}
	if err := strict(raw, &a); err != nil {
		return nil, err
	}
	out := make([]PlanStep, 0, len(a.Steps))
	for i, s := range a.Steps {
		st, err := planStep(s)
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", i+1, err)
		}
		out = append(out, st)
	}
	return out, nil
}

func planStep(raw json.RawMessage) (PlanStep, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return PlanStep{Text: text}, nil
	}
	var o struct {
		Text   string   `json:"text"`
		Argv   []string `json:"argv"`
		Dir    string   `json:"dir"`
		Reason string   `json:"reason"`
	}
	if err := strict(raw, &o); err != nil {
		return PlanStep{}, err
	}
	st := PlanStep{Text: o.Text, Reason: oneLine(o.Reason)}
	if len(o.Argv) > 0 || o.Dir != "" {
		cmd := tool.Command{Argv: o.Argv, Dir: o.Dir}
		if err := check(cmd); err != nil {
			return PlanStep{}, err
		}
		st.Command = &cmd
	}
	return st, nil
}

// check refuses an argv the node would not run as given: empty, too long,
// with a NUL, or a folder that is not absolute.
func check(c tool.Command) error {
	if len(c.Argv) == 0 || len(c.Argv) > maxArgs || c.Argv[0] == "" {
		return fmt.Errorf("%w: argv has 1 to %d arguments and a program", ErrArgs, maxArgs)
	}
	for _, a := range c.Argv {
		if strings.ContainsRune(a, 0) || len([]rune(a)) > maxArgRunes {
			return fmt.Errorf("%w: an argument holds a NUL or is over %d characters", ErrArgs, maxArgRunes)
		}
	}
	if c.Dir != "" && (!filepath.IsAbs(c.Dir) || filepath.Clean(c.Dir) != c.Dir) {
		return fmt.Errorf("%w: dir must be an absolute, clean path", ErrArgs)
	}
	return nil
}

func strict(raw json.RawMessage, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", ErrArgs, err)
	}
	return nil
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
