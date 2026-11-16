package builtin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/igorrius/ganglion/internal/redact"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/modtrial"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
)

// window is what the install question shows the owner: the build, its
// rights, what they add to the active version's, and what the agent reads of
// each tool. The press installs only when the plan at the press shows the
// same window (Review Focus 2 of M4b).
type window struct {
	Module   string            `json:"module"`
	SHA256   string            `json:"sha256"`
	Version  int               `json:"version"`
	Size     int               `json:"size"`
	Rights   modstore.Rights   `json:"rights"`
	Widening modstore.Rights   `json:"widening"`
	About    map[string]string `json:"about"` // the node's tool name → the description the agent reads
	// Trial is the trial run's lines as the owner reads them, redacted and in
	// the asker's language: a language change between the question and the
	// press makes the press refuse and the owner is asked again (safe by
	// design). Kept so the press shows the same.
	Trial []string `json:"trial"`
}

// windowOf is the window of a build whose plan has no refusal; trial is the
// section of its trial run (trialLines).
func windowOf(b modstore.Build, p modstore.Plan, m runner.Manifest, trial []string) window {
	short := modstore.ShortName(b.Module)
	about := map[string]string{}
	for _, t := range m.Triggers {
		if t.Tool != "" && t.Description() != "" {
			about[short+"."+t.Tool] = t.Description()
		}
	}
	return window{Module: b.Module, SHA256: p.SHA256, Version: p.Version, Size: p.Size, Rights: p.Rights, Widening: p.Widening, About: about, Trial: trial}
}

// recordPath is where the window a call asked with is kept until its press.
func (m Modules) recordPath(c tool.Call, sha string) string {
	return m.buildPath(sha, ".window-"+keyHash(c)+".json")
}

// recordWindow keeps the window call c asks with, for its press. It is
// written once: a rerun of the turn asks again, but the question the owner
// sees and presses on is the first one (the pending call and the outbox keep
// it), so a second window never replaces the first.
func (m Modules) recordWindow(c tool.Call, w window) error {
	b, err := json.Marshal(w)
	if err != nil {
		return err
	}
	return writeOnce(m.recordPath(c, w.SHA256), b)
}

// errWindowChanged refuses a press whose plan no longer shows what the owner
// saw.
var errWindowChanged = errors.New("what this build may do changed since the owner saw the question " +
	"(a secret, a setting, a cell's owner or the active version); call module.install again so the owner sees it as it is now")

// sameWindow checks that w is the window call c asked with.
func (m Modules) sameWindow(c tool.Call, w window) error {
	shown, err := os.ReadFile(m.recordPath(c, w.SHA256))
	if err != nil {
		return errWindowChanged
	}
	now, err := json.Marshal(w)
	if err != nil {
		return err
	}
	if !bytes.Equal(shown, now) {
		return errWindowChanged
	}
	return nil
}

// Placeholders of the window's texts.
const (
	varItems = "items"
	varName  = "name"
)

// noneShown stands for an empty list in the window.
const noneShown = "—"

// digestShown is how many hex digits of a digest the window shows.
const digestShown = 6

// windowText is the install question: a line of what widened first, when
// anything did, then the build and every right (R-RUN-9). Every name the
// manifest wrote is shown as it is when it is plain, else escaped in a code
// span, so it cannot draw a line or hide text.
func windowText(sp texts.Speaker, w window) string {
	var lines []string
	if n := wideningLine(sp, w); n != "" {
		lines = append(lines, n)
	}
	r := w.Rights
	lines = append(lines,
		sp.Text(texts.AskModuleInstall, texts.Vars{"module": modstore.LocalPrefix + item(modstore.ShortName(w.Module)), "version": strconv.Itoa(w.Version),
			"size": status.Bytes(int64(w.Size)), "digest": w.SHA256[:digestShown] + "…"}),
		sp.Text(texts.AskModuleNetwork, texts.Vars{varItems: listed(r.Hosts)}),
		sp.Text(texts.AskModuleSecrets, texts.Vars{varItems: listed(r.Secrets)}),
		sp.Text(texts.AskModuleCreates, texts.Vars{varItems: listed(r.CellsCreated)}),
		sp.Text(texts.AskModuleTakes, texts.Vars{varItems: listed(r.CellsTaken)}),
		sp.Text(texts.AskModuleReads, texts.Vars{varItems: listed(r.CellsRead)}))
	lines = append(lines, reflexLines(sp, r)...)
	lines = append(lines, sp.Text(texts.AskModuleRuns, texts.Vars{varItems: runs(sp, w.Module, r)}))
	for _, t := range r.Tools {
		name := modstore.ShortName(w.Module) + "." + t
		if about, ok := w.About[name]; ok {
			lines = append(lines, sp.Text(texts.AskModuleAbout, texts.Vars{"tool": item(name), "about": shown(about)}))
		}
	}
	lines = append(lines, sp.Text(texts.AskModuleLimits, texts.Vars{"memory": memoryShown(r.Memory), "timeout": timeoutShown(r.Timeout)}))
	lines = append(lines, w.Trial...)
	return strings.Join(lines, "\n")
}

// trialRunes bounds the trial section of the window, in runes of its joined
// lines; the rest is counted, not shown.
const trialRunes = 1200

// messageRunes bounds one message or value in the trial section.
const messageRunes = 200

// trialLines is the trial run's section of the install window. A module's
// message is shown as chat shows it (Markdown escaped, one line); a value is
// compact JSON in a code span. These texts reach the owner's question only,
// never a tool result. ok is false when no trial is kept.
func trialLines(sp texts.Speaker, r modtrial.Report, ok bool) []string {
	if !ok {
		return []string{sp.Text(texts.AskModuleTrialNone, nil)}
	}
	passed := 0
	for _, e := range r.Examples {
		if e.Passed {
			passed++
		}
	}
	lines := []string{sp.Text(texts.AskModuleTrial, texts.Vars{"passed": strconv.Itoa(passed), "total": strconv.Itoa(len(r.Examples))})}
	for _, e := range r.Examples {
		lines = append(lines, sp.Text(texts.AskModuleTrialExample, texts.Vars{varName: item(e.Name)}))
		out := false
		for _, s := range e.Steps {
			l := stepLines(sp, s)
			out = out || len(l) > 0
			lines = append(lines, l...)
		}
		if !out {
			lines = append(lines, sp.Text(texts.AskModuleTrialNothing, nil))
		}
	}
	if r.Now != nil {
		lines = append(lines, sp.Text(texts.AskModuleTrialNow, nil))
		l := stepLines(sp, *r.Now)
		if len(l) == 0 {
			l = []string{sp.Text(texts.AskModuleTrialNothing, nil)}
		}
		lines = append(lines, l...)
	}
	// Redacted as one text, after escaping and before the cap: a module's
	// harmless words ("Monkey: 5") would otherwise make the question refused
	// for a secret, a pair can match across a line break, and the marker
	// changes lengths.
	return capped(sp, strings.Split(redact.String(strings.Join(lines, "\n")), "\n"), trialRunes)
}

// stepLines are the messages and the reported cells of one step.
func stepLines(sp texts.Speaker, s modtrial.StepResult) []string {
	var lines []string
	for _, m := range s.Messages {
		flat := text.Truncate(strings.Join(strings.Fields(m), " "), messageRunes)
		lines = append(lines, sp.Text(texts.AskModuleTrialMessage, texts.Vars{"text": texts.Escape(flat)}))
	}
	for _, c := range s.Reports {
		lines = append(lines, sp.Text(texts.AskModuleTrialCell, texts.Vars{"cell": item(c.Cell), "value": shown(compact(c.Value))}))
	}
	return lines
}

// compact is a reported value as one short line of JSON.
func compact(v json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, v); err != nil {
		return text.Truncate(strings.Join(strings.Fields(string(v)), " "), messageRunes)
	}
	return text.Truncate(b.String(), messageRunes)
}

// capped is lines as far as their joined text stays within limit runes, then
// a line with the number of lines left out.
func capped(sp texts.Speaker, lines []string, limit int) []string {
	used := 0
	for i, l := range lines {
		used += utf8.RuneCountInString(l)
		if i > 0 {
			used++ // the line break
		}
		if used > limit {
			return append(lines[:i:i], sp.Text(texts.AskModuleTrialMore, texts.Vars{"n": strconv.Itoa(len(lines) - i)}))
		}
	}
	return lines
}

// reflexLines are the lines of what a module reacts to and how much it may
// write to the owner; none for a module that does neither, so the window of
// an ordinary module stays as it was.
func reflexLines(sp texts.Speaker, r modstore.Rights) []string {
	var lines []string
	if len(r.Reacts) > 0 {
		lines = append(lines, sp.Text(texts.AskModuleReacts, texts.Vars{varItems: listed(r.Reacts)}))
	}
	if r.NotifyPerDay > 0 {
		lines = append(lines, sp.Text(texts.AskModuleNotify, texts.Vars{"n": strconv.Itoa(r.NotifyPerDay)}))
	}
	return lines
}

// wideningLine is "New: …" with each right the active version did not have;
// "" when nothing widened.
func wideningLine(sp texts.Speaker, w window) string {
	n := w.Widening
	var parts []string
	add := func(k texts.Key, items []string) {
		if len(items) > 0 {
			parts = append(parts, sp.Text(k, texts.Vars{varItems: listed(items)}))
		}
	}
	add(texts.AskModuleNewNetwork, n.Hosts)
	add(texts.AskModuleNewSecrets, n.Secrets)
	add(texts.AskModuleNewCells, append(append([]string(nil), n.CellsCreated...), n.CellsTaken...))
	add(texts.AskModuleNewReads, n.CellsRead)
	add(texts.AskModuleNewReacts, n.Reacts)
	if n.NotifyPerDay > 0 {
		parts = append(parts, sp.Text(texts.AskModuleNewNotify, texts.Vars{"n": strconv.Itoa(n.NotifyPerDay)}))
	}
	if len(n.Crons)+len(n.Tools) > 0 {
		parts = append(parts, sp.Text(texts.AskModuleNewRuns, texts.Vars{varItems: runs(sp, w.Module, modstore.Rights{Crons: n.Crons, Tools: n.Tools})}))
	}
	if n.Memory > 0 {
		parts = append(parts, sp.Text(texts.AskModuleNewMemory, texts.Vars{"memory": memoryShown(n.Memory)}))
	}
	if n.Timeout > 0 {
		parts = append(parts, sp.Text(texts.AskModuleNewTimeout, texts.Vars{"timeout": timeoutShown(n.Timeout)}))
	}
	if len(parts) == 0 {
		return ""
	}
	return sp.Text(texts.AskModuleNew, texts.Vars{varItems: strings.Join(parts, "; ")})
}

// runs is the crons and the tools of r, as the node names the tools.
func runs(sp texts.Speaker, module string, r modstore.Rights) string {
	var out []string
	for _, c := range r.Crons {
		out = append(out, cronShown(c))
	}
	for _, t := range r.Tools {
		out = append(out, sp.Text(texts.AskModuleTool, texts.Vars{varName: item(modstore.ShortName(module) + "." + t)}))
	}
	if len(out) == 0 {
		return noneShown
	}
	return strings.Join(out, "; ")
}

// plainItem is a name shown as it is: letters, digits, dots and hyphens,
// none of which Markdown formats with. Anything else is shown in a code span,
// so "_x_" cannot render as emphasis and hide what installs.
var plainItem = regexp.MustCompile(`^[A-Za-z0-9.-]{1,253}$`)

// plainCron is a cron line of digits and the cron marks alone; it is still
// shown in a code span, since Markdown formats with "*".
var plainCron = regexp.MustCompile(`^[0-9*/,-]{1,20}( [0-9*/,-]{1,20}){4,5}$`)

// listed is items joined, each shown by item; noneShown for none.
func listed(items []string) string {
	if len(items) == 0 {
		return noneShown
	}
	out := make([]string, len(items))
	for i, s := range items {
		out[i] = item(s)
	}
	return strings.Join(out, ", ")
}

// item is s as it is when it is plain, else in a code span, escaped as
// host.exec's question shows an argument.
func item(s string) string {
	if plainItem.MatchString(s) {
		return s
	}
	return shown(s)
}

// cronShown is a cron line in a code span: as it is when it is plain, else
// escaped as host.exec's question shows an argument.
func cronShown(c string) string {
	if plainCron.MatchString(c) {
		return text.CodeSpan(c)
	}
	return shown(c)
}

// memoryShown is a memory limit in MiB when it is whole ones.
func memoryShown(n uint64) string {
	const mib = 1 << 20
	if n%mib == 0 {
		return fmt.Sprintf("%d MiB", n/mib)
	}
	return status.Bytes(int64(n)) //nolint:gosec // a manifest's memory is at most 1 GiB
}

// timeoutShown is a time limit in seconds when it is whole ones.
func timeoutShown(d time.Duration) string {
	if d%time.Second == 0 {
		return fmt.Sprintf("%d s", d/time.Second)
	}
	return d.String()
}
