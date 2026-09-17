package muse

import (
	"encoding/json"
	"strings"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// Stage 2 of the adapter (issue #1960): the real session.jsonl parser.
// Records are payload_type-discriminated envelopes; the interesting ones
// wrap a "runtime.session" envelope whose own payload.kind ("run", "task",
// "approval") further discriminates, and payload.event.kind discriminates a
// third level for "run" and "approval". All shapes below are quoted from
// live captures under ~/.local/share/muse/sessions/ on this machine
// (muse-format-spec.md), cross-checked directly against the raw JSONL a
// second time while writing this file — every payload_type/event.kind named
// here was read off a real record, not inferred from the spec doc alone.
//
// # The retained_frame structural trap
//
// Most lines are one flat record. Some lines are instead an OUTER FRAME that
// batches several inner records, each independently JSON-encoded a second
// time as a "record_json" string:
//
//	{"retained_frame":"session_permission_transaction","frame_schema_version":1,
//	 "outer_log_ordinal":1,"transaction_id":"...",
//	 "children":[{"child_index":0,"record_json":"{...}"},{"child_index":1,"record_json":"{...}"}]}
//
// agent.LineParser is a ONE-EVENT-PER-LINE contract
// (core/domain/agent/file_parser.go) and the tailer applies exactly one
// ParsedEvent per parseTranscriptLine call (core/pkg/tailer/tailer.go), so a
// multi-record line has to fold into a single verdict. ParseLine does this by
// decoding each child and routing it through decodeAndRoute — the SAME
// dispatcher an ordinary top-level line uses — then merging the per-child
// results with mergeChildEvents. This means a retained_frame's children are
// never silently dropped: whatever decodeAndRoute would do with a child's
// record as a top-level line is exactly what happens to it here too.
//
// Verified against every retained_frame in a local 16-session corpus (43
// occurrences, including nested subagent copies, via a recursive scan of
// every session.jsonl under ~/.local/share/muse/sessions): the frame is
// ALWAYS named "session_permission_transaction", ALWAYS carries exactly 2
// children in the same order — runtime.session.permission_format_declared
// then runtime.session.permission_profile_committed — and ALWAYS sits at
// outer_log_ordinal 1 (the very first line of the stream). Both inner
// payload types are permission-PROFILE bookkeeping (which filesystem/network
// rules and approval mode apply to the session), carry no run/task/approval
// content, and are unhandled by route() below (its default arm), so in
// every observed case the merged result is Skip=true with zero deltas. The
// code does not special-case that outcome, though — a future muse release
// that ever nests something else inside a retained_frame (or uses a
// different frame name this corpus never produced) still gets parsed.
//
// # Tool-call tracking: assistant_tool_calls_committed / tool_result_batch_committed
//
// The task/tool_batch.effect.* families (kind:"task" proposed/accepted/
// started/side_effect_intent/status/output/completed/..., plus the top-level
// tool_batch.effect.started/terminal payload_types) describe the SAME tool
// calls as a lower-level "effect execution" view, correlated by the identical
// call_id. Direct inspection of real captures found a materially SIMPLER,
// equally authoritative pairing living directly under kind:"run":
// assistant_tool_calls_committed (event.tool_calls[].call_id/name — opens)
// and tool_result_batch_committed (event.results[].tool_call_id — closes).
// This parser uses ONLY that pair for ToolUses/ToolResultIDs. Also wiring
// task/tool_batch.effect.* would track the identical call_id twice from two
// parallel views of one lifecycle — harmless in itself (the tailer's
// openToolCalls is an idempotent id-keyed map) but pure duplication with no
// additional signal, since tool_batch.effect.terminal's outcome.kind never
// showed a genuine "failed" example in this corpus (489 "completed", 1
// "cancelled" — see IsError below) and side_effect_intent's policy_decision
// is not needed either: kind:"approval" already reports blocking directly.
//
// FOLLOW-UP (issue #1960's 3-3_background-process fix): the "pure
// duplication, no additional signal" conclusion above was reached by scanning
// the OUTER outcome.kind field only ("completed"/"cancelled") and is correct
// for THAT field. It does not extend to outcome.task_completion.kind, a
// DIFFERENT field nested one level deeper inside tool_batch.effect.terminal's
// same record, which the scan above never examined. A corpus-wide scan (527
// tool_batch.effect.terminal records under ~/.local/share/muse/sessions on
// this machine) found task_completion.kind is "terminal" (399, ordinary
// synchronous close) or "complete" (126, a simpler synchronous-close shape
// some tool kinds use) in every case but one, where it is "pending" — the
// tool call returned but its task_id keeps running as a detached background
// process, muse's own equivalent of Claude Code's Bash tool_result "Command
// running in background with ID: …" text. parseToolBatchEffectTerminal reads
// ONLY this one nested field for exactly that "pending" signal — a narrow,
// additive carve-out, not a re-wiring of the family this doc still argues
// against above.
//
// # Waiting: kind:"approval" only, not approval_wait.effect.*
//
// approval_wait.effect.started/terminal pair 1:1 with kind:"approval"
// "requested"/"decision_applied" on the identical pending_action_id in every
// example found (format-spec §5) — a second, lower-level view of the same
// open/close, the same relationship as the tool-call pair above. Only the
// "runtime.session"/"approval" family is used here, since "requested" alone
// (unlike approval_wait.effect.started) also carries tool_name for context.
//
// # requested's presentation_phase: not every "requested" is the user's wait (issue #1978)
//
// "requested" carries a presentation_phase field this parser used to ignore
// entirely, opening a user-visible permission prompt unconditionally. A
// corpus scan of every top-level session.jsonl under
// ~/.local/share/muse/sessions on this machine (148 files, subagent nested
// copies excluded — those are the SAME approval records folded into the
// parent session, see the "Resolved (issue #1960 stage 3)" section below)
// found presentation_phase takes exactly two values: "automated_reviewing"
// (68 occurrences) — muse's own ":auto-review" LLM judge decides first, no
// user involved yet — and "human_pending" (9 occurrences) — the user must
// decide right now, the case this parser always assumed. Reading the
// reporter's own already-on-disk session (no live drive) measured the
// practical cost from its recorded_at stamps:
// requested{automated_reviewing} at seq 1157, automated_review_started one
// line later, then 18.73s before automated_review_completed{status:
// escalated} — 18.73s the LLM judge was still deciding, wrongly reported as
// waiting, followed by a genuine 22.16s of the user's own wait before
// decision_applied at seq 1269. parseApprovalRequested now reads
// presentation_phase: "automated_reviewing" opens nothing (the judge is
// deciding, not the user); "human_pending" opens exactly as before; a
// missing or any future unrecognized value also opens, fail-safe — a wait
// this parser cannot classify is shown as waiting rather than hidden.
//
// automated_review_completed carries the judge's own verdict and is no
// longer unconditional bookkeeping either: parseApprovalReviewCompleted
// re-opens the prompt when the review did NOT cleanly auto-approve — any of
// status != "approved", timed_out true, or a present, non-null failure. A
// clean auto-approval (status "approved", timed_out false, failure
// absent/null) stays Skip=true, exactly as every automated_review_completed
// record was treated before this fix. automated_review_started and
// stage_requirement_resolved remain pure bookkeeping either way — see
// parseApprovalEvent's default branch.
//
// # Why presentation_phase is an identity, not a heuristic
//
// The same corpus scan pairs each "requested" with its own
// decision_applied.decision_source.kind — muse's own record of WHO decided.
// The two agree:
//
//	requested.presentation_phase   decision_source.kind    n
//	automated_reviewing            llm_judge               62
//	human_pending                  human_approval           9
//	automated_reviewing            human_approval           4
//	automated_reviewing            (never closed in-file)   2
//
// "human_pending" resolved to a human in 9 of 9 and never once to the judge,
// so opening on it is right in every observed case. The 4 rows where an
// "automated_reviewing" request did reach a human are the ones the re-open
// branch above exists for — and it covers all 4: each emits an
// automated_review_completed whose outcome is not a clean auto-approval
// (three status "escalated", one status "timed_out" carrying timed_out true
// AND a non-null failure). So status is not always "escalated" when a human
// is needed, which is why the check is status != "approved" rather than an
// equality test against "escalated", and why timed_out and failure are
// checked at all rather than assumed redundant.
//
// Net effect measured over the same 148 files: 67 windows totalling 865.3s
// (median 8.19s, max 90.01s) stop being reported as waiting, every one of
// them longer than services.activityDebounceWindow (2s) and therefore
// certainly visible before this fix; the 9 genuine human waits are
// untouched; no observed user prompt is hidden.
//
// # Resolved (issue #1960 stage 3): stage 2's original worry here read
//
// This section used to state, correctly at the time, that every single
// kind:"approval" record found in this corpus (all 11 occurrences) lived in
// a file with ZERO run/task content — the "top-level approval-only shadow"
// adapter.go documents — and that sessionIDFromPath's suppression of that
// shadow (as it worked then) meant the approval mapping below, though
// correctly implemented, never actually reached the daemon's watcher. That
// was a real, measured gap, not a false alarm: direct comparison confirmed
// the nested <parent>/subagent/<id>/session.jsonl copy carries none of those
// 11 approval records either — the two files are disjoint, not duplicates.
// sessionIDFromPath and parentSessionIDFromPath (adapter.go) were narrowed
// to stop discarding the shadow and instead fold it into the SAME session
// the nested copy already establishes, so the approval mapping below now
// does reach the watcher — see adapter.go's doc for the fix and the ordering
// evidence that makes it safe. Separately, a live-driven probe on this
// machine confirmed a session's OWN top-level session.jsonl (never anyone's
// subagent, so never shadowed) carries its approval events directly and
// unconditionally — a real tool-approval prompt, triggered live, produced
// requested/automated_review_started/automated_review_completed/
// decision_applied inline in that session's own file, no shadow involved.
// So this path was never broken for a plain top-level session; the gap was
// specific to a SUBAGENT's own approval-wait, and only while the shadow was
// suppressed.
//
// # Token accounting: model_completed only, not goal_usage_attribution
//
// kind:"run" carries token usage twice per LLM call: event.kind
// "model_completed" (usage + the calling model) and "goal_usage_attribution"
// (the SAME usage re-emitted with ownership/attribution metadata, e.g. main
// vs. subagent). Direct comparison of paired real records (same run_id,
// adjacent sequence numbers) confirmed the usage numbers are byte-identical
// between the two events every time checked. goal_usage_attribution also
// never carries a model id, so it can't stand alone as a pricing source
// either way. This parser accumulates ONLY model_completed and marks
// goal_usage_attribution Skip=true with no Contribution, so the two are never
// both counted.
type Parser struct {
	// todos reconciles muse's write_todos snapshots (event.kind
	// "todo_snapshot_updated") into task-progress deltas — the same
	// full-snapshot-replace shape and reconciler opencode's todowrite and
	// gemini-cli's write_todos already use. See parseTodoSnapshotUpdated.
	todos tailer.TodoReconciler
	// lastErrorClass is the error_class from the most recent
	// run_fatal_error_classified event not yet consumed by a
	// terminal:"failed" event in the SAME run. Verified live: a captured
	// run_fatal_error_classified{"error_class":"config_error"} sits exactly
	// one line before that run's own terminal{"terminal":"failed",
	// "reason":"invalid run configuration: provider does not support base
	// instructions"} in four separate subagent transcripts. Not every
	// failure has one, though — the corpus's other real failure (terminal
	// reason "model stream idle timeout after 30000ms") is preceded by
	// task{"kind":"timed_out"}, no run_fatal_error_classified at all — so
	// parseRunTerminal falls back to a generic class when this is empty.
	// Reset on "started" too, so a stale class from an earlier run in the
	// same multi-turn session file can never attach to a later run's
	// failure.
	lastErrorClass string
}

// payload_type values this parser reads directly (top-level line field, or
// an inner record's own field once unwrapped from a retained_frame).
const (
	payloadTypeSession                   = "runtime.session"
	payloadTypeMetadata                  = "runtime.session.metadata"
	payloadTypeRouteFacts                = "runtime.session.route_facts"
	payloadTypeRunModelConfigured        = "run.model.configured"
	payloadTypeModelReconfigureCompleted = "runtime.model_reconfigure.completed"
	payloadTypeSessionEnd                = "session.end"
	payloadTypeCommandInvoked            = "command.invoked"
	// payloadTypeToolBatchEffectTerminal is the ONLY tool_batch.effect.*
	// family member this parser reads, and only for one nested field — see
	// the package doc's FOLLOW-UP note and parseToolBatchEffectTerminal.
	payloadTypeToolBatchEffectTerminal = "tool_batch.effect.terminal"
)

// payload.kind values carried under the "runtime.session" envelope.
const (
	sessionKindRun      = "run"
	sessionKindApproval = "approval"
	sessionKindTask     = "task"
)

// payload.event.kind / event.details.phase / facet.kind values read under
// sessionKindTask — narrowly, for the provider-retry shape only. See
// parseTaskEvent and parseTaskStatus.
const (
	taskEventStatus                = "status"
	taskStatusPhaseRetryScheduled  = "retry_scheduled"
	taskStatusFacetExternalAttempt = "external_attempt"
)

// payload.event.kind values under sessionKindRun.
const (
	runEventStarted              = "started"
	runEventTerminal             = "terminal"
	runEventModelCompleted       = "model_completed"
	runEventGoalUsageAttribution = "goal_usage_attribution"
	runEventAssistantMessage     = "assistant_message_committed"
	runEventAssistantToolCalls   = "assistant_tool_calls_committed"
	runEventToolResultBatch      = "tool_result_batch_committed"
	runEventFatalErrorClassified = "run_fatal_error_classified"
	runEventTodoSnapshotUpdated  = "todo_snapshot_updated"
	runEventInboxItemQueued      = "inbox_item_queued"
)

// task_completion.kind values nested inside
// tool_batch.effect.terminal's payload.record.outcome (see
// payloadTypeToolBatchEffectTerminal and parseToolBatchEffectTerminal).
const taskCompletionPending = "pending"

// event.source.source values on a kind:"run" event.kind:"inbox_item_queued"
// record (see parseInboxItemQueued). Live-confirmed
// (~/.local/share/muse/sessions, this machine): "subagent_result" and
// "user_steer" also occur and are deliberately left unhandled — only a
// background-task termination notice changes BackgroundProcessCount.
const inboxSourceBackgroundTaskTerminal = "background_task_terminal"

// payload.event.terminal values (only meaningful on runEventTerminal).
const (
	terminalCompleted = "completed"
	terminalFailed    = "failed"
	terminalCancelled = "cancelled"
)

// payload.event.kind values under sessionKindApproval.
const (
	approvalRequested       = "requested"
	approvalDecisionApplied = "decision_applied"
	approvalReviewCompleted = "automated_review_completed"
)

// requested's presentation_phase values (issue #1978) — see the "requested's
// presentation_phase" doc section above.
const (
	presentationPhaseAutomatedReviewing = "automated_reviewing"
)

// automated_review_completed's own clean-approval status value (issue
// #1978) — see the same doc section.
const approvalReviewStatusApproved = "approved"

// ParseLine implements agent.LineParser.
func (p *Parser) ParseLine(raw map[string]any) *tailer.ParsedEvent {
	if _, ok := raw["retained_frame"]; ok {
		return p.parseRetainedFrame(raw)
	}
	return p.decodeAndRoute(raw)
}

// decodeAndRoute is the single per-record dispatcher shared by ParseLine (for
// an ordinary top-level line) and parseRetainedFrame (for each of a frame's
// children), so a record nested inside a retained_frame is interpreted
// identically to the same record appearing on its own line.
func (p *Parser) decodeAndRoute(raw map[string]any) *tailer.ParsedEvent {
	payloadType, _ := raw["payload_type"].(string)
	if payloadType == "" {
		return &tailer.ParsedEvent{Skip: true}
	}
	ev := &tailer.ParsedEvent{Timestamp: parseRecordedAt(raw)}
	payload, _ := raw["payload"].(map[string]any)
	p.route(payloadType, payload, ev)
	return ev
}

// route dispatches on the record's top-level payload_type.
func (p *Parser) route(payloadType string, payload map[string]any, ev *tailer.ParsedEvent) {
	switch payloadType {
	case payloadTypeSession:
		p.parseSessionEnvelope(payload, ev)
	case payloadTypeMetadata:
		parseMetadata(payload, ev)
	case payloadTypeRouteFacts:
		parseRouteFacts(payload, ev)
	case payloadTypeRunModelConfigured:
		parseRunModelConfigured(payload, ev)
	case payloadTypeModelReconfigureCompleted:
		parseModelReconfigure(payload, ev)
	case payloadTypeSessionEnd:
		// session.end (format-spec §8): a clean /exit or a captured
		// exit_reason, never written at all on SIGKILL. No transcript-state
		// signal is derived from it — process liveness (DiscoverPID finding
		// no running process) is what the daemon already uses to notice a
		// session ended, and treating this as "activity" would be wrong for
		// a session that has genuinely finished.
		ev.Skip = true
	case payloadTypeCommandInvoked:
		// A slash command (/model, /usage, /exit — format-spec §10). /usage
		// leaves no other durable record at all (verified: 8 consecutive
		// invocations in one real session produced no other payload_type).
		// Bookkeeping, not a turn boundary.
		ev.Skip = true
	case payloadTypeToolBatchEffectTerminal:
		parseToolBatchEffectTerminal(payload, ev)
	default:
		// subagent.control.*, tool_batch.effect.started, approval_wait.effect.*,
		// runtime.session.task, runtime.retained_fact,
		// runtime.command_intake.*, session.login.completed,
		// session.opened.observed, session.startup_phases.observed,
		// session.workspace_branch.observed, runtime.user_intent.*,
		// session.name.changed, async.owner.attempt_admitted,
		// reminder.cleanup_effect.*, and any payload_type a future muse
		// release adds. See the package doc for why tool_batch.effect.* and
		// approval_wait.effect.* are deliberately not (otherwise) used even
		// though they exist — payloadTypeToolBatchEffectTerminal above is the
		// one narrow, documented exception.
		ev.Skip = true
	}
}

// parseToolBatchEffectTerminal reads ONLY
// payload.record.outcome.task_completion.kind — see the package doc's
// FOLLOW-UP note for why this single nested field is a narrow exception to
// "tool_batch.effect.* is deliberately not used". A "pending" completion
// means the tool call itself returned but payload.record.task_id keeps
// running as a detached background process (muse's own equivalent of Claude
// Code's Bash tool_result "Command running in background with ID: …" text);
// every other value ("terminal", "complete", or absent on a cancelled
// outcome — all three live-confirmed) is an ordinary synchronous close with
// nothing more to extract here, since ToolUses/ToolResultIDs already come
// from assistant_tool_calls_committed/tool_result_batch_committed.
//
// No output-file path exists in this shape, so the recorded BackgroundSpawn
// carries only the task_id (BashID) — that's sufficient for
// BackgroundProcessCount; see parseInboxItemQueued for why no lsof/PID
// liveness probe is needed on top of it.
//
// EventType is set to a value distinct from every string
// session.SessionMetrics.IsAgentDone treats as a turn boundary ("turn_done",
// "assistant", "assistant_output") — this is mid-turn bookkeeping about a
// tool call, never the turn itself ending, even when it happens to be the
// last line a scan pass reads.
func parseToolBatchEffectTerminal(payload map[string]any, ev *tailer.ParsedEvent) {
	record, _ := payload["record"].(map[string]any)
	outcome, _ := record["outcome"].(map[string]any)
	taskCompletion, _ := outcome["task_completion"].(map[string]any)
	if str(taskCompletion, "kind") != taskCompletionPending {
		ev.Skip = true
		return
	}
	taskID := str(record, "task_id")
	if taskID == "" {
		ev.Skip = true
		return
	}
	ev.EventType = "background_task_pending"
	ev.BackgroundSpawns = append(ev.BackgroundSpawns, tailer.BackgroundSpawn{BashID: taskID})
}

// parseInboxItemQueued reads ONLY event.source.source=="background_task_terminal"
// notifications (see inboxSourceBackgroundTaskTerminal) — muse's own
// definitive termination signal for a background bash task begun via
// parseToolBatchEffectTerminal's "pending" case. Every other source
// (live-confirmed in this corpus: "subagent_result", "user_steer") is
// unrelated inbox bookkeeping and stays Skip=true, same as
// inbox_item_drained/inbox_delivery_anomaly in the default arm above.
//
// Skip=true + OriginTaskNotification mirrors Claude Code's own
// handleTaskNotification (claudecode/parser.go): the tailer's
// applyBackgroundProcessTerminations (the Skip=true path) is what actually
// drops the id from the open set, and OriginTaskNotification tells it that
// this notification's arrival also starts the model's next inference turn —
// matching muse's own on-disk ordering, where this inbox drain is what wakes
// the run to process the completed background task (verified directly:
// session 01a09c47-506a-7763-b82a-9ab89a9e195a/subagent/
// 01a09c6d-9715-7113-b7f7-733681139140/session.jsonl, this inbox_item_queued
// record precedes that run's next model_completed).
func parseInboxItemQueued(event map[string]any, ev *tailer.ParsedEvent) {
	source, _ := event["source"].(map[string]any)
	if str(source, "source") != inboxSourceBackgroundTaskTerminal {
		ev.Skip = true
		return
	}
	taskID := str(source, "task_id")
	if taskID == "" {
		ev.Skip = true
		return
	}
	ev.Skip = true
	ev.OriginTaskNotification = true
	ev.TerminatedBackgroundTaskIDs = append(ev.TerminatedBackgroundTaskIDs, taskID)
}

// parseSessionEnvelope dispatches on payload.kind for the "runtime.session"
// envelope.
func (p *Parser) parseSessionEnvelope(payload map[string]any, ev *tailer.ParsedEvent) {
	switch str(payload, "kind") {
	case sessionKindRun:
		p.parseRunEvent(payload, ev)
	case sessionKindApproval:
		p.parseApprovalEvent(payload, ev)
	case sessionKindTask:
		parseTaskEvent(payload, ev)
	default:
		// kind "agent_tree_initialized", any future kind.
		ev.Skip = true
	}
}

// parseTaskEvent dispatches on payload.event.kind for kind:"task" —
// narrowly, for exactly one shape: a provider-retry status (see
// parseTaskStatus). Every other kind:"task" event (proposed/accepted/
// scheduled/started/side_effect_intent/output/completed/rejected/cancelled/
// timed_out/tool_delta/tool_output_ref, and any "status" event whose phase
// isn't retry_scheduled) stays Skip=true — see the package doc for why this
// parser otherwise reads tool calls off assistant_tool_calls_committed/
// tool_result_batch_committed instead of kind:"task".
// TestParseLine_TaskKindEvents_Skipped locks this for a side_effect_intent
// example; that lock is unaffected, since its fixture's event.kind is
// "side_effect_intent", never "status".
func parseTaskEvent(payload map[string]any, ev *tailer.ParsedEvent) {
	event, _ := payload["event"].(map[string]any)
	if str(event, "kind") != taskEventStatus {
		ev.Skip = true
		return
	}
	parseTaskStatus(event, ev)
}

// parseTaskStatus reads ONLY details.phase=="retry_scheduled" — muse's
// provider-retry bookkeeping (issue #1960's 2-22_provider-overloaded-retry
// fix). Every other status phase (live-confirmed: "opening_stream",
// "stream_succeeded") stays Skip=true.
//
// Live-confirmed (~/.local/share/muse/sessions, this machine: 86
// retry_scheduled status events across the corpus — rate_limited/429 (68),
// transport (9, no http_status), server/503 (5), decode (4, no http_status),
// server/504 (2) — every one carrying a details.facets[] entry with
// kind:"external_attempt"), so this generalizes across transient-failure
// kinds, not just 429. That facet's fields map onto tailer.SessionError:
//
//	attempt        -> Attempt
//	max_attempts   -> MaxAttempts
//	retry_delay_ms -> RetryIn (OptDurationFromMillis)
//	http_status    -> HTTPStatus (OptInt — absent for transport/decode kinds)
//	error_kind     -> Class
//
// tailer.OptInt/OptDurationFromMillis (not a bare int/time.Duration read) is
// the same choice claudecode's apiErrorFromSystemEvent makes for the
// identical shape: absence and zero are different facts here (a
// "transport"/"decode" error_kind genuinely carries no http_status).
//
// Phase is ErrorPhaseRetrying, deliberately — NOT ErrorPhaseUnknown, the
// choice parseRunTerminal's terminal:"failed" case makes for muse's OTHER
// error shape. The two are genuinely different: terminal:"failed" IS the run
// ending with no further attempt promised, so there is nothing left for
// ErrorPhaseRetrying's "clears on the next turn_done" exit to guard.
// retry_scheduled is the opposite — explicitly non-terminal, with the SAME
// task's own next status going on to "opening_stream" for the next attempt,
// then either "stream_succeeded" or another "retry_scheduled" (verified
// directly against the real task lifecycle this comment cites below) — so
// tailer.SessionError.ClearedByTurnBoundary() is correct: the run's own
// eventual terminal event, once the retries resolve, is a genuine recovery
// signal. See core/domain/session/session_error.go's ErrorPhaseRetrying doc
// for why that specific phase is what makes this scenario end green rather
// than sitting red forever. Example lifecycle (session
// 01a09c47-506a-7763-b82a-9ab89a9e195a, task_id
// 01a09c50-27eb-7e83-ac0e-d19d69bdade4): proposed(task_kind:
// "model.meta.response") -> ... -> started -> status(opening_stream,
// attempt 1) -> status(retry_scheduled, attempt 1->2, rate_limited/429) ->
// status(opening_stream, attempt 2) -> status(stream_succeeded) ->
// completed.
func parseTaskStatus(event map[string]any, ev *tailer.ParsedEvent) {
	details, _ := event["details"].(map[string]any)
	if str(details, "phase") != taskStatusPhaseRetryScheduled {
		ev.Skip = true
		return
	}
	facets, _ := details["facets"].([]any)
	var attempt map[string]any
	for _, f := range facets {
		if facet, ok := f.(map[string]any); ok && str(facet, "kind") == taskStatusFacetExternalAttempt {
			attempt = facet
			break
		}
	}
	if attempt == nil {
		ev.Skip = true
		return
	}
	// Skip=true: bookkeeping the tailer folds through applySkippedEvent
	// (applyMetadata -> applySessionError) regardless — the same routing
	// claudecode's api_error retry ladder uses (sessionerror.go's own doc:
	// "It is Skip=true ... so it reaches the tailer through applySkippedEvent
	// rather than processParsedEvent"). applySkippedEvent's own explicit
	// `if parsed.SessionError != nil { substantive = true }` check is what
	// keeps this pass from reading as NoSubstantiveActivity.
	ev.Skip = true
	ev.SessionError = &tailer.SessionError{
		Phase:       tailer.ErrorPhaseRetrying,
		Class:       str(attempt, "error_kind"),
		Message:     strings.TrimSpace(str(event, "message")),
		HTTPStatus:  tailer.OptInt(attempt, "http_status"),
		Attempt:     tailer.OptInt(attempt, "attempt"),
		MaxAttempts: tailer.OptInt(attempt, "max_attempts"),
		RetryIn:     tailer.OptDurationFromMillis(attempt, "retry_delay_ms"),
	}
}

// parseRunEvent dispatches on payload.event.kind for kind:"run".
func (p *Parser) parseRunEvent(payload map[string]any, ev *tailer.ParsedEvent) {
	event, _ := payload["event"].(map[string]any)
	switch str(event, "kind") {
	case runEventStarted:
		p.parseRunStarted(event, ev)
	case runEventTerminal:
		p.parseRunTerminal(event, ev)
	case runEventModelCompleted:
		parseModelCompleted(event, ev)
	case runEventGoalUsageAttribution:
		// Duplicate of model_completed's usage — see the package doc.
		ev.Skip = true
	case runEventAssistantMessage:
		parseAssistantMessageCommitted(event, ev)
	case runEventAssistantToolCalls:
		parseAssistantToolCallsCommitted(event, ev)
	case runEventToolResultBatch:
		parseToolResultBatchCommitted(event, ev)
	case runEventFatalErrorClassified:
		p.parseRunFatalErrorClassified(event, ev)
	case runEventTodoSnapshotUpdated:
		p.parseTodoSnapshotUpdated(event, ev)
	case runEventInboxItemQueued:
		parseInboxItemQueued(event, ev)
	default:
		// context_block_diagnostic, provider_request_options_configured,
		// model_input_trace_recorded, model_response_created,
		// reasoning_committed, reasoning_summary_delta,
		// reasoning_summary_committed, model_request_configured,
		// memory_reminder_child_session_linked, resource_usage_sampled,
		// reminder_proposal, reminder_reconciler_outcome,
		// reminder_installed, task_stream_linked,
		// inbox_item_drained, inbox_delivery_anomaly,
		// skill_read_observed, skill_reminder_decision — high-volume
		// internal bookkeeping this stage has no use for. Format-spec §11
		// confirms context_block_diagnostic in particular is prompt-assembly
		// bookkeeping (which context blocks loaded), never a real
		// compaction/eviction event in this corpus.
		ev.Skip = true
	}
}

// parseRunStarted opens a turn: the literal user prompt that launched this
// run (format-spec §3).
func (p *Parser) parseRunStarted(event map[string]any, ev *tailer.ParsedEvent) {
	p.lastErrorClass = ""
	ev.EventType = "user_message"
	ev.ClearToolNames = true
	if prompt := str(event, "prompt"); prompt != "" {
		ev.UserText = strings.TrimSpace(prompt)
	}
}

// parseRunTerminal closes a turn. terminal discriminates the outcome
// (format-spec §3, 37 samples across the corpus: 31 completed, 5 cancelled,
// 1 failed). EventType is "turn_done" for all three: every one of them is
// the run's own terminal record, so the turn objectively ended in every
// case — the classifier's session_error rule (fed by SessionError below) is
// what routes a failed run to `error` rather than `ready`, not a different
// EventType.
func (p *Parser) parseRunTerminal(event map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "turn_done"
	switch str(event, "terminal") {
	case terminalFailed:
		class := p.lastErrorClass
		if class == "" {
			class = "terminal_failed"
		}
		ev.SessionError = &tailer.SessionError{
			// PHASE UNKNOWN, deliberately (the junie/copilot precedent):
			// muse's terminal record never says whether another attempt is
			// coming — no attempt/max_attempts counters, no retry field —
			// and unlike copilot's session.error this one IS itself the
			// turn boundary, so ErrorPhaseRetrying's "clears on the next
			// turn_done" exit would have nothing left to guard: the next
			// run always opens with its own "started" (→ user_message,
			// ClearToolNames=true), which clears any phase unconditionally
			// via ParsedEvent.StartsNewUserTurn before a bare turn_done
			// check is ever reached (core/pkg/tailer/tailer_metrics.go
			// clearSessionErrorOnRecovery). UNVERIFIED whether muse ever
			// auto-retries a failed run without a fresh "started" in
			// between — no such example exists in the available corpus.
			Phase:   tailer.ErrorPhaseUnknown,
			Class:   class,
			Message: strings.TrimSpace(str(event, "reason")),
		}
	case terminalCancelled:
		// UNVERIFIED whether this represents a genuine user ESC keypress.
		// Every terminal:"cancelled" example in the corpus (format-spec §3)
		// came from a subagent's own run stream with internal-sounding
		// reasons ("cancelled during model step", "...end-of-turn reminder
		// wait", "...tool result reconciliation") that read like
		// programmatic cancellation (e.g. a parent cancelling a child task)
		// rather than an explicit interrupt tag — no live ESC keypress was
		// driven to check. So IsUserInterrupt is deliberately left false
		// here: the turn still correctly ends (EventType=turn_done above),
		// it just isn't asserted to be a HUMAN cancellation this parser
		// never confirmed.
	case terminalCompleted:
		p.lastErrorClass = ""
	}
}

// parseModelCompleted is the token-accounting source for one LLM call
// (format-spec §6). Skip=true: pure bookkeeping the tailer folds through
// applySkippedEvent/applyMetadata regardless (#1798's routing, the same
// pattern junie/copilot's own metrics events use).
func parseModelCompleted(event map[string]any, ev *tailer.ParsedEvent) {
	ev.Skip = true
	usage, _ := event["usage"].(map[string]any)
	if usage == nil {
		return
	}
	// tailer.ExtractUsage (parser.go's shared helper) is deliberately NOT used
	// here: it reads Claude/Codex-style field names (input_tokens,
	// cache_read_input_tokens, ...) or Pi's (input, cacheRead, ...) and sums
	// them assuming input_tokens is ALREADY the fresh (non-cached) count — the
	// opposite of muse's convention below, where input_tokens is INCLUSIVE of
	// the cache. Calling it unmodified on muse's raw usage map would count
	// cache_read_tokens twice: once inside its unmodified input_tokens read,
	// once again in its own CacheRead field. The netting immediately below is
	// muse-specific and has to happen before any total is summed.
	//
	// input_tokens is INCLUSIVE of the cached portion, the same OpenAI-
	// Responses-API convention codex's parser documents and corrects for
	// (core/adapters/inbound/agents/codex/parser.go's
	// applyCodexCachedTokens). Verified directly against this corpus: across
	// 476 real model_completed records with nonzero usage, input_tokens was
	// never once smaller than cache_read_tokens/cached_tokens, and the gap
	// tracked the small amount of genuinely fresh context each turn adds on
	// top of an already-cached, growing prompt — the signature of an
	// inclusive total, not two independent counters. cached_tokens and
	// cache_read_tokens were byte-identical in every one of those 476
	// records; cache_read_tokens is preferred since it is the field name
	// muse itself uses whenever both are present, with cached_tokens as a
	// fallback for the shape (seen live under --provider echo) that omits
	// cache_read_tokens/cache_write_tokens entirely.
	cacheRead := i64(usage, "cache_read_tokens")
	if cacheRead == 0 {
		cacheRead = i64(usage, "cached_tokens")
	}
	bd := tailer.UsageBreakdown{
		Input:  nonNegative(i64(usage, "input_tokens") - cacheRead),
		Output: i64(usage, "output_tokens"),
		// Muse doesn't distinguish cache-write TTLs (no field resembling a
		// 5m/1h split anywhere in this corpus); 5m is the default tier the
		// price map falls back through for a provider that doesn't
		// distinguish them, the same reasoning copilot's cacheWriteTokens
		// comment gives for the identical gap.
		CacheCreation5m: i64(usage, "cache_write_tokens"),
		CacheRead:       cacheRead,
	}
	model := str(event, "model")
	if model == "" && bd == (tailer.UsageBreakdown{}) {
		return
	}
	if model != "" {
		applyModelIdentity(model, ev)
	}
	ev.Contribution = &tailer.PerTurnContribution{Model: tailer.NormalizeModelName(model), Usage: bd}
	if bd.Input > 0 || bd.Output > 0 {
		// Total is the sum of the four buckets above, NOT a second read of
		// some raw "total_tokens" field (muse's usage object has none — see
		// the real fixture: input_tokens/output_tokens/cached_tokens/
		// cache_write_tokens/cache_read_tokens/reasoning_tokens only). bd.Input
		// is already netted (input_tokens minus cache_read_tokens, per the
		// doc comment above), so summing it with CacheRead here reconstructs
		// the ORIGINAL inclusive input rather than double-counting the cached
		// portion — the same hand-summed idiom aider's closeModelCall and
		// geminicli's applyTokens use for their own TokenSnapshot.Total.
		ev.Tokens = &tailer.TokenSnapshot{
			Input:         bd.Input,
			Output:        bd.Output,
			CacheRead:     bd.CacheRead,
			CacheCreation: bd.CacheCreation5m,
			Total:         bd.Input + bd.Output + bd.CacheRead + bd.CacheCreation5m,
		}
	}
}

// parseAssistantMessageCommitted surfaces the assistant's reply text for
// waiting-state display and the prose waiting-cue scan. Non-settling — the
// run's own "terminal" event is the turn boundary (format-spec §3) — so a
// reply followed by more tool calls correctly leaves the session working.
func parseAssistantMessageCommitted(event map[string]any, ev *tailer.ParsedEvent) {
	ev.EventType = "assistant_message"
	text := str(event, "text")
	if strings.TrimSpace(text) == "" {
		return
	}
	// Scan the FULL text for the task-estimate marker (issue #558) before the
	// truncation below drops all but the last 200 runes — the same ordering
	// every other adapter's parser uses (aider/codex/opencode/pi).
	if est := tailer.ScanTaskEstimate(text, ev.Timestamp); est != nil {
		ev.TaskEstimate = est
	}
	ev.AssistantText = tailer.TruncateAssistantText(text)
	// Scan the FULL text, not the truncated display tail — a question
	// sitting before the trailing 200 runes would otherwise settle the turn
	// (issue #1150), the same computation every other adapter performs.
	ev.PendingWaitingCue = session.ProseIndicatesWaiting(tailer.WaitingScanWindow(text))
}

// parseAssistantToolCallsCommitted opens one or more tool calls (format-spec
// §4's real capture: event.tool_calls[] carries call_id + name + args
// directly — see the package doc for why this, not task/tool_batch.effect.*,
// is the tracking source).
func parseAssistantToolCallsCommitted(event map[string]any, ev *tailer.ParsedEvent) {
	calls, _ := event["tool_calls"].([]any)
	ev.EventType = "function_call"
	for _, c := range calls {
		call, ok := c.(map[string]any)
		if !ok {
			continue
		}
		id := str(call, "call_id")
		name := str(call, "name")
		if id == "" || name == "" {
			continue
		}
		ev.ToolUses = append(ev.ToolUses, tailer.ToolUse{ID: id, Name: name})
	}
	if len(ev.ToolUses) == 0 {
		ev.Skip = true
	}
}

// parseToolResultBatchCommitted closes one or more tool calls, pairing on
// tool_call_id (format-spec §4).
func parseToolResultBatchCommitted(event map[string]any, ev *tailer.ParsedEvent) {
	results, _ := event["results"].([]any)
	ev.EventType = "function_call_output"
	for _, r := range results {
		result, ok := r.(map[string]any)
		if !ok {
			continue
		}
		id := str(result, "tool_call_id")
		if id == "" {
			continue
		}
		ev.ToolResultIDs = append(ev.ToolResultIDs, id)
		if resultTextReportsNonZeroExit(str(result, "text")) {
			ev.IsError = true
		}
	}
	if len(ev.ToolResultIDs) == 0 {
		ev.Skip = true
	}
}

// resultTextReportsNonZeroExit best-effort-sniffs a tool result's text for an
// embedded exit_code field. Verified live: a real bash tool_result_batch
// entry's text is literally the same JSON chunk the underlying task's own
// "output" event carries — {"chunk_id":...,"command":...,"exit_code":0,
// "terminal_status":"completed","output":...} — confirmed by direct
// byte-for-byte comparison of the two in a captured session. No sample
// anywhere in the available corpus ever captured a NONZERO exit_code (every
// observed command succeeded, and the one non-"completed" tool_batch.effect
// outcome in the corpus was "cancelled", not a failure) — so whether a real
// command failure reaches muse's tool result text in exactly this shape, and
// whether every non-bash tool reports failure the same way at all, is
// UNVERIFIED. Returns false for any text that doesn't parse as this exact
// shape, which is the safe default: an unrecognized shape must never be read
// as a failure.
func resultTextReportsNonZeroExit(text string) bool {
	if !strings.Contains(text, `"exit_code"`) {
		return false
	}
	var chunk struct {
		ExitCode *int `json:"exit_code"`
	}
	if err := json.Unmarshal([]byte(text), &chunk); err != nil {
		return false
	}
	return chunk.ExitCode != nil && *chunk.ExitCode != 0
}

// parseRunFatalErrorClassified remembers the error_class a subsequent
// terminal:"failed" event should carry (see lastErrorClass). Skip=true:
// bookkeeping, not itself a turn boundary or activity signal beyond what the
// terminal event that follows already provides.
func (p *Parser) parseRunFatalErrorClassified(event map[string]any, ev *tailer.ParsedEvent) {
	ev.Skip = true
	p.lastErrorClass = str(event, "error_class")
}

// parseTodoSnapshotUpdated folds muse's write_todos tool into
// TaskCreate/TaskUpdate deltas via the shared tailer.TodoReconciler — the
// same mechanism opencode's appendTodowriteDeltas and gemini-cli's
// appendWriteTodosDeltas use for their own full-snapshot-replace todo tools
// (todo_reconciler.go's own doc names all three as the intended callers).
//
// event.items[] is the whole current list re-sent every call — verified live
// (~/.local/share/muse/sessions, session
// 01a09c47-506a-7763-b82a-9ab89a9e195a/session.jsonl, 4 revisions): each item
// is {"text":..., "status":...} with no stable id, so items are keyed by
// their literal text, same as opencode's `content` key. status is spelled
// "pending"/"in_progress"/"completed" in every one of those revisions —
// exactly tailer.TaskStatusPending/InProgress/Completed's own vocabulary —
// so it is passed straight through with no remapping.
//
// EventType is deliberately a value that matches none of the strings
// session.SessionMetrics.IsAgentDone treats as a turn boundary ("turn_done",
// "assistant", "assistant_output"): muse's own turn boundary is exclusively
// kind:"run" event.kind:"terminal" (parseRunTerminal), and this is pure
// mid-turn task-list bookkeeping that must never itself look like the turn
// ending, even when it happens to be the last line a scan pass reads.
func (p *Parser) parseTodoSnapshotUpdated(event map[string]any, ev *tailer.ParsedEvent) {
	rawItems, _ := event["items"].([]any)
	todos := make([]tailer.Todo, 0, len(rawItems))
	for _, ri := range rawItems {
		item, ok := ri.(map[string]any)
		if !ok {
			continue
		}
		text := str(item, "text")
		if text == "" {
			continue
		}
		todos = append(todos, tailer.Todo{Key: text, Status: str(item, "status")})
	}
	if len(todos) == 0 {
		ev.Skip = true
		return
	}
	ev.EventType = "task_snapshot"
	p.todos.Reconcile(todos, ev)
}

// parseApprovalEvent dispatches on payload.event.kind for kind:"approval"
// (format-spec §5).
func (p *Parser) parseApprovalEvent(payload map[string]any, ev *tailer.ParsedEvent) {
	event, _ := payload["event"].(map[string]any)
	switch str(event, "kind") {
	case approvalRequested:
		parseApprovalRequested(event, ev)
	case approvalDecisionApplied:
		parseApprovalDecisionApplied(event, ev)
	case approvalReviewCompleted:
		parseApprovalReviewCompleted(event, ev)
	default:
		// automated_review_started (the :auto-review LLM-judge's own
		// review-start notice) and stage_requirement_resolved (a
		// per-requirement resolution notice distinct from the
		// pending_action_id "requested" opened above, format-spec §5) stay
		// pure bookkeeping about HOW a decision is being reached: whether
		// the wait opens at all is decided by requested's own
		// presentation_phase (parseApprovalRequested), and re-opened, if
		// the judge doesn't cleanly auto-approve, by
		// automated_review_completed's own outcome
		// (parseApprovalReviewCompleted, case above) — issue #1978.
		// Neither of these two intermediate notices itself changes
		// whether the agent is blocked on the user.
		ev.Skip = true
	}
}

// parseApprovalRequested opens a permission prompt: the agent is blocked
// awaiting an approval decision on one tool call, keyed by pending_action_id
// (format-spec §5) — UNLESS presentation_phase says muse's own :auto-review
// LLM judge is deciding first, not the user. See this file's "requested's
// presentation_phase" doc section for the measured corpus split and timing
// (issue #1978).
func parseApprovalRequested(event map[string]any, ev *tailer.ParsedEvent) {
	id := str(event, "pending_action_id")
	if id == "" {
		ev.Skip = true
		return
	}
	if str(event, "presentation_phase") == presentationPhaseAutomatedReviewing {
		// Open nothing: the judge is deciding, not the user, so there is
		// no user-visible wait yet. Deliberately Skip=true rather than
		// some other EventType such as "function_call" — that would
		// fabricate an open tool call this record does not represent.
		// It is safe to skip because the session is already "working",
		// not "ready": the tool call this approval covers was already
		// opened by the preceding assistant_tool_calls_committed record
		// (EventType "function_call"), so this event has nothing left to
		// contribute to session state — it is pure bookkeeping about HOW
		// the pending decision will be reached, the same category as
		// automated_review_started/stage_requirement_resolved in
		// parseApprovalEvent's default branch.
		ev.Skip = true
		return
	}
	ev.EventType = "permission_requested"
	ev.PermissionRequestIDs = []string{id}
}

// parseApprovalReviewCompleted closes the :auto-review judge's own review
// lifecycle and, when the review did NOT cleanly auto-approve, re-opens the
// permission prompt this pending_action_id names — the wait has genuinely
// become the user's (issue #1978). Re-opens when ANY of: status is not
// "approved" (the only escalation value observed in this corpus is
// "escalated"), timed_out is true, or failure is present and non-null. On a
// clean auto-approval (status "approved", timed_out false, failure
// absent/null) this stays Skip=true — the same outcome every
// automated_review_completed record had before this fix, since it fell into
// parseApprovalEvent's default branch.
func parseApprovalReviewCompleted(event map[string]any, ev *tailer.ParsedEvent) {
	id := str(event, "pending_action_id")
	cleanApproval := str(event, "status") == approvalReviewStatusApproved &&
		!boolField(event, "timed_out") &&
		!hasNonNilField(event, "failure")
	if id == "" || cleanApproval {
		ev.Skip = true
		return
	}
	ev.EventType = "permission_requested"
	ev.PermissionRequestIDs = []string{id}
}

// parseApprovalDecisionApplied closes the prompt its pending_action_id
// names, whatever the decision — an approval and a denial both mean the
// agent is no longer blocked on this particular prompt.
func parseApprovalDecisionApplied(event map[string]any, ev *tailer.ParsedEvent) {
	id := str(event, "pending_action_id")
	if id == "" {
		ev.Skip = true
		return
	}
	ev.EventType = "permission_completed"
	ev.PermissionResolvedIDs = []string{id}
}

// parseMetadata reads the session-wide build version (format-spec §7).
// Skip=true: session metadata is not activity.
func parseMetadata(payload map[string]any, ev *tailer.ParsedEvent) {
	ev.Skip = true
	record, _ := payload["record"].(map[string]any)
	build, _ := record["build"].(map[string]any)
	if semver := str(build, "semver"); semver != "" {
		ev.AgentVersion = semver
	}
}

// parseRouteFacts reads the session's working directory (format-spec §2),
// the only place a Muse transcript names it. Skip=true: bookkeeping, but
// wanted as early as possible so PID binding and dashboard project grouping
// don't wait for the first turn — the same reasoning copilot's
// parseSessionStart gives for its own cwd stamp.
func parseRouteFacts(payload map[string]any, ev *tailer.ParsedEvent) {
	ev.Skip = true
	record, _ := payload["record"].(map[string]any)
	if cwd := str(record, "cwd"); cwd != "" {
		ev.CWD = cwd
	}
}

// parseRunModelConfigured reads the model a specific run is using
// (format-spec §7) — fires per run, before that run's first model_completed,
// so it lights up the model display (and, via applyModelIdentity, the
// context-window budget) as early as possible each turn. Skip=true:
// bookkeeping.
func parseRunModelConfigured(payload map[string]any, ev *tailer.ParsedEvent) {
	ev.Skip = true
	record, _ := payload["record"].(map[string]any)
	if model := str(record, "model_id"); model != "" {
		applyModelIdentity(model, ev)
	}
}

// parseModelReconfigure reads the session's effective model after a startup
// resolution or a mid-session /model switch (format-spec §7). Skip=true:
// bookkeeping.
func parseModelReconfigure(payload map[string]any, ev *tailer.ParsedEvent) {
	ev.Skip = true
	record, _ := payload["record"].(map[string]any)
	effective, _ := record["effective"].(map[string]any)
	if model := str(effective, "model_id"); model != "" {
		applyModelIdentity(model, ev)
	}
}

// applyModelIdentity sets ev.ModelName from model — the raw id muse reports
// (run.model.configured's model_id, runtime.model_reconfigure.completed's
// effective.model_id, or model_completed's own model field; all three name
// the same session-scoped concept, format-spec §7) — and, when the local
// model-catalog cache has a matching row (issue #1960 stage 3;
// contextWindowForModel, catalog.go), ev.ContextWindow alongside it. The raw
// (pre-normalize) id is what's looked up: NormalizeModelName only rewrites a
// handful of Claude short aliases and strips a "[1m]" suffix (parser.go's
// own NormalizeModelName), neither of which ever appears on a muse model id,
// so the catalog's model_id column (muse-spark-1.3-contributor, verified
// live on this machine) matches the raw string unchanged. Shared by every
// call site that learns a model id so the lookup isn't triplicated.
func applyModelIdentity(model string, ev *tailer.ParsedEvent) {
	ev.ModelName = tailer.NormalizeModelName(model)
	if window, ok := contextWindowForModel(model); ok {
		ev.ContextWindow = window
	}
}

// parseRetainedFrame unwraps a retained_frame outer envelope — see the
// package doc for the full structural explanation and the corpus evidence
// behind it.
func (p *Parser) parseRetainedFrame(raw map[string]any) *tailer.ParsedEvent {
	children, _ := raw["children"].([]any)
	if len(children) == 0 {
		return &tailer.ParsedEvent{Skip: true}
	}
	events := make([]*tailer.ParsedEvent, 0, len(children))
	for _, c := range children {
		child, ok := c.(map[string]any)
		if !ok {
			continue
		}
		recordJSON, _ := child["record_json"].(string)
		if recordJSON == "" {
			continue
		}
		var inner map[string]any
		if err := json.Unmarshal([]byte(recordJSON), &inner); err != nil {
			continue
		}
		events = append(events, p.decodeAndRoute(inner))
	}
	return mergeChildEvents(events)
}

// mergeChildEvents folds N per-child ParsedEvents into the single event one
// transcript line must produce. Id-keyed deltas (ToolUses, ToolResultIDs,
// PermissionRequestIDs, PermissionResolvedIDs) are concatenated in child
// order — safe because the tailer applies each through an idempotent,
// id-keyed map (applyToolCallDeltas, applyPermissionDeltas in
// core/pkg/tailer/tailer.go), so duplicate or out-of-order opens/closes
// within one merged event are harmless. Single-value fields (EventType,
// ModelName, ContextWindow, CWD, AgentVersion, AssistantText, UserText,
// SessionError, Contribution, Tokens) take the LAST non-empty child's value
// — the same "latest wins" convention every adapter in this repo already uses for a
// skipped-event metadata stamp. Boolean flags (IsError, ClearToolNames,
// IsUserInterrupt, PendingWaitingCue) OR across children. The merged event
// is Skip=true only when every child was Skip=true.
//
// The per-child merge is split below by strategy (bookkeeping, additive
// deltas, OR'd flags, last-wins scalars, last-non-nil pointers) rather than
// left as one flat pass — each helper is independent of the others (none
// reads a field another one writes), so the split changes nothing about
// which value wins, only how the four strategies read on the page. This
// keeps SonarCloud's go:S3776 cognitive-complexity threshold (15) honest:
// the flat version was a single function nesting 17 independent ifs inside
// one loop (complexity 35 — verified via SonarCloud PR #1961 analysis
// before this split), which is exactly the "many unrelated cases in one
// function" shape the metric exists to catch, not a case where splitting
// would separate a check from what it guards.
func mergeChildEvents(events []*tailer.ParsedEvent) *tailer.ParsedEvent {
	merged := &tailer.ParsedEvent{Skip: true}
	for _, ev := range events {
		if ev == nil {
			continue
		}
		mergeChildBookkeeping(merged, ev)
		mergeChildDeltaSlices(merged, ev)
		mergeChildBooleanFlags(merged, ev)
		mergeChildScalarFields(merged, ev)
		mergeChildPointerFields(merged, ev)
	}
	return merged
}

// mergeChildBookkeeping applies the two fields that don't fit a "value"
// merge strategy: Timestamp takes the latest non-zero child value, and Skip
// flips to false as soon as any child is non-Skip (it starts true).
func mergeChildBookkeeping(merged, ev *tailer.ParsedEvent) {
	if !ev.Timestamp.IsZero() {
		merged.Timestamp = ev.Timestamp
	}
	if !ev.Skip {
		merged.Skip = false
	}
}

// mergeChildDeltaSlices concatenates the id-keyed delta slices in child
// order — see mergeChildEvents' doc comment for why out-of-order or
// duplicate ids across children are harmless downstream.
func mergeChildDeltaSlices(merged, ev *tailer.ParsedEvent) {
	merged.ToolUses = append(merged.ToolUses, ev.ToolUses...)
	merged.ToolResultIDs = append(merged.ToolResultIDs, ev.ToolResultIDs...)
	merged.PermissionRequestIDs = append(merged.PermissionRequestIDs, ev.PermissionRequestIDs...)
	merged.PermissionResolvedIDs = append(merged.PermissionResolvedIDs, ev.PermissionResolvedIDs...)
}

// mergeChildBooleanFlags ORs each flag across children: once any child sets
// one, the merged event keeps it set regardless of later children.
func mergeChildBooleanFlags(merged, ev *tailer.ParsedEvent) {
	if ev.IsError {
		merged.IsError = true
	}
	if ev.ClearToolNames {
		merged.ClearToolNames = true
	}
	if ev.IsUserInterrupt {
		merged.IsUserInterrupt = true
	}
	if ev.PendingWaitingCue {
		merged.PendingWaitingCue = true
	}
}

// mergeChildScalarFields applies "last non-empty/non-zero child wins" to the
// single-value fields that have an obvious empty/zero sentinel.
func mergeChildScalarFields(merged, ev *tailer.ParsedEvent) {
	if ev.EventType != "" {
		merged.EventType = ev.EventType
	}
	if ev.ModelName != "" {
		merged.ModelName = ev.ModelName
	}
	if ev.ContextWindow > 0 {
		merged.ContextWindow = ev.ContextWindow
	}
	if ev.AgentVersion != "" {
		merged.AgentVersion = ev.AgentVersion
	}
	if ev.CWD != "" {
		merged.CWD = ev.CWD
	}
	if ev.AssistantText != "" {
		merged.AssistantText = ev.AssistantText
	}
	if ev.UserText != "" {
		merged.UserText = ev.UserText
	}
}

// mergeChildPointerFields applies "last non-nil child wins" to the
// pointer-typed fields, which have no zero-value sentinel of their own.
func mergeChildPointerFields(merged, ev *tailer.ParsedEvent) {
	if ev.SessionError != nil {
		merged.SessionError = ev.SessionError
	}
	if ev.Contribution != nil {
		merged.Contribution = ev.Contribution
	}
	if ev.Tokens != nil {
		merged.Tokens = ev.Tokens
	}
}

// parseRecordedAt reads a record's "recorded_at" stamp — Unix MICROSECONDS
// (verified: a captured value of 1789327863265453 divided by 1e6 lands on
// 2026-09-13, the day these sessions were recorded), falling back to the
// shared ParseTimestamp heuristics for a record without one.
func parseRecordedAt(raw map[string]any) time.Time {
	if v, ok := raw["recorded_at"].(float64); ok && v > 0 {
		return time.UnixMicro(int64(v))
	}
	return tailer.ParseTimestamp(raw)
}

// str reads a string field from a decoded JSON object, returning "" when the
// map is nil, the key is absent, or the value is not a string.
func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
}

// boolField reads a bool field from a decoded JSON object, returning false
// when the map is nil, the key is absent, or the value is not a bool.
func boolField(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	b, _ := m[key].(bool)
	return b
}

// hasNonNilField reports whether key is present in m with a non-null JSON
// value. A missing key and an explicit JSON null are indistinguishable once
// decoded (both give the map lookup's zero value, nil), so this also covers
// "absent" — exactly the "present and non-null" check automated_review_
// completed's failure field needs (issue #1978).
func hasNonNilField(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	return m[key] != nil
}

// i64 reads a numeric field from a decoded JSON object as int64
// (encoding/json decodes all JSON numbers to float64), returning 0 when
// absent, nil-map, or non-numeric.
func i64(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	v, _ := m[key].(float64)
	return int64(v)
}

// nonNegative clamps a computed delta to zero.
func nonNegative(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
