package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/schedule"
	"github.com/igorrius/ganglion/tool"
	"github.com/igorrius/ganglion/tool/pending"
)

// Names of the schedule tools and of the quick actions on a listed job.
// Delete and Run now are both: a tool the model calls, which the owner
// confirms, and a button the owner presses.
const (
	toolCreate    = "schedule.create"
	toolList      = "schedule.list"
	ActionDetails = "schedule.details"
	ActionDelete  = "schedule.delete"
	ActionRunNow  = "schedule.run_now"
)

// Limits of what schedule.create takes.
const (
	// maxTextRunes caps a reminder's text or a task's instruction, so the
	// confirmation shows all of it.
	maxTextRunes = 1000
	// listButtonsTTL is how long the buttons of a listed job work.
	listButtonsTTL = 24 * time.Hour
	timeLayout     = "2006-01-02 15:04"
)

// hostPattern is a bare lower-case host name with at least one dot: no
// scheme, path, port or wildcard.
var hostPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

// errInvalid marks a schedule.create call whose arguments cannot become
// a job; the model reads why.
var errInvalid = errors.New("schedule.create: invalid")

// Jobs is the job store (schedule.Store).
type Jobs interface {
	Create(ctx context.Context, j schedule.Job) error
	Get(ctx context.Context, id string) (schedule.Job, error)
	List(ctx context.Context, owner string) ([]schedule.Job, error)
	Delete(ctx context.Context, id, owner string) error
}

// Runner runs a task outside its schedule (schedule.Scheduler); the same
// token is the same run.
type Runner interface {
	RunKey(ctx context.Context, id, owner, token string) error
}

// SpecFunc returns the spec of a registered tool; false for an unknown
// one. A grant may name only registered tools.
type SpecFunc func(name string) (tool.Spec, bool)

// Buttons stores quick-action buttons and asks the owner (tool/pending.Store).
type Buttons interface {
	AddButtons(ctx context.Context, bs ...pending.Button) error
	Ask(ctx context.Context, c tool.Call, question string) (string, error)
	Outgoing(ctx context.Context, id string) (event.Outgoing, error)
}

// Scheduling is what the schedule tools and their quick actions share
// (design section 5.3).
type Scheduling struct {
	Jobs    Jobs
	Runner  Runner
	Specs   SpecFunc
	Buttons Buttons
	Convs   Convs // who may receive a task's replies
	Loc     *time.Location
	Now     func() time.Time
}

// createArgs are the arguments of schedule.create.
type createArgs struct {
	Kind  string     `json:"kind"`
	Cron  string     `json:"cron"`
	At    string     `json:"at"`
	Text  string     `json:"text"`
	Grant *grantArgs `json:"grant"`
}

type grantArgs struct {
	Tools   []string `json:"tools"`
	Hosts   []string `json:"hosts"`
	ReplyTo string   `json:"reply_to"`
}

// idArgs are the arguments of the tools and buttons on one job.
type idArgs struct {
	ID string `json:"id"`
}

// strictJSON decodes args into v and refuses unknown fields.
func strictJSON(args json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// jobID is the id of the job a call creates: its key in lower case, so a
// rerun of the approved call finds the job it made.
func jobID(c tool.Call) string { return strings.ToLower(c.TurnID) + "-" + strconv.Itoa(c.N) }

// job builds and checks the job a schedule.create call asks for. Every
// error wraps errInvalid, except a failed lookup of the reply target.
func (s Scheduling) job(ctx context.Context, c tool.Call) (schedule.Job, error) {
	var a createArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return schedule.Job{}, fmt.Errorf("%w arguments: %w", errInvalid, err)
	}
	j := schedule.Job{ID: jobID(c), Owner: c.Principal, Conv: c.Conv, Transport: c.Transport, Kind: schedule.Kind(a.Kind),
		Text: strings.TrimSpace(a.Text), Created: s.Now()}
	if err := checkText(j); err != nil {
		return schedule.Job{}, err
	}
	if err := s.when(&j, a); err != nil {
		return schedule.Job{}, err
	}
	if err := s.grant(ctx, c, &j, a.Grant); err != nil {
		return schedule.Job{}, err
	}
	if err := s.unique(ctx, j); err != nil {
		return schedule.Job{}, err
	}
	return j, nil
}

// unique refuses a job that a live job of the same owner does already:
// the same conversation, kind, schedule and text, white space aside
// (ruling R31). A model that asks twice for one job gets no second
// question, and the fixed answer names the job it has. The job a rerun
// of this call created is not a duplicate.
func (s Scheduling) unique(ctx context.Context, j schedule.Job) error {
	live, err := s.Jobs.List(ctx, j.Owner)
	if err != nil {
		return fmt.Errorf("schedule.create: %w", err)
	}
	for _, o := range live {
		if o.ID != j.ID && !o.Deleted && sameJob(o, j) {
			return fmt.Errorf("schedule.create: job %s already does exactly this and is live; it was not created again", o.ID)
		}
	}
	return nil
}

// sameJob reports whether a and b do the same thing at the same times in
// the same conversation, comparing the cron and the text with white
// space collapsed.
func sameJob(a, b schedule.Job) bool {
	return a.Conv == b.Conv && a.Kind == b.Kind && oneSpace(a.Cron) == oneSpace(b.Cron) && a.At.Equal(b.At) &&
		oneSpace(a.Text) == oneSpace(b.Text)
}

// oneSpace is s with every run of white space one space, trimmed.
func oneSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// checkText checks the kind and the text.
func checkText(j schedule.Job) error {
	switch {
	case j.Kind != schedule.KindReminder && j.Kind != schedule.KindTask:
		return fmt.Errorf("%w: kind %q is neither reminder nor task", errInvalid, j.Kind)
	case j.Text == "":
		return fmt.Errorf("%w: the text is empty", errInvalid)
	case utf8.RuneCountInString(j.Text) > maxTextRunes:
		return fmt.Errorf("%w: the text is longer than %d characters", errInvalid, maxTextRunes)
	}
	return nil
}

// when sets the job's cron or time: exactly one, in the future.
func (s Scheduling) when(j *schedule.Job, a createArgs) error {
	cron, at := strings.TrimSpace(a.Cron), strings.TrimSpace(a.At)
	if (cron == "") == (at == "") {
		return fmt.Errorf("%w: give either cron or at", errInvalid)
	}
	now := s.Now()
	if cron != "" {
		spec, err := schedule.ParseCron(cron)
		if err != nil {
			return fmt.Errorf("%w: %w", errInvalid, err)
		}
		if spec.Next(now, s.Loc).IsZero() {
			return fmt.Errorf("%w: cron %q never comes", errInvalid, cron)
		}
		j.Cron = cron
		return nil
	}
	t, err := s.parseAt(at)
	if err != nil {
		return err
	}
	if !t.After(now) {
		return fmt.Errorf("%w: %s is in the past", errInvalid, t.In(s.Loc).Format(timeLayout))
	}
	j.At = t.UTC()
	return nil
}

// parseAt reads "YYYY-MM-DD HH:MM" (or with a T) in the node's zone, or
// RFC 3339.
func (s Scheduling) parseAt(at string) (time.Time, error) {
	for _, layout := range []string{timeLayout, "2006-01-02T15:04"} {
		if t, err := time.ParseInLocation(layout, at, s.Loc); err == nil {
			return t, nil
		}
	}
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("%w: at %q is not YYYY-MM-DD HH:MM", errInvalid, at)
}

// grant sets a task's grant: registered tools that are not forbidden and
// do not schedule, bare host names for web.fetch, and a known reply
// target (the call's conversation by default). A reminder has none.
func (s Scheduling) grant(ctx context.Context, c tool.Call, j *schedule.Job, g *grantArgs) error {
	if g == nil {
		g = &grantArgs{}
	}
	if j.Kind == schedule.KindReminder {
		if len(g.Tools) > 0 || len(g.Hosts) > 0 || g.ReplyTo != "" {
			return fmt.Errorf("%w: a reminder has no grant", errInvalid)
		}
		return nil
	}
	tools, err := s.grantTools(g.Tools)
	if err != nil {
		return err
	}
	hosts, err := grantHosts(g.Hosts, slices.Contains(tools, toolWebFetch))
	if err != nil {
		return err
	}
	j.Grant = tool.Grant{Tools: tools, Hosts: hosts, ReplyTo: c.Conv}
	if g.ReplyTo != "" && g.ReplyTo != c.Conv {
		if j.Transport, j.Grant.ReplyTo, err = s.Convs.resolve(ctx, c, g.ReplyTo); err != nil {
			return fmt.Errorf("schedule.create: reply_to: %w", err)
		}
	}
	return nil
}

// toolWebFetch is the tool whose hosts a grant lists.
const toolWebFetch = "web.fetch"

// neverGranted are tools an unattended run may never use, registered or
// not: memory is written only with the owner at hand (R-POL-7), and the
// persona only by the owner's own introduction, so a page a task reads
// cannot plant an instruction in either.
var neverGranted = []string{toolMemoryWrite, toolMemoryForget, toolPersonaPropose}

// ungrantable reports whether a grant may never name the tool: the
// schedule tools (a task does not schedule or delete jobs) and
// neverGranted. This is the one list of such tools.
func ungrantable(name string) bool {
	return strings.HasPrefix(name, "schedule.") || slices.Contains(neverGranted, name)
}

// grantTools checks the tools of a grant and returns them sorted, once
// each.
func (s Scheduling) grantTools(names []string) ([]string, error) {
	for _, name := range names {
		spec, ok := s.Specs(name)
		switch {
		case ungrantable(name):
			return nil, fmt.Errorf("%w: a task cannot use %s", errInvalid, name)
		case !ok:
			return nil, fmt.Errorf("%w: unknown tool %q", errInvalid, name)
		case spec.Policy == tool.PolicyForbidden:
			return nil, fmt.Errorf("%w: %s is forbidden", errInvalid, name)
		}
	}
	out := slices.Clone(names)
	slices.Sort(out)
	return slices.Compact(out), nil
}

// grantHosts checks the hosts of a grant: bare lower-case names, given
// exactly when web.fetch is granted.
func grantHosts(hosts []string, fetch bool) ([]string, error) {
	switch {
	case fetch && len(hosts) == 0:
		return nil, fmt.Errorf("%w: web.fetch needs the hosts it may reach", errInvalid)
	case !fetch && len(hosts) > 0:
		return nil, fmt.Errorf("%w: hosts are for web.fetch, which is not granted", errInvalid)
	}
	for _, h := range hosts {
		if !hostPattern.MatchString(h) {
			return nil, fmt.Errorf("%w: host %q is not a bare lower-case host name such as bank.gov.ua", errInvalid, h)
		}
	}
	out := slices.Clone(hosts)
	slices.Sort(out)
	return slices.Compact(out), nil
}

// ScheduleCreate is the schedule.create tool.
type ScheduleCreate struct{ s Scheduling }

// NewScheduleCreate returns schedule.create.
func NewScheduleCreate(s Scheduling) *ScheduleCreate { return &ScheduleCreate{s: s} }

// Spec describes schedule.create: a side effect, so the owner confirms.
func (*ScheduleCreate) Spec() tool.Spec {
	return tool.Spec{
		Name: toolCreate,
		Description: "Schedule a reminder (a fixed text sent at its time, no model) or a task (an instruction run as a full " +
			"assistant turn, unattended). Give cron for a repeating job or at for one time, in the node's time zone. " +
			"A task may use only the tools in its grant, and web.fetch only on the grant's hosts; it runs in its own " +
			"conversation, so its earlier runs are its history. The owner confirms the job with its grant.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"kind":{"type":"string","enum":["reminder","task"]},` +
			`"cron":{"type":"string","description":"five fields: minute hour day-of-month month day-of-week (0 = Sunday); numbers, ranges a-b, lists a,b, steps */n; no names"},` +
			`"at":{"type":"string","description":"one time, YYYY-MM-DD HH:MM in the node's time zone"},` +
			`"text":{"type":"string","description":"the reminder's text or the task's instruction"},` +
			`"grant":{"type":"object","description":"a task's tools, the hosts web.fetch may reach (bare names such as bank.gov.ua), and the conversation or person the answer goes to (default: this chat)",` +
			`"properties":{"tools":{"type":"array","items":{"type":"string"}},"hosts":{"type":"array","items":{"type":"string"}},"reply_to":{"type":"string"}},` +
			`"additionalProperties":false}},` +
			`"required":["kind","text"],"additionalProperties":false}`),
		Class:     tool.ClassSideEffect,
		Policy:    tool.PolicyConfirm,
		AdminOnly: true,
	}
}

// Question implements tool.Questioner: what the owner approves, in plain
// words (design section 5.3). An invalid call is refused before anyone
// is asked.
func (t *ScheduleCreate) Question(ctx context.Context, c tool.Call) (string, error) {
	j, err := t.s.job(ctx, c)
	if err != nil {
		return "", err
	}
	if j.Kind == schedule.KindReminder {
		return "Create a reminder?\n" + t.s.summary(j, c.Conv), nil
	}
	return "Create a scheduled task? It runs unattended, as you, and spends tokens.\n" + t.s.summary(j, c.Conv), nil
}

// Run creates the job. A rerun of the same call finds it created.
func (t *ScheduleCreate) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	j, err := t.s.job(ctx, c)
	if err != nil {
		return tool.Result{}, err
	}
	if err := t.s.Jobs.Create(ctx, j); err != nil && !errors.Is(err, schedule.ErrExists) {
		return tool.Result{}, fmt.Errorf("schedule.create: %w", err)
	}
	return tool.Result{Text: fmt.Sprintf("created %s %s; %s", j.Kind, j.ID, t.s.timing(j))}, nil
}

// ScheduleList is the schedule.list tool.
type ScheduleList struct{ s Scheduling }

// NewScheduleList returns schedule.list.
func NewScheduleList(s Scheduling) *ScheduleList { return &ScheduleList{s: s} }

// Spec describes schedule.list: a read.
func (*ScheduleList) Spec() tool.Spec {
	return tool.Spec{
		Name: toolList,
		Description: "List the owner's reminders and scheduled tasks. Each is also shown to the owner as its own message " +
			"with buttons (Details, Delete, Run now), so do not repeat the list; a short remark is enough.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Class:       tool.ClassRead,
		Policy:      tool.PolicyAuto,
		AdminOnly:   true,
	}
}

// Run lists the caller's jobs: a line each for the model, and a message
// each, with its buttons, for the owner.
func (t *ScheduleList) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	jobs, err := t.s.Jobs.List(ctx, c.Principal)
	if err != nil {
		return tool.Result{}, fmt.Errorf("schedule.list: %w", err)
	}
	if len(jobs) == 0 {
		return tool.Result{Text: "no scheduled jobs"}, nil
	}
	lines := make([]string, 0, len(jobs))
	out := make([]event.Outgoing, 0, len(jobs))
	for _, j := range jobs {
		buttons, err := t.s.buttons(ctx, j, c.Principal, c.Conv, ActionDetails, ActionDelete, ActionRunNow)
		if err != nil {
			return tool.Result{}, fmt.Errorf("schedule.list: %w", err)
		}
		lines = append(lines, "- "+j.ID+" ("+t.s.title(j)+", "+t.s.timing(j)+"): "+j.Text)
		out = append(out, event.Outgoing{IdemKey: c.Key() + ":list:" + j.ID, Transport: c.Transport, Conv: c.Conv,
			Text: t.s.entry(j), Buttons: buttons})
	}
	return tool.Result{Text: strconv.Itoa(len(jobs)) + " jobs, shown to the owner with buttons:\n" + strings.Join(lines, "\n"), Out: out}, nil
}

// ScheduleDelete is the schedule.delete tool.
type ScheduleDelete struct{ s Scheduling }

// NewScheduleDelete returns schedule.delete.
func NewScheduleDelete(s Scheduling) *ScheduleDelete { return &ScheduleDelete{s: s} }

// Spec describes schedule.delete: a side effect, so the owner confirms.
func (*ScheduleDelete) Spec() tool.Spec {
	return tool.Spec{
		Name:        ActionDelete,
		Description: "Delete one of the owner's reminders or scheduled tasks by its id (from schedule.list). The owner confirms. A system job cannot be deleted.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`),
		Class:       tool.ClassSideEffect,
		Policy:      tool.PolicyConfirm,
		AdminOnly:   true,
	}
}

// Question implements tool.Questioner.
func (t *ScheduleDelete) Question(ctx context.Context, c tool.Call) (string, error) {
	j, err := t.s.own(ctx, c)
	if err != nil {
		return "", err
	}
	return "Delete this " + string(j.Kind) + "?\n" + t.s.summary(j, c.Conv), nil
}

// Run deletes the job; schedule.ErrSystem for a system job.
func (t *ScheduleDelete) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	var a idArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return tool.Result{}, fmt.Errorf("%s: arguments: %w", ActionDelete, err)
	}
	if err := t.s.Jobs.Delete(ctx, a.ID, c.Principal); err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ActionDelete, err)
	}
	return tool.Result{Text: "deleted " + a.ID}, nil
}

// ScheduleRunNow is the schedule.run_now tool.
type ScheduleRunNow struct{ s Scheduling }

// NewScheduleRunNow returns schedule.run_now.
func NewScheduleRunNow(s Scheduling) *ScheduleRunNow { return &ScheduleRunNow{s: s} }

// Spec describes schedule.run_now: a side effect, so the owner confirms
// when the model asks; the owner's own button asks only for a task that
// may act (design section 5.3).
func (*ScheduleRunNow) Spec() tool.Spec {
	return tool.Spec{
		Name:        ActionRunNow,
		Description: "Run one of the owner's scheduled tasks now, outside its schedule, by its id. The owner confirms; the task's answer goes where its runs go.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false}`),
		Class:       tool.ClassSideEffect,
		Policy:      tool.PolicyConfirm,
		AdminOnly:   true,
	}
}

// Question implements tool.Questioner.
func (t *ScheduleRunNow) Question(ctx context.Context, c tool.Call) (string, error) {
	j, err := t.s.own(ctx, c)
	if err != nil {
		return "", err
	}
	return runNowQuestion + t.s.summary(j, c.Conv), nil
}

const runNowQuestion = "Run this task now?\n"

// Run puts the task's run now, keyed by the call's key: a confirmed call
// keeps its key, so a rerun of the turn that ran it (a crash before its
// outcome was stored) starts the same run, not a second one (R-DLV-6).
func (t *ScheduleRunNow) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	var a idArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return tool.Result{}, fmt.Errorf("%s: arguments: %w", ActionRunNow, err)
	}
	if err := t.s.Runner.RunKey(ctx, a.ID, c.Principal, c.Key()); err != nil {
		return tool.Result{}, fmt.Errorf("%s: %w", ActionRunNow, err)
	}
	return tool.Result{Text: "started " + a.ID + "; its answer goes where its runs go"}, nil
}

// own returns the caller's job named in the call's "id"; ErrNotFound for
// someone else's or a deleted one.
func (s Scheduling) own(ctx context.Context, c tool.Call) (schedule.Job, error) {
	var a idArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return schedule.Job{}, fmt.Errorf("%s: arguments: %w", c.Name, err)
	}
	j, err := s.Jobs.Get(ctx, a.ID)
	if err == nil && (j.Deleted || j.Owner != c.Principal) {
		err = fmt.Errorf("%s: job %s: %w", c.Name, a.ID, schedule.ErrNotFound)
	}
	return j, err
}
