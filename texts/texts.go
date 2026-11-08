// Package texts holds every fixed text a person reads, under a stable key, in the languages the
// node knows (R-LANG-1, R-LANG-2, ADR-0053). It reads only its embedded files; who a person is and
// what their language is belong to package language.
package texts

import (
	"context"
	"embed"
	"fmt"
	"maps"
	"regexp"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.yaml.in/yaml/v3"
)

// Lang is a language's primary subtag, from the table of native names.
type Lang string

// English is the default, and what a text falls back to.
const English Lang = "en"

// Key names one fixed text. Its prefix says where the text goes, and so its limit.
type Key string

// The keys. en.yaml holds their English texts; a test keeps the two the same.
const (
	ButtonApprove Key = "button.approve"
	ButtonDeny    Key = "button.deny"

	ToastApproved   Key = "toast.approved"
	ToastDeclined   Key = "toast.declined"
	ToastAccepted   Key = "toast.accepted"
	ToastNotYours   Key = "toast.not_yours"
	ToastNoButton   Key = "toast.no_button"
	ToastUsed       Key = "toast.used"
	ToastExpired    Key = "toast.expired"
	ToastNotAllowed Key = "toast.not_allowed"

	LineRunning        Key = "line.running"
	LineApproved       Key = "line.approved"
	LineApprovedFailed Key = "line.approved_failed"
	LineDeclined       Key = "line.declined"
	LineUsed           Key = "line.used"
	LineExpired        Key = "line.expired"

	ReplyNoModel      Key = "reply.no_model"
	ReplyStepLimit    Key = "reply.step_limit"
	ReplyExpired      Key = "reply.expired"
	ReplyDailyLimit   Key = "reply.daily_limit"
	ReplyGiveUp       Key = "reply.give_up"
	ReplyNewSession   Key = "reply.new_session"
	ReplyNoSessions   Key = "reply.no_sessions"
	ReplyNoSuchResume Key = "reply.no_such_resume"
	ReplyResumed      Key = "reply.resumed"
	ReplySessionRow   Key = "reply.session_row"
	ReplyNoTasks      Key = "reply.no_tasks"
	ReplyNotAvailable Key = "reply.not_available"

	ButtonDetails        Key = "button.details"
	ButtonDelete         Key = "button.delete"
	ButtonRunNow         Key = "button.run_now"
	AskLanguage          Key = "ask.language"
	AskLanguageNoFile    Key = "ask.language_no_file"
	AskRunTool           Key = "ask.run_tool"
	AskSendMessage       Key = "ask.send_message"
	AskCreateReminder    Key = "ask.create_reminder"
	AskCreateTask        Key = "ask.create_task"
	AskDeleteReminder    Key = "ask.delete_reminder"
	AskDeleteTask        Key = "ask.delete_task"
	AskRunNow            Key = "ask.run_now"
	AskOpenPage          Key = "ask.open_page"
	AskSwitchCell        Key = "ask.switch_cell"
	AskRemember          Key = "ask.remember"
	AskForgetFact        Key = "ask.forget_fact"
	AskForgetSlug        Key = "ask.forget_slug"
	AskPersona           Key = "ask.persona"
	AskSkillScript       Key = "ask.skill_script"
	SchedSummaryReminder Key = "sched.summary_reminder"
	SchedSummaryTask     Key = "sched.summary_task"
	SchedEntry           Key = "sched.entry"
	SchedDetails         Key = "sched.details"
	SchedOnce            Key = "sched.once"
	SchedEvery           Key = "sched.every"
	SchedEveryNext       Key = "sched.every_next"
	SchedKindReminder    Key = "sched.kind_reminder"
	SchedKindTask        Key = "sched.kind_task"
	SchedSystem          Key = "sched.system"
	SchedKindSystem      Key = "sched.kind_system"
	SchedAuditText       Key = "sched.audit_text"

	NoticeMachine      Key = "notice.machine"
	ReportAuditClean   Key = "report.audit_clean"
	ReportAuditFlagged Key = "report.audit_flagged"
	SchedGone          Key = "sched.gone"
	SchedDeleted       Key = "sched.deleted"
	SchedNoDelete      Key = "sched.no_delete"
	SchedNotTask       Key = "sched.not_task"
	SchedThisChat      Key = "sched.this_chat"
	SchedNone          Key = "sched.none"
	SchedStarted       Key = "sched.started"

	StatusNoAnswer           Key = "status.no_answer"
	StatusVerdictOK          Key = "status.verdict_ok"
	StatusVerdictWarn        Key = "status.verdict_warn"
	StatusVerdictFail        Key = "status.verdict_fail"
	StatusSectionNode        Key = "status.section_node"
	StatusSectionModels      Key = "status.section_models"
	StatusSectionDelivery    Key = "status.section_delivery"
	StatusSectionProblems    Key = "status.section_problems"
	StatusSince              Key = "status.since"
	StatusSession            Key = "status.session"
	StatusSessionFallback    Key = "status.session_fallback"
	StatusSessionNone        Key = "status.session_none"
	StatusDay                Key = "status.day"
	StatusReasonLimitNear    Key = "status.reason_limit_near"
	StatusReasonLimitReached Key = "status.reason_limit_reached"
	StatusVersion            Key = "status.version"
	StatusVersionModified    Key = "status.version_modified"
	StatusVersionUnknown     Key = "status.version_unknown"
	StatusUptime             Key = "status.uptime"
	StatusMemory             Key = "status.memory"
	StatusMemoryNoLimit      Key = "status.memory_no_limit"
	StatusReasonMemoryHigh   Key = "status.reason_memory_high"
	StatusFiles              Key = "status.files"
	StatusFilesNoLogs        Key = "status.files_no_logs"
	StatusModelAnswered      Key = "status.model_answered"
	StatusModelSilent        Key = "status.model_silent"
	StatusReasonModelFailed  Key = "status.reason_model_failed"
	StatusTurnsFailed        Key = "status.turns_failed"
	StatusTelegram           Key = "status.telegram"
	StatusTelegramWaiting    Key = "status.telegram_waiting"
	StatusReasonPollStale    Key = "status.reason_poll_stale"
	StatusReasonConflict     Key = "status.reason_conflict"
	StatusReasonUnauthorized Key = "status.reason_unauthorized"
	StatusQueues             Key = "status.queues"
	StatusReasonQueueOld     Key = "status.reason_queue_old"
	StatusReasonUndelivered  Key = "status.reason_undelivered"
)

var allKeys = []Key{
	ButtonApprove, ButtonDeny,
	ToastApproved, ToastDeclined, ToastAccepted, ToastNotYours, ToastNoButton, ToastUsed, ToastExpired, ToastNotAllowed,
	LineRunning, LineApproved, LineApprovedFailed, LineDeclined, LineUsed, LineExpired,
	ReplyNoModel, ReplyStepLimit, ReplyExpired, ReplyDailyLimit, ReplyGiveUp, ReplyNewSession, ReplyNoSessions,
	ReplyNoSuchResume, ReplyResumed, ReplySessionRow, ReplyNoTasks, ReplyNotAvailable,
	ButtonDetails, ButtonDelete, ButtonRunNow, AskLanguage, AskLanguageNoFile, AskRunTool, AskSendMessage, AskCreateReminder, AskCreateTask, AskDeleteReminder, AskDeleteTask, AskRunNow, AskOpenPage, AskSwitchCell, AskRemember, AskForgetFact, AskForgetSlug, AskPersona, AskSkillScript, SchedSummaryReminder, SchedSummaryTask, SchedEntry, SchedDetails, SchedOnce, SchedEvery, SchedEveryNext, SchedKindReminder, SchedKindTask, SchedSystem, SchedKindSystem, SchedAuditText, NoticeMachine, ReportAuditClean, ReportAuditFlagged, SchedGone, SchedDeleted, SchedNoDelete, SchedNotTask, SchedThisChat, SchedNone, SchedStarted,
	StatusNoAnswer, StatusVerdictOK, StatusVerdictWarn, StatusVerdictFail, StatusSectionNode, StatusSectionModels, StatusSectionDelivery, StatusSectionProblems, StatusSince, StatusSession, StatusSessionFallback, StatusSessionNone, StatusDay, StatusReasonLimitNear, StatusReasonLimitReached, StatusVersion, StatusVersionModified, StatusVersionUnknown, StatusUptime, StatusMemory, StatusMemoryNoLimit, StatusReasonMemoryHigh, StatusFiles, StatusFilesNoLogs, StatusModelAnswered, StatusModelSilent, StatusReasonModelFailed, StatusTurnsFailed, StatusTelegram, StatusTelegramWaiting, StatusReasonPollStale, StatusReasonConflict, StatusReasonUnauthorized, StatusQueues, StatusReasonQueueOld, StatusReasonUndelivered,
}

// EnglishAll returns every English text, which a file or a translation is checked against.
func EnglishAll() map[Key]string { return maps.Clone(shipped[English]) }

// Keys returns every key.
func Keys() []Key { return allKeys }

// Limits of a text, in characters: Telegram's for a toast, one that fits a button on a phone, and
// the length of one message for the rest.
const (
	limitToast   = 200
	limitButton  = 32
	limitMessage = 4000
	// limitQuestion leaves a question room for its values under tool.MaxQuestionRunes, which the
	// policy cuts at: a person must see the whole of what they approve (ruling R34).
	limitQuestion = 400
	// limitInQuestion is for a text that goes inside a question, so several add up.
	limitInQuestion = 300
)

// limit returns the most characters k's text may have.
func limit(k Key) int {
	switch {
	case strings.HasPrefix(string(k), "toast."):
		return limitToast
	case strings.HasPrefix(string(k), "button."):
		return limitButton
	case strings.HasPrefix(string(k), "ask."):
		return limitQuestion
	case strings.HasPrefix(string(k), "sched."):
		return limitInQuestion
	default:
		return limitMessage
	}
}

// IsApproval reports whether a person reads k's text to decide an approval. A machine translation
// missing any of these is refused whole (R-LANG-5).
func IsApproval(k Key) bool {
	switch k {
	case ButtonApprove, ButtonDeny, ButtonDelete, ButtonRunNow, SchedSummaryReminder, SchedSummaryTask,
		SchedOnce, SchedEvery, SchedEveryNext, SchedThisChat, SchedNone:
		return true
	default:
		return strings.HasPrefix(string(k), "ask.")
	}
}

// Source says where a text came from.
type Source string

// Sources, highest priority first (R-LANG-4).
const (
	SourceOperator Source = "operator"
	SourceShipped  Source = "shipped"
	SourceMachine  Source = "machine"
	SourceEnglish  Source = "english"
)

// Vars are the values of a text's placeholders.
type Vars map[string]string

// PackFunc finds a machine translation. It must not block: it is called while a message is built.
type PackFunc func(Lang, Key) (string, bool)

//go:embed lang/*.yaml
var langFiles embed.FS

// shipped holds the embedded files. A file that does not parse fails at the first test, so a
// running binary always has them.
var shipped = mustLoad(English, "ru", "uk")

func mustLoad(langs ...Lang) map[Lang]map[Key]string {
	out := make(map[Lang]map[Key]string, len(langs))
	for _, l := range langs {
		raw, err := langFiles.ReadFile("lang/" + string(l) + ".yaml")
		if err != nil {
			panic(fmt.Sprintf("texts: %s: %v", l, err))
		}
		file := map[Key]string{}
		if err := yaml.Unmarshal(raw, &file); err != nil {
			panic(fmt.Sprintf("texts: %s: %v", l, err))
		}
		out[l] = file
	}
	return out
}

// EnglishText returns k's English text.
func EnglishText(k Key) string { return shipped[English][k] }

// Speaker writes in one person's language. The zero Speaker writes English from the shipped files,
// which is what a text a model reads wants.
type Speaker struct {
	Lang Lang
	Cat  *Catalog
}

func (s Speaker) catalog() *Catalog {
	if s.Cat != nil {
		return s.Cat
	}
	return Shipped()
}

// Text is Catalog.Text in s's language.
func (s Speaker) Text(k Key, v Vars) string { return s.catalog().Text(s.Lang, k, v) }

// In is s writing in another language, from the same catalogue.
func (s Speaker) In(l Lang) Speaker { return Speaker{Lang: l, Cat: s.Cat} }

// Has reports whether s's language has its approval texts; see Catalog.Has.
func (s Speaker) Has() bool { return s.catalog().Has(s.Lang) }

// Label is Catalog.Label in s's language.
func (s Speaker) Label(k Key) string { return s.catalog().Label(s.Lang, k, nil) }

// Option configures a Catalog.
type Option func(*Catalog)

// WithFiles sets the operator's files, already checked. They come before everything else.
func WithFiles(files map[Lang]map[Key]string) Option {
	return func(c *Catalog) { c.operator = files }
}

// WithPacks sets where machine translations are found.
func WithPacks(p PackFunc) Option { return func(c *Catalog) { c.pack = p } }

// Catalog finds a text for a language, key by key.
type Catalog struct {
	operator map[Lang]map[Key]string
	pack     PackFunc
	sent     metric.Int64Counter // nil for Shipped, which nobody reads metrics of
}

// Shipped returns a catalogue of the shipped files alone.
func Shipped() *Catalog { return &Catalog{pack: noPack} }

func noPack(Lang, Key) (string, bool) { return "", false }

// New returns a catalogue of the shipped files and whatever opts add.
func New(opts ...Option) (*Catalog, error) {
	c := Shipped()
	c.sent, _ = otel.Meter("ganglion/texts").Int64Counter("ganglion.texts.sent",
		metric.WithDescription("Fixed texts written, by language and source (operator, shipped, machine, english)."))
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// find returns k's template in l and where it came from (R-LANG-4).
func (c *Catalog) find(l Lang, k Key) (string, Source) {
	if s, ok := c.operator[l][k]; ok {
		return s, SourceOperator
	}
	if s, ok := shipped[l][k]; ok {
		return s, SourceShipped
	}
	if s, ok := c.pack(l, k); ok {
		return s, SourceMachine
	}
	return EnglishText(k), SourceEnglish
}

// Source returns where k's text in l comes from.
func (c *Catalog) Source(l Lang, k Key) Source {
	_, src := c.find(l, k)
	return src
}

// Has reports whether l has every approval text from a file or a translation.
func (c *Catalog) Has(l Lang) bool {
	for _, k := range allKeys {
		if IsApproval(k) && c.Source(l, k) == SourceEnglish {
			return false
		}
	}
	return true
}

// written finds k's template in l and counts it.
func (c *Catalog) written(l Lang, k Key) string {
	s, src := c.find(l, k)
	if c.sent != nil {
		c.sent.Add(context.Background(), 1, metric.WithAttributes(attribute.String("lang", string(l)), attribute.String("source", string(src))))
	}
	return s
}

// placeholder matches anything in braces, so a translated placeholder is seen as one.
var placeholder = regexp.MustCompile(`\{[^{}]*\}`)

// Text returns k's text in l for a message, which the transport renders as Markdown. The template
// is escaped, so a translation cannot add formatting; the values keep theirs.
func (c *Catalog) Text(l Lang, k Key, v Vars) string {
	return fill(c.written(l, k), v, escapeMarkdown)
}

// Label returns k's text in l for a button or a toast, which are not rendered.
func (c *Catalog) Label(l Lang, k Key, v Vars) string {
	return fill(c.written(l, k), v, func(s string) string { return s })
}

// fill puts v into the template in one pass, so a value holding braces stays as it is.
func fill(template string, v Vars, literal func(string) string) string {
	var b strings.Builder
	last := 0
	for _, m := range placeholder.FindAllStringIndex(template, -1) {
		b.WriteString(literal(template[last:m[0]]))
		token := template[m[0]:m[1]]
		if val, ok := v[token[1:len(token)-1]]; ok {
			b.WriteString(val)
		} else {
			b.WriteString(literal(token))
		}
		last = m[1]
	}
	b.WriteString(literal(template[last:]))
	return b.String()
}

// Escape keeps s as written when it goes into a message as a value: a skill named my_skill is not
// read as italics.
func Escape(s string) string { return escapeMarkdown(s) }

// escapeMarkdown puts a backslash before each mark that formats, the same marks a translation may
// not add (Check). Ordinary punctuation stays as it is, so a text reads the same in a log or a test.
func escapeMarkdown(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(markupMarks+`&\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
