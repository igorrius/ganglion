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
	ToastReplaced   Key = "toast.replaced"

	LineRunning        Key = "line.running"
	LineApproved       Key = "line.approved"
	LineApprovedFailed Key = "line.approved_failed"
	LineDeclined       Key = "line.declined"
	LineUsed           Key = "line.used"
	LineExpired        Key = "line.expired"
	LineReplaced       Key = "line.replaced"

	ReplyNoModel       Key = "reply.no_model"
	ReplyStepLimit     Key = "reply.step_limit"
	ReplyExpired       Key = "reply.expired"
	ReplyDailyLimit    Key = "reply.daily_limit"
	ReplyGiveUp        Key = "reply.give_up"
	ReplyNewSession    Key = "reply.new_session"
	ReplyNoSessions    Key = "reply.no_sessions"
	ReplyNoSuchResume  Key = "reply.no_such_resume"
	ReplyResumed       Key = "reply.resumed"
	ReplySessionRow    Key = "reply.session_row"
	ReplyNoTasks       Key = "reply.no_tasks"
	ReplyNotAvailable  Key = "reply.not_available"
	ReplyCommandsOn    Key = "reply.commands_on"
	ReplyCommandsOff   Key = "reply.commands_off"
	ReplyCommandsUsage Key = "reply.commands_usage"
	ReplyCommandLate   Key = "reply.command_late"
	ReplyBuildLate     Key = "reply.build_late"
	ReplyRestarting    Key = "reply.restarting"
	ReplyBack          Key = "reply.back"         // vars: seconds
	ReplyBackApplied   Key = "reply.back_applied" // vars: changes

	ReplyIntakeExpired      Key = "reply.intake_expired"        // a pasted line of a request that ran out (R-INT-5)
	ReplyIntakeUnknown      Key = "reply.intake_unknown"        // no open request of that id
	ReplyIntakeVersion      Key = "reply.intake_version"        // a line of a page version the node does not know
	ReplyIntakeMalformed    Key = "reply.intake_malformed"      // a line that does not parse
	ReplyIntakeFailed       Key = "reply.intake_failed"         // a line that was taken and not accepted
	ReplyIntakeAlone        Key = "reply.intake_alone"          // a line with other text, in a caption or forwarded
	SecretCardNode          Key = "secret.card_node"            // the card's head for the node's own secret; vars: name
	SecretCardModule        Key = "secret.card_module"          // the card's head for a module; vars: who, name, hosts
	SecretCardModuleNoHosts Key = "secret.card_module_no_hosts" // the card's head for a module that reaches no host; vars: who, name
	SecretCardServer        Key = "secret.card_server"          // the card's head for an MCP server's header key; vars: who, name, hosts
	SecretCardWhy           Key = "secret.card_why"             // the agent's reason, quoted as data; vars: why
	SecretCardTainted       Key = "secret.card_tainted"         // the card's note for a session that read outside content
	SecretCardBody          Key = "secret.card_body"            // the card's instructions; vars: link, time
	SecretEnter             Key = "secret.enter"                // the link's text on the card
	SecretStopped           Key = "secret.stopped"              // the intake page failed its check (R-INT-3); vars: name
	SecretLineSet           Key = "secret.line_set"             // a folded card: the secret is set; vars: name, whose, time
	SecretLineFailed        Key = "secret.line_failed"          // a folded card: the line was not accepted; vars: name, whose, time
	SecretLineLocked        Key = "secret.line_locked"          // a folded card: the store is locked; vars: name, whose, time
	SecretLineExpired       Key = "secret.line_expired"         // a folded card: nobody answered in time; vars: name, whose, time
	SecretWhoseNode         Key = "secret.whose_node"           // inside a folded card: the node's own
	SecretWhoseModule       Key = "secret.whose_module"         // inside a folded card: a module's; vars: who
	SecretWhoseServer       Key = "secret.whose_server"         // inside a folded card: a server's; vars: who

	ButtonDetails        Key = "button.details"
	ButtonDelete         Key = "button.delete"
	ButtonRunNow         Key = "button.run_now"
	ButtonDone           Key = "button.done"
	ButtonAllow          Key = "button.allow"
	ButtonRefuse         Key = "button.refuse"
	GroupRequestCard     Key = "group.request_card"  // vars: title, id, name
	GroupNotConnected    Key = "group.not_connected" // said once in a group that is not connected
	GroupAllowed         Key = "group.allowed"       // vars: title
	GroupRefused         Key = "group.refused"       // vars: title
	AskLanguage          Key = "ask.language"
	AskLanguageNoFile    Key = "ask.language_no_file"
	AskRunTool           Key = "ask.run_tool"
	AskSendMessage       Key = "ask.send_message"
	AskDestinationSet    Key = "ask.destination_set" // vars: name, place
	AskConfigPropose     Key = "ask.config_propose"  // vars: lines, reason
	AskConfigTainted     Key = "ask.config_tainted"
	AskConfigKept        Key = "ask.config_kept" // vars: fields
	AskConfigDefault     Key = "ask.config_default"
	ReplyConfigWritten   Key = "reply.config_written"
	ButtonConfigRestart  Key = "button.config_restart"
	AskDestinationRemove Key = "ask.destination_remove" // vars: name
	AskModuleNotifyTo    Key = "ask.module_notify_to"   // vars: module, places
	AskGroupDisconnect   Key = "ask.group_disconnect"   // vars: title
	AskLinkAdd           Key = "ask.link_add"           // vars: from, to, shape
	AskLinkRemove        Key = "ask.link_remove"        // vars: from, to
	PlacePrivate         Key = "ask.place_private"      // what a destination points at, inside a question
	PlaceGroup           Key = "ask.place_group"        // vars: title
	PlaceTopic           Key = "ask.place_topic"        // vars: topic, title
	PlaceTopicNumber     Key = "ask.place_topic_number" // vars: n, title
	PlaceTopicGiven      Key = "ask.place_topic_given"  // vars: topic, n, title: a title the owner gave, not yet seen
	PlaceGeneral         Key = "ask.place_general"      // vars: title: a forum's General topic
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
	AskForgetTopic       Key = "ask.forget_topic"
	AskPersona           Key = "ask.persona"
	AskSkillScript       Key = "ask.skill_script"
	AskHostExec          Key = "ask.host_exec"
	AskPlan              Key = "ask.plan"
	AskModuleInstall     Key = "ask.module_install"
	AskModuleNetwork     Key = "ask.module_network"
	AskModuleSecrets     Key = "ask.module_secrets"
	AskModuleCreates     Key = "ask.module_creates"
	AskModuleTakes       Key = "ask.module_takes"
	AskModuleReads       Key = "ask.module_reads"
	AskModuleReacts      Key = "ask.module_reacts"
	AskModuleNotify      Key = "ask.module_notify"
	AskModuleEmits       Key = "ask.module_emits"
	AskModuleAccepts     Key = "ask.module_accepts"
	AskModuleServes      Key = "ask.module_serves"
	AskModulePages       Key = "ask.module_pages"
	AskModuleCalls       Key = "ask.module_calls"
	AskModuleRuns        Key = "ask.module_runs"
	AskModuleTool        Key = "ask.module_tool"
	AskModuleAbout       Key = "ask.module_about"
	AskModuleLimits      Key = "ask.module_limits"
	AskModuleNew         Key = "ask.module_new"
	AskModuleNewNetwork  Key = "ask.module_new_network"
	AskModuleNewSecrets  Key = "ask.module_new_secrets"
	AskModuleNewCells    Key = "ask.module_new_cells"
	AskModuleNewReads    Key = "ask.module_new_reads"
	AskModuleNewReacts   Key = "ask.module_new_reacts"
	AskModuleNewNotify   Key = "ask.module_new_notify"
	AskModuleNewEmits    Key = "ask.module_new_emits"
	AskModuleNewAccepts  Key = "ask.module_new_accepts"
	AskModuleNewServes   Key = "ask.module_new_serves"
	AskModuleNewPages    Key = "ask.module_new_pages"
	AskModuleNewCalls    Key = "ask.module_new_calls"
	AskModuleNewRuns     Key = "ask.module_new_runs"
	AskModuleNewMemory   Key = "ask.module_new_memory"
	AskModuleNewTimeout  Key = "ask.module_new_timeout"

	AskModuleTrial        Key = "ask.module_trial"
	AskModuleTrialNone    Key = "ask.module_trial_none"
	AskModuleTrialExample Key = "ask.module_trial_example"
	AskModuleTrialMessage Key = "ask.module_trial_message"
	AskModuleTrialCell    Key = "ask.module_trial_cell"
	AskModuleTrialEvent   Key = "ask.module_trial_event"
	AskModuleTrialNothing Key = "ask.module_trial_nothing"
	AskModuleTrialNow     Key = "ask.module_trial_now"
	AskModuleTrialMore    Key = "ask.module_trial_more"
	AskModuleTrialNotRun  Key = "ask.module_trial_not_run"
	AskModuleTrialFailed  Key = "ask.module_trial_failed"
	AskModuleTrialError   Key = "ask.module_trial_error"
	AskModuleOutside      Key = "ask.module_outside"
	AskModuleRequest      Key = "ask.module_request" // vars: items
	AskModuleLinks        Key = "ask.module_links"
	AskModuleLink         Key = "ask.module_link" // vars: from, to, shape
	SchedSummaryReminder  Key = "sched.summary_reminder"
	SchedSummaryTask      Key = "sched.summary_task"
	SchedSummaryCommands  Key = "sched.summary_commands"
	SchedEntry            Key = "sched.entry"
	SchedDetails          Key = "sched.details"
	SchedOnce             Key = "sched.once"
	SchedEvery            Key = "sched.every"
	SchedEveryNext        Key = "sched.every_next"
	SchedKindReminder     Key = "sched.kind_reminder"
	SchedKindTask         Key = "sched.kind_task"
	SchedSystem           Key = "sched.system"
	SchedKindSystem       Key = "sched.kind_system"
	SchedKindModule       Key = "sched.kind_module"
	SchedAuditText        Key = "sched.audit_text"

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
	SchedReminderSet   Key = "sched.reminder_set"
	SchedReminderOff   Key = "sched.reminder_off"
	SchedOff           Key = "sched.off" // vars: text

	StatusNoAnswer               Key = "status.no_answer"
	StatusCommandsOff            Key = "status.commands_off"
	StatusVerdictOK              Key = "status.verdict_ok"
	StatusVerdictWarn            Key = "status.verdict_warn"
	StatusVerdictFail            Key = "status.verdict_fail"
	StatusSectionNode            Key = "status.section_node"
	StatusSectionModels          Key = "status.section_models"
	StatusSectionDelivery        Key = "status.section_delivery"
	StatusSectionProblems        Key = "status.section_problems"
	StatusSince                  Key = "status.since"
	StatusSession                Key = "status.session"
	StatusSessionFallback        Key = "status.session_fallback"
	StatusSessionNone            Key = "status.session_none"
	StatusDay                    Key = "status.day"
	StatusReasonLimitNear        Key = "status.reason_limit_near"
	StatusReasonLimitReached     Key = "status.reason_limit_reached"
	StatusVersion                Key = "status.version"
	StatusVersionModified        Key = "status.version_modified"
	StatusVersionUnknown         Key = "status.version_unknown"
	StatusUptime                 Key = "status.uptime"
	StatusMemory                 Key = "status.memory"
	StatusMemoryNoLimit          Key = "status.memory_no_limit"
	StatusReasonMemoryHigh       Key = "status.reason_memory_high"
	StatusFiles                  Key = "status.files"
	StatusFilesNoLogs            Key = "status.files_no_logs"
	StatusModelAnswered          Key = "status.model_answered"
	StatusModelSilent            Key = "status.model_silent"
	StatusReasonModelFailed      Key = "status.reason_model_failed"
	StatusTurnsFailed            Key = "status.turns_failed"
	StatusTelegram               Key = "status.telegram"
	StatusTelegramWaiting        Key = "status.telegram_waiting"
	StatusReasonPollStale        Key = "status.reason_poll_stale"
	StatusReasonConflict         Key = "status.reason_conflict"
	StatusReasonUnauthorized     Key = "status.reason_unauthorized"
	StatusTelegramWebhook        Key = "status.telegram_webhook"
	StatusTelegramWebhookQuiet   Key = "status.telegram_webhook_quiet"
	StatusReasonWebhookPending   Key = "status.reason_webhook_pending"
	StatusReasonWebhookFallback  Key = "status.reason_webhook_fallback"
	StatusReasonWebhookFailing   Key = "status.reason_webhook_failing"
	StatusReasonWebhookTaken     Key = "status.reason_webhook_taken"
	StatusQueues                 Key = "status.queues"
	StatusReasonQueueOld         Key = "status.reason_queue_old"
	StatusReasonUndelivered      Key = "status.reason_undelivered"
	StatusSkills                 Key = "status.skills"
	StatusSkillRefused           Key = "status.skill_refused"
	StatusReasonSkillShape       Key = "status.reason_skill_shape"
	StatusReasonSkillRead        Key = "status.reason_skill_read"
	StatusReasonSkillFrontmatter Key = "status.reason_skill_frontmatter"
	StatusReasonSkillScript      Key = "status.reason_skill_script"
	StatusReasonSkillBuiltin     Key = "status.reason_skill_builtin"
	SecretsPrivateOnly           Key = "secrets.private_only"
	SecretsEmpty                 Key = "secrets.empty"
	SecretsHead                  Key = "secrets.head"
	SecretsScopeNode             Key = "secrets.scope_node"
	SecretsRow                   Key = "secrets.row"
	SecretsLocked                Key = "secrets.locked"
	SecretsRemoved               Key = "secrets.removed"
	SecretsNotFound              Key = "secrets.not_found"
	SecretsReserved              Key = "secrets.reserved" // vars: name: a bus.* name, the node's own key
	SecretsResetAsk              Key = "secrets.reset_ask"
	SecretsResetDone             Key = "secrets.reset_done"
	SecretsUsage                 Key = "secrets.usage"
	StatusSecretsLocked          Key = "status.secrets_locked"
	StatusReasonSecretsKey       Key = "status.reason_secrets_key"
	StatusModuleFallback         Key = "status.module_fallback"
	StatusReasonModuleFailed     Key = "status.reason_module_failed"
	StatusModulesLocal           Key = "status.modules_local"
	StatusModuleOff              Key = "status.module_off"
	StatusReasonModuleCrashloop  Key = "status.reason_module_crashloop"
	StatusModuleStopped          Key = "status.module_stopped"
	StatusModulesUnread          Key = "status.modules_unread"
	StatusLinkBroken             Key = "status.link_broken"      // vars: link
	StatusReasonLinkEnd          Key = "status.reason_link_end"  // a module is not running or has no such port
	StatusReasonLinkKind         Key = "status.reason_link_kind" // the ends are not an output and an input of one kind
	StatusReasonLinkShape        Key = "status.reason_link_shape"
	StatusReasonLinkTaken        Key = "status.reason_link_taken" // a calling port has one link
	StatusReasonLinkLoop         Key = "status.reason_link_loop"  // vars: path
	StatusReasonLinkDeep         Key = "status.reason_link_deep"  // vars: path, max
	StatusReasonModuleInit       Key = "status.reason_module_init"
	StatusReasonModuleCells      Key = "status.reason_module_cells"
	StatusReasonModuleLoad       Key = "status.reason_module_load"
	StatusReasonModuleNoAdmin    Key = "status.reason_module_no_admin"
	StatusJobs                   Key = "status.jobs"
	StatusJobsIdle               Key = "status.jobs_idle"
	StatusJobsNone               Key = "status.jobs_none"
	StatusSectionJobs            Key = "status.section_jobs"
	StatusJobBuild               Key = "status.job_build"
	StatusJobCommand             Key = "status.job_command"
	StatusJobBuildQueued         Key = "status.job_build_queued"
	StatusJobCommandQueued       Key = "status.job_command_queued"
	StatusReasonSchedLag         Key = "status.reason_sched_lag"
	StatusReasonSchedFailing     Key = "status.reason_sched_failing"
	StatusReasonSchedStalled     Key = "status.reason_sched_stalled"
	StatusUnitDay                Key = "status.unit_day"
	StatusUnitHour               Key = "status.unit_hour"
	StatusUnitMin                Key = "status.unit_min"
	StatusUnitSec                Key = "status.unit_sec"
	StatusSandboxReady           Key = "status.sandbox_ready"
	StatusSandboxOff             Key = "status.sandbox_off"
	StatusReasonSandboxNotLinux  Key = "status.reason_sandbox_not_linux"
	StatusReasonSandboxRoot      Key = "status.reason_sandbox_root"
	StatusReasonSandboxLandlock  Key = "status.reason_sandbox_landlock"
	StatusReasonSandboxDumpable  Key = "status.reason_sandbox_dumpable"
	StatusReasonSandboxSelfTest  Key = "status.reason_sandbox_self_test"

	CmdRead               Key = "cmd.read"
	CmdContent            Key = "cmd.content"
	CmdUnknown            Key = "cmd.unknown"
	CmdPath               Key = "cmd.path"
	CmdScript             Key = "cmd.script"
	CmdRoot               Key = "cmd.root"
	CmdOutside            Key = "cmd.outside"
	CmdAllEnv             Key = "cmd.all_env"
	CmdScriptFile         Key = "cmd.script_file"
	CmdDockerOther        Key = "cmd.docker_other"
	CmdRemove             Key = "cmd.remove"
	CmdMove               Key = "cmd.move"
	CmdCreate             Key = "cmd.create"
	CmdPerms              Key = "cmd.perms"
	CmdKill               Key = "cmd.kill"
	CmdArchive            Key = "cmd.archive"
	CmdTruncate           Key = "cmd.truncate"
	CmdNetwork            Key = "cmd.network"
	CmdGoClean            Key = "cmd.go_clean"
	CmdCargoClean         Key = "cmd.cargo_clean"
	CmdDockerImagePrune   Key = "cmd.docker_image_prune"
	CmdDockerRmi          Key = "cmd.docker_rmi"
	CmdDockerBuilderPrune Key = "cmd.docker_builder_prune"
	CmdDockerRm           Key = "cmd.docker_rm"
	CmdDockerStop         Key = "cmd.docker_stop"
	CmdDockerVolumeRm     Key = "cmd.docker_volume_rm"
	CmdDockerSystemPrune  Key = "cmd.docker_system_prune"
	CmdSystemctlStop      Key = "cmd.systemctl_stop"
	CmdJournalVacuum      Key = "cmd.journal_vacuum"
	CmdGitIgnored         Key = "cmd.git_ignored"
	CmdReason             Key = "cmd.reason"
	CmdIn                 Key = "cmd.in"
	CmdWrapper            Key = "cmd.wrapper"

	ButtonHistory            Key = "button.history"
	ButtonRuns               Key = "button.runs"
	WhyHeader                Key = "why.header"
	WhyThisMessage           Key = "why.this_message"
	WhyUsage                 Key = "why.usage"
	WhyNotModule             Key = "why.not_module"
	WhyForwarded             Key = "why.forwarded"
	WhyNoRun                 Key = "why.no_run"
	WhyGone                  Key = "why.gone"
	RunCauseCron             Key = "run.cause_cron"
	RunCauseTool             Key = "run.cause_tool"
	RunCauseCell             Key = "run.cause_cell"
	RunCauseCellBy           Key = "run.cause_cell_by"
	RunCauseEvent            Key = "run.cause_event"
	RunCauseCall             Key = "run.cause_call"
	RunCauseHTTP             Key = "run.cause_http"
	RunResultOK              Key = "run.result_ok"
	RunResultFailed          Key = "run.result_failed"
	RunWrote                 Key = "run.wrote"
	RunsHeader               Key = "runs.header"
	RunsNone                 Key = "runs.none"
	ModulesUsage             Key = "modules.usage"
	ModulesNone              Key = "modules.none"
	ModulesHeader            Key = "modules.header"
	ModulesPrevious          Key = "modules.previous"
	ModulesFailed            Key = "modules.failed"
	ModulesLocalRow          Key = "modules.local_row"
	ModulesStateRunning      Key = "modules.state_running"
	ModulesStateOff          Key = "modules.state_off"
	ModulesStateCrashloop    Key = "modules.state_crashloop"
	ModulesStateStopped      Key = "modules.state_stopped"
	ModulesStateEdge         Key = "modules.state_edge"
	ModulesEdgeRunning       Key = "modules.edge_running"
	ModulesEdgeWaiting       Key = "modules.edge_waiting"
	ModulesEdgeRefused       Key = "modules.edge_refused"
	ModulesEdgeNothing       Key = "modules.edge_nothing"
	ModulesEdgeUndelivered   Key = "modules.edge_undelivered"
	ModulesUnknown           Key = "modules.unknown"
	ModulesUpdated           Key = "modules.updated"
	ModulesUpToDate          Key = "modules.up_to_date"
	ModulesRolledBack        Key = "modules.rolled_back"
	ModulesNothingToRollBack Key = "modules.nothing_to_roll_back"
	ModulesStays             Key = "modules.stays"
	ModulesCardRunning       Key = "modules.card_running"
	ModulesCardStopped       Key = "modules.card_stopped"
	ModulesCardVersions      Key = "modules.card_versions"
	ModulesCardHealthOK      Key = "modules.card_health_ok"
	ModulesCardHealthFailed  Key = "modules.card_health_failed"
	ModulesJournalRow        Key = "modules.journal_row"
	ModulesMarkActive        Key = "modules.mark_active"
	ModulesMarkPrevious      Key = "modules.mark_previous"
	ModulesMarkFailed        Key = "modules.mark_failed"
	ModulesMarkRetired       Key = "modules.mark_retired"
	ModulesCardSettings      Key = "modules.card_settings"
	ModulesCardReacts        Key = "modules.card_reacts"
	ModulesCardFeeds         Key = "modules.card_feeds"
	ModulesCardNotify        Key = "modules.card_notify"
	ModulesSettingRow        Key = "modules.setting_row"
	ModulesSettingUnset      Key = "modules.setting_unset"
	ModulesSettingDefault    Key = "modules.setting_default"
	ModulesSettingRequired   Key = "modules.setting_required"
	ModulesConfigDone        Key = "modules.config_done"
	ModulesConfigUndeclared  Key = "modules.config_undeclared"
	ModulesConfigKeys        Key = "modules.config_keys"
	ModulesConfigNoKeys      Key = "modules.config_no_keys"
	ModulesConfigWrongType   Key = "modules.config_wrong_type"
	ModulesOffDone           Key = "modules.off_done"
	ModulesOnDone            Key = "modules.on_done"
	ModulesNotStarted        Key = "modules.not_started"
	ModulesRemoved           Key = "modules.removed"
	ModulesRemovedLinks      Key = "modules.removed_links" // vars: name, links
	ModuleNotifyLimit        Key = "module.notify_limit"
	ModuleChainStopped       Key = "module.chain_stopped"
	CellsNone                Key = "cells.none"
	CellsRow                 Key = "cells.row"
	CellsNoValue             Key = "cells.no_value"
	CellsMore                Key = "cells.more"
	CellsAgeNow              Key = "cells.age_now"
	CellsAgeMin              Key = "cells.age_min"
	CellsAgeHour             Key = "cells.age_hour"
	CellsAgeDay              Key = "cells.age_day"
	CellsHistoryHead         Key = "cells.history_head"
	CellsHistoryRow          Key = "cells.history_row"
	CellsHistoryNone         Key = "cells.history_none"
	CellsGone                Key = "cells.gone"

	JobCardRunning     Key = "job.card_running"
	JobCardCount       Key = "job.card_count"
	JobCardQueued      Key = "job.card_queued"
	JobCardDone        Key = "job.card_done"
	JobCardFailed      Key = "job.card_failed"
	JobCardInterrupted Key = "job.card_interrupted"
	JobKindBuild       Key = "job.kind_build"
	JobKindBuildNth    Key = "job.kind_build_nth"
	JobKindCommand     Key = "job.kind_command"

	HintModuleAuthFailed Key = "hint.module_auth_failed"
	HintModuleStale      Key = "hint.module_stale"
	HintModuleFormat     Key = "hint.module_format"
	HintModuleException  Key = "hint.module_exception"
	HintModuleTimeout    Key = "hint.module_timeout"
	HintModuleTrap       Key = "hint.module_trap"
	HintModuleNotRunning Key = "hint.module_not_running"

	MemoryUpdated         Key = "memory.updated"
	MemoryLineAdded       Key = "memory.line_added"
	MemoryLineReplaced    Key = "memory.line_replaced"
	MemoryLineRemoved     Key = "memory.line_removed"
	MemoryLineDropped     Key = "memory.line_dropped"
	MemoryUndoConflict    Key = "memory.undo_conflict"
	MemoryEmpty           Key = "memory.empty"
	MemoryTopicsHead      Key = "memory.topics_head"
	MemoryTopicRow        Key = "memory.topic_row"
	MemoryNoTopic         Key = "memory.no_topic"
	MemoryPrivateOnly     Key = "memory.private_only"
	MemoryFromOutside     Key = "memory.from_outside"
	MemoryByHand          Key = "memory.by_hand"
	ButtonUndo            Key = "button.undo"
	LineUndone            Key = "line.undone"
	MemoryProposal        Key = "memory.proposal"
	MemoryLineForget      Key = "memory.line_forget"
	MemoryHeld            Key = "memory.held"
	ButtonRemember        Key = "button.remember"
	ButtonSkip            Key = "button.skip"
	LineSkipped           Key = "line.skipped"
	StatusMemoryReviewOff Key = "status.memory_review_off"

	// What the node tells its admins about an update, sent by the update watch (R-UPD-4, R-UPD-5).
	UpdateAlertFailed       Key = "update.alert_failed"     // vars: from, to, db: it did not start twice
	UpdateAlertUnverified   Key = "update.alert_unverified" // vars: from, to, db: its files no longer verify
	UpdateAlertNoExec       Key = "update.alert_noexec"     // vars: from, to, db: the data volume does not run programs
	UpdateAlertExec         Key = "update.alert_exec"       // vars: from, to, db: it could not be executed
	UpdateDBRestored        Key = "update.db_restored"      // inside an alert: the database went back to before the update
	UpdateDBKept            Key = "update.db_kept"          // inside an alert: the database was not changed
	UpdateImageVersion      Key = "update.image_version"    // inside an alert: the version the image or package holds
	UpdateConfirmed         Key = "update.confirmed"        // vars: version
	UpdateStarted           Key = "update.started"          // vars: version, minutes: a new version started, to be confirmed
	UpdateAvailable         Key = "update.available"        // vars: version, date, page: the daily check found a newer release
	UpdatePrivateOnly       Key = "update.private_only"
	UpdateUsage             Key = "update.usage"
	UpdateUnavailable       Key = "update.unavailable"
	UpdateStatus            Key = "update.status"      // vars: running, newest, date, page
	UpdateStatusNext        Key = "update.status_next" // vars: newest
	UpdateStatusCurrent     Key = "update.status_current"
	UpdateNoImage           Key = "update.no_image"
	UpdateDevBuild          Key = "update.dev_build" // vars: image
	UpdateDevBuildAny       Key = "update.dev_build_any"
	UpdateInstalling        Key = "update.installing"            // vars: version
	UpdateInstalled         Key = "update.installed"             // vars: version
	UpdateRolledBack        Key = "update.rolled_back"           // vars: version
	UpdateButtonInstall     Key = "update.button_install"        // label, vars: version
	UpdateButtonRollback    Key = "update.button_rollback"       // label, vars: version
	UpdateButtonRollbackImg Key = "update.button_rollback_image" // label
	UpdateButtonStale       Key = "update.button_stale"
	UpdateAlreadyInstalled  Key = "update.already_installed" // vars: version
	UpdateRefusedBadVersion Key = "update.refused_bad_version"
	UpdateRefusedNotNewer   Key = "update.refused_not_newer"
	UpdateRefusedNotFound   Key = "update.refused_not_found"
	UpdateRefusedNoPlatform Key = "update.refused_no_platform"
	UpdateRefusedIncomplete Key = "update.refused_incomplete"
	UpdateRefusedSignature  Key = "update.refused_signature"
	UpdateRefusedSums       Key = "update.refused_sums"
	UpdateRefusedMismatch   Key = "update.refused_mismatch"
	UpdateRefusedManifest   Key = "update.refused_manifest"
	UpdateRefusedEnv        Key = "update.refused_env"
	UpdateRefusedBusy       Key = "update.refused_busy"
	UpdateRefusedFetch      Key = "update.refused_fetch"
	UpdateRefusedNoPrevious Key = "update.refused_no_previous"
	UpdateRefusedState      Key = "update.refused_state"
	UpdateRefusedOther      Key = "update.refused_other"

	BackupAlertFailed       Key = "backup.alert_failed"     // vars: reason, last: the daily backup failed
	BackupAlertRecovered    Key = "backup.alert_recovered"  // vars: size, time: the backup works again
	BackupLastNone          Key = "backup.last_none"        // inside an alert: there is no good copy
	BackupReasonRefused     Key = "backup.reason_refused"   // vars: detail: the bucket answered with an error
	BackupReasonNetwork     Key = "backup.reason_network"   // the bucket could not be reached
	BackupReasonChanged     Key = "backup.reason_changed"   // a file changed while it was copied
	BackupReasonIntegrity   Key = "backup.reason_integrity" // the database copy is damaged
	BackupReasonOther       Key = "backup.reason_other"     // any other reason
	StatusBackupOff         Key = "status.backup_off"
	StatusBackupOK          Key = "status.backup_ok" // vars: time, size
	StatusBackupWaiting     Key = "status.backup_waiting"
	StatusBackupStale       Key = "status.backup_stale" // vars: time, size: no new copy for long
	StatusBackupNone        Key = "status.backup_none"
	StatusReasonBackupStale Key = "status.reason_backup_stale" // span: for
	StatusBackupFailed      Key = "status.backup_failed"

	// The message bus in /status (R-BUS-1, R-OBS-11).
	StatusBusOK       Key = "status.bus_ok"       // vars: mode, size
	StatusBusDown     Key = "status.bus_down"     // vars: mode: the connection to the server is lost
	StatusBusNoChain  Key = "status.bus_no_chain" // vars: mode: the secret store is locked, so the bus runs without its trust chain
	StatusEdgeOK      Key = "status.edge_ok"      // vars: name, modules, waiting: an edge heard on time
	StatusEdgeSilent  Key = "status.edge_silent"  // vars: name, held: no snapshot for three intervals
	StatusEdgeUnheard Key = "status.edge_unheard" // vars: name: sourced, not heard since the node started

	// The remote MCP servers' cards, /mcp and /status (R-MCP-3, R-MCP-9, R-MCP-10).
	AskMCPConnect             Key = "ask.mcp_connect"     // vars: server, url
	AskMCPSignIn              Key = "ask.mcp_signin"      // vars: server, host, signin_host, link
	AskMCPSignInLink          Key = "ask.mcp_signin_link" // the sign-in link's text
	AskMCPPageWarning         Key = "ask.mcp_page_warning"
	AskMCPToolsCard           Key = "ask.mcp_tools_card" // vars: server, host, n
	AskMCPToolsNone           Key = "ask.mcp_tools_none"
	AskMCPToolsAuto           Key = "ask.mcp_tools_auto"
	AskMCPToolsConfirm        Key = "ask.mcp_tools_confirm"
	AskMCPToolsForbidden      Key = "ask.mcp_tools_forbidden"
	AskMCPToolsAutoWrite      Key = "ask.mcp_tools_auto_write"
	AskMCPToolsForbiddenCount Key = "ask.mcp_tools_forbidden_count" // vars: n
	AskMCPToolsSkipped        Key = "ask.mcp_tools_skipped"
	AskMCPToolsMore           Key = "ask.mcp_tools_more" // vars: n
	AskMCPRule                Key = "ask.mcp_rule"       // vars: tool, from, to
	AskMCPRules               Key = "ask.mcp_rules"      // vars: server, n
	AskMCPRuleLine            Key = "ask.mcp_rule_line"  // vars: tool, from, to
	AskMCPRuleGroup           Key = "ask.mcp_rule_group" // vars: n, from, to
	AskMCPPolicyAuto          Key = "ask.mcp_policy_auto"
	AskMCPPolicyConfirm       Key = "ask.mcp_policy_confirm"
	AskMCPPolicyForbidden     Key = "ask.mcp_policy_forbidden"
	ButtonMCPConnect          Key = "button.mcp_connect"
	ButtonMCPAskAll           Key = "button.mcp_ask_all"
	ButtonMCPCancel           Key = "button.mcp_cancel"
	ButtonMCPDisconnect       Key = "button.mcp_disconnect" // vars: server
	MCPConnected              Key = "mcp.connected"         // vars: server, n, auto, ask
	MCPCancelled              Key = "mcp.cancelled"         // vars: server
	MCPCardStale              Key = "mcp.card_stale"        // a press on a card made before the server\'s line changed
	MCPDisconnected           Key = "mcp.disconnected"      // vars: server
	MCPListFailed             Key = "mcp.list_failed"       // vars: server, kind
	MCPListNone               Key = "mcp.list_none"
	MCPListHead               Key = "mcp.list_head"
	MCPListRow                Key = "mcp.list_row"       // vars: server, state, n
	MCPListSignedIn           Key = "mcp.list_signed_in" // vars: time
	MCPPrivateOnly            Key = "mcp.private_only"
	MCPStateConnecting        Key = "mcp.state_connecting"
	MCPStateConnected         Key = "mcp.state_connected"
	MCPStateUnreachable       Key = "mcp.state_unreachable"
	MCPStateToolsChanged      Key = "mcp.state_tools_changed"
	MCPStateUnauthorized      Key = "mcp.state_unauthorized"
	MCPStateLocked            Key = "mcp.state_locked"
	StatusMCPConnecting       Key = "status.mcp_connecting"    // vars: server
	StatusMCPUnreachable      Key = "status.mcp_unreachable"   // vars: server
	StatusMCPToolsChanged     Key = "status.mcp_tools_changed" // vars: server
	StatusMCPUnauthorized     Key = "status.mcp_unauthorized"  // vars: server
	StatusMCPLocked           Key = "status.mcp_locked"        // vars: server
	ButtonEdgeApprove         Key = "button.edge_approve"
	ButtonEdgeCancel          Key = "button.edge_cancel"
	ButtonEdgeRevoke          Key = "button.edge_revoke"   // vars: name
	EdgeCard                  Key = "edge.card"            // vars: name, publish, subscribe
	EdgeNone                  Key = "edge.none"            // an empty list of permissions
	EdgeApproved              Key = "edge.approved"        // vars: name, time, command
	EdgeAddressUnknown        Key = "edge.address_unknown" // stands for the node's address in the command
	EdgeCancelled             Key = "edge.cancelled"       // vars: name
	EdgeStale                 Key = "edge.stale"           // vars: name
	EdgeTaken                 Key = "edge.taken"           // vars: name
	EdgeBusy                  Key = "edge.busy"
	EdgeListNone              Key = "edge.list_none"
	EdgeListHead              Key = "edge.list_head"
	EdgeListRow               Key = "edge.list_row"  // vars: name, time
	EdgeListNote              Key = "edge.list_note" // permissions are not shown
	EdgePrivateOnly           Key = "edge.private_only"
	EdgeRevoked               Key = "edge.revoked"       // vars: name
	EdgeGone                  Key = "edge.gone"          // vars: name
	EdgeRevokeFailed          Key = "edge.revoke_failed" // vars: name
	EdgeJoined                Key = "edge.joined"        // vars: name, time
)

var allKeys = []Key{
	ButtonApprove, ButtonDeny,
	ToastApproved, ToastDeclined, ToastAccepted, ToastNotYours, ToastNoButton, ToastUsed, ToastExpired, ToastNotAllowed, ToastReplaced,
	LineRunning, LineApproved, LineApprovedFailed, LineDeclined, LineUsed, LineExpired, LineReplaced,
	ReplyNoModel, ReplyStepLimit, ReplyExpired, ReplyDailyLimit, ReplyGiveUp, ReplyNewSession, ReplyNoSessions,
	ReplyNoSuchResume, ReplyResumed, ReplySessionRow, ReplyNoTasks, ReplyNotAvailable, ReplyCommandsOn, ReplyCommandsOff, ReplyCommandsUsage, ReplyCommandLate, ReplyBuildLate, ReplyRestarting, ReplyBack, ReplyBackApplied, ReplyIntakeExpired, ReplyIntakeUnknown, ReplyIntakeVersion, ReplyIntakeMalformed, ReplyIntakeFailed, ReplyIntakeAlone, SecretCardNode, SecretCardModule, SecretCardModuleNoHosts, SecretCardServer, SecretCardWhy, SecretCardTainted, SecretCardBody, SecretEnter, SecretStopped, SecretLineSet, SecretLineFailed, SecretLineLocked, SecretLineExpired, SecretWhoseNode, SecretWhoseModule, SecretWhoseServer, StatusCommandsOff,
	AskDestinationSet, AskConfigPropose, AskConfigTainted, AskConfigKept, AskConfigDefault, ReplyConfigWritten, ButtonConfigRestart, AskDestinationRemove, AskModuleNotifyTo, AskGroupDisconnect, PlacePrivate, PlaceGroup, PlaceTopic, PlaceTopicNumber, PlaceTopicGiven, PlaceGeneral,
	ButtonAllow, ButtonRefuse, GroupRequestCard, GroupNotConnected, GroupAllowed, GroupRefused,
	ButtonDetails, ButtonDelete, ButtonRunNow, AskLanguage, AskLanguageNoFile, AskRunTool, AskSendMessage, AskCreateReminder, AskCreateTask, AskDeleteReminder, AskDeleteTask, AskRunNow, AskOpenPage, AskSwitchCell, AskRemember, AskForgetFact, AskForgetSlug, AskForgetTopic, AskPersona, AskSkillScript, AskHostExec, AskPlan, AskModuleInstall, AskModuleNetwork, AskModuleSecrets, AskModuleCreates, AskModuleTakes, AskModuleReads, AskModuleReacts, AskModuleNotify, AskModuleEmits, AskModuleAccepts, AskModuleServes, AskModulePages, AskModuleCalls, AskModuleRuns, AskModuleTool, AskModuleAbout, AskModuleLimits, AskModuleNew, AskModuleNewNetwork, AskModuleNewSecrets, AskModuleNewCells, AskModuleNewReads, AskModuleNewReacts, AskModuleNewNotify, AskModuleNewEmits, AskModuleNewAccepts, AskModuleNewServes, AskModuleNewPages, AskModuleNewCalls, AskModuleNewRuns, AskModuleNewMemory, AskModuleNewTimeout, AskModuleTrial, AskModuleTrialNone, AskModuleTrialExample, AskModuleTrialMessage, AskModuleTrialCell, AskModuleTrialEvent, AskModuleTrialNothing, AskModuleTrialNow, AskModuleTrialMore, AskModuleTrialNotRun, AskModuleTrialFailed, AskModuleTrialError, AskModuleOutside, AskModuleRequest, AskModuleLinks, AskModuleLink, SchedSummaryReminder, SchedSummaryTask, SchedSummaryCommands, SchedEntry, SchedDetails, SchedOnce, SchedEvery, SchedEveryNext, SchedKindReminder, SchedKindTask, SchedSystem, SchedKindSystem, SchedKindModule, SchedAuditText, NoticeMachine, ReportAuditClean, ReportAuditFlagged, SchedGone, SchedDeleted, SchedNoDelete, SchedNotTask, SchedThisChat, SchedNone, SchedStarted, SchedReminderSet, SchedReminderOff, SchedOff, ButtonDone,
	StatusNoAnswer, StatusVerdictOK, StatusVerdictWarn, StatusVerdictFail, StatusSectionNode, StatusSectionModels, StatusSectionDelivery, StatusSectionProblems, StatusSince, StatusSession, StatusSessionFallback, StatusSessionNone, StatusDay, StatusReasonLimitNear, StatusReasonLimitReached, StatusVersion, StatusVersionModified, StatusVersionUnknown, StatusUptime, StatusMemory, StatusMemoryNoLimit, StatusReasonMemoryHigh, StatusFiles, StatusFilesNoLogs, StatusModelAnswered, StatusModelSilent, StatusReasonModelFailed, StatusTurnsFailed, StatusTelegram, StatusTelegramWaiting, StatusReasonPollStale, StatusReasonConflict, StatusReasonUnauthorized, StatusTelegramWebhook, StatusTelegramWebhookQuiet, StatusReasonWebhookPending, StatusReasonWebhookFallback, StatusReasonWebhookFailing, StatusReasonWebhookTaken, StatusQueues, StatusReasonQueueOld, StatusReasonUndelivered, StatusSkills, StatusSkillRefused, StatusReasonSkillShape, StatusReasonSkillRead, StatusReasonSkillFrontmatter, StatusReasonSkillScript, StatusReasonSkillBuiltin, SecretsPrivateOnly, SecretsEmpty, SecretsHead, SecretsScopeNode, SecretsRow, SecretsLocked, SecretsRemoved, SecretsNotFound, SecretsReserved, SecretsResetAsk, SecretsResetDone, SecretsUsage, StatusSecretsLocked, StatusReasonSecretsKey, StatusModuleFallback, StatusReasonModuleFailed, StatusModulesLocal, StatusModuleOff, StatusReasonModuleCrashloop, StatusModuleStopped, StatusModulesUnread, StatusReasonModuleInit, StatusReasonModuleCells, StatusReasonModuleLoad, StatusReasonModuleNoAdmin, StatusJobs, StatusJobsIdle, StatusJobsNone, StatusSectionJobs, StatusJobBuild, StatusJobCommand, StatusJobBuildQueued, StatusJobCommandQueued, StatusReasonSchedLag, StatusReasonSchedFailing, StatusReasonSchedStalled, StatusUnitDay, StatusUnitHour, StatusUnitMin, StatusUnitSec, StatusSandboxReady, StatusSandboxOff, StatusReasonSandboxNotLinux, StatusReasonSandboxRoot, StatusReasonSandboxLandlock, StatusReasonSandboxDumpable, StatusReasonSandboxSelfTest,
	ButtonHistory, ButtonRuns, WhyHeader, WhyThisMessage, WhyUsage, WhyNotModule, WhyForwarded, WhyNoRun, WhyGone, RunCauseCron, RunCauseTool, RunCauseCell, RunCauseCellBy, RunCauseEvent, RunCauseCall, RunCauseHTTP, RunResultOK, RunResultFailed, RunWrote, RunsHeader, RunsNone, ModulesUsage, ModulesNone, ModulesHeader, ModulesPrevious, ModulesFailed, ModulesLocalRow, ModulesStateRunning, ModulesStateOff, ModulesStateCrashloop, ModulesStateStopped, ModulesStateEdge, ModulesEdgeRunning, ModulesEdgeWaiting, ModulesEdgeRefused, ModulesEdgeNothing, ModulesEdgeUndelivered, ModulesUnknown, ModulesUpdated, ModulesUpToDate, ModulesRolledBack, ModulesNothingToRollBack, ModulesStays, ModulesCardRunning, ModulesCardStopped, ModulesCardVersions, ModulesCardHealthOK, ModulesCardHealthFailed, ModulesJournalRow, ModulesMarkActive, ModulesMarkPrevious, ModulesMarkFailed, ModulesMarkRetired, ModulesCardSettings, ModulesCardReacts, ModulesCardFeeds, ModulesCardNotify, ModulesSettingRow, ModulesSettingUnset, ModulesSettingDefault, ModulesSettingRequired, ModulesConfigDone, ModulesConfigUndeclared, ModulesConfigKeys, ModulesConfigNoKeys, ModulesConfigWrongType, ModulesOffDone, ModulesOnDone, ModulesNotStarted, ModulesRemoved, ModuleNotifyLimit, ModuleChainStopped, CellsNone, CellsRow, CellsNoValue, CellsMore, CellsAgeNow, CellsAgeMin, CellsAgeHour, CellsAgeDay, CellsHistoryHead, CellsHistoryRow, CellsHistoryNone, CellsGone,
	JobCardRunning, JobCardCount, JobCardQueued, JobCardDone, JobCardFailed, JobCardInterrupted, JobKindBuild, JobKindBuildNth, JobKindCommand,
	HintModuleAuthFailed, HintModuleStale, HintModuleFormat, HintModuleException, HintModuleTimeout, HintModuleTrap, HintModuleNotRunning,
	MemoryUpdated, MemoryLineAdded, MemoryLineReplaced, MemoryLineRemoved, MemoryLineDropped, MemoryUndoConflict, MemoryEmpty, MemoryTopicsHead, MemoryTopicRow, MemoryNoTopic, MemoryPrivateOnly, MemoryFromOutside, MemoryByHand, ButtonUndo, LineUndone, MemoryProposal, MemoryLineForget, MemoryHeld, ButtonRemember, ButtonSkip, LineSkipped, StatusMemoryReviewOff,
	CmdRead, CmdContent, CmdUnknown, CmdPath, CmdScript, CmdRoot, CmdOutside, CmdAllEnv, CmdScriptFile, CmdDockerOther, CmdRemove, CmdMove, CmdCreate, CmdPerms, CmdKill, CmdArchive, CmdTruncate, CmdNetwork, CmdGoClean, CmdCargoClean, CmdDockerImagePrune, CmdDockerRmi, CmdDockerBuilderPrune, CmdDockerRm, CmdDockerStop, CmdDockerVolumeRm, CmdDockerSystemPrune, CmdSystemctlStop, CmdJournalVacuum, CmdGitIgnored, CmdReason, CmdIn, CmdWrapper,
	UpdateAlertFailed, UpdateAlertUnverified, UpdateAlertNoExec, UpdateAlertExec, UpdateDBRestored, UpdateDBKept, UpdateImageVersion, UpdateConfirmed, UpdateStarted, UpdateAvailable,
	UpdatePrivateOnly, UpdateUsage, UpdateUnavailable, UpdateStatus, UpdateStatusNext, UpdateStatusCurrent, UpdateNoImage, UpdateDevBuild, UpdateDevBuildAny, UpdateInstalling, UpdateInstalled, UpdateRolledBack, UpdateButtonInstall, UpdateButtonRollback, UpdateButtonRollbackImg, UpdateButtonStale, UpdateAlreadyInstalled, UpdateRefusedBadVersion, UpdateRefusedNotNewer, UpdateRefusedNotFound, UpdateRefusedNoPlatform, UpdateRefusedIncomplete, UpdateRefusedSignature, UpdateRefusedSums, UpdateRefusedMismatch, UpdateRefusedManifest, UpdateRefusedEnv, UpdateRefusedBusy, UpdateRefusedFetch, UpdateRefusedNoPrevious, UpdateRefusedState, UpdateRefusedOther,
	BackupAlertFailed, BackupAlertRecovered, BackupLastNone, BackupReasonRefused, BackupReasonNetwork, BackupReasonChanged, BackupReasonIntegrity, BackupReasonOther, StatusBackupOff, StatusBackupOK, StatusBackupWaiting, StatusBackupStale, StatusBackupNone, StatusReasonBackupStale, StatusBackupFailed, StatusBusOK, StatusBusDown, StatusBusNoChain, StatusEdgeOK, StatusEdgeSilent, StatusEdgeUnheard,
	AskMCPConnect, AskMCPSignIn, AskMCPSignInLink, AskMCPPageWarning, AskMCPToolsCard, AskMCPToolsNone, AskMCPToolsAuto, AskMCPToolsConfirm, AskMCPToolsForbidden, AskMCPToolsSkipped, AskMCPToolsMore, AskMCPToolsAutoWrite, AskMCPToolsForbiddenCount, AskMCPRule, AskMCPRules, AskMCPRuleLine, AskMCPRuleGroup, AskMCPPolicyAuto, AskMCPPolicyConfirm, AskMCPPolicyForbidden, ButtonMCPConnect, ButtonMCPAskAll, ButtonMCPCancel, ButtonMCPDisconnect, MCPConnected, MCPCancelled, MCPCardStale, MCPDisconnected, MCPListFailed, MCPListNone, MCPListHead, MCPListRow, MCPListSignedIn, MCPPrivateOnly, MCPStateConnecting, MCPStateConnected, MCPStateUnreachable, MCPStateToolsChanged, MCPStateUnauthorized, MCPStateLocked, StatusMCPConnecting, StatusMCPUnreachable, StatusMCPToolsChanged, StatusMCPUnauthorized, StatusMCPLocked,
	ButtonEdgeApprove, ButtonEdgeCancel, ButtonEdgeRevoke, EdgeCard, EdgeNone, EdgeApproved, EdgeAddressUnknown, EdgeCancelled, EdgeStale, EdgeTaken, EdgeBusy, EdgeListNone, EdgeListHead, EdgeListRow, EdgeListNote, EdgePrivateOnly, EdgeRevoked, EdgeGone, EdgeRevokeFailed, EdgeJoined,
	AskLinkAdd, AskLinkRemove, ModulesRemovedLinks, StatusLinkBroken, StatusReasonLinkEnd, StatusReasonLinkKind, StatusReasonLinkShape, StatusReasonLinkTaken, StatusReasonLinkLoop, StatusReasonLinkDeep,
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
	case strings.HasPrefix(string(k), "sched."), strings.HasPrefix(string(k), "cmd."):
		return limitInQuestion
	default:
		return limitMessage
	}
}

// IsApproval reports whether a person reads k's text to decide an approval. A machine translation
// missing any of these is refused whole (R-LANG-5).
func IsApproval(k Key) bool {
	switch k {
	case ButtonApprove, ButtonDeny, ButtonDelete, ButtonRunNow, ButtonDone, ButtonAllow, ButtonRefuse, GroupRequestCard, ButtonMCPConnect, ButtonMCPAskAll, ButtonMCPCancel, ButtonEdgeApprove, ButtonEdgeCancel, EdgeCard, MemoryProposal, ButtonRemember, ButtonSkip, SchedSummaryReminder, SchedSummaryTask, SchedSummaryCommands,
		SchedOnce, SchedEvery, SchedEveryNext, SchedThisChat, SchedNone:
		return true
	default:
		return strings.HasPrefix(string(k), "ask.") || strings.HasPrefix(string(k), "cmd.")
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

// LabelWith is Catalog.Label in s's language, with values.
func (s Speaker) LabelWith(k Key, v Vars) string { return s.catalog().Label(s.Lang, k, v) }

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
