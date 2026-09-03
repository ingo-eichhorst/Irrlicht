// hooks.go provides the HTTP handler for receiving Claude Code hook events.
// The events we install are listed once, in installedHookEvents
// (hookinstaller.go); this comment deliberately does not restate them, because
// the copy that did drifted to six for the whole of #1173's seven-event install
// (#1356). The daemon uses them to surface user-blocking and turn-done state in
// the classifier. The roles worth calling out: PermissionRequest covers
// permission gates (issue #108); PreToolUse on AskUserQuestion / ExitPlanMode
// covers user-input overlays that block the agent before the transcript is
// flushed (issue #307); Stop is the authoritative per-turn done signal
// delivered at true turn end, carrying the final assistant text (issue #1161);
// Notification/idle_prompt is the authoritative "idle at the prompt waiting for
// the user" signal for turns that end with no prose waiting-cue (issue #1173).
package claudecode

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
	"irrlicht/core/ports/outbound"
)

// Hook event names. Claude Code fires these; the daemon recognizes only
// these seven and ignores everything else.
//
// The names themselves live in core/domain/session, next to the hook-name →
// SignalKind table that the detector and the replay harness share — neither of
// which may import this package (issue #1320). These are re-exports so
// adapter-side call sites read in adapter terms.
const (
	HookPermissionRequest  = session.HookPermissionRequest
	HookPreToolUse         = session.HookPreToolUse
	HookPostToolUse        = session.HookPostToolUse
	HookPostToolUseFailure = session.HookPostToolUseFailure
	HookPreCompact         = session.HookPreCompact
	// HookStop fires once at true turn end, carrying last_assistant_message.
	// It is the authoritative turn-done signal for claudecode (issue #1161).
	HookStop = session.HookStop
	// HookNotification fires for Claude Code UI notifications, carrying a
	// notification_type discriminator. The daemon acts on the types in
	// notificationTypesDrivingState, each of which means a human is blocked:
	// idle_prompt is the agent idle at the prompt after a finished turn (issue
	// #1173), and the rest are blocking dialogs (issue #1861). Every other type
	// is accepted and ignored.
	HookNotification = session.HookNotification
)

// notificationTypeIdlePrompt is the Notification hook's notification_type value
// for "the agent is idle at the prompt waiting for the user" (issue #1173).
const notificationTypeIdlePrompt = "idle_prompt"

// The Notification notification_type values meaning "a human is blocked right
// now" (issue #1861). Each is the only hook signal Claude Code emits for its
// case: none of these dialogs carries a tool name, so none raises a
// PermissionRequest at any matcher width.
//
// permission_prompt is also Claude Code's DEFAULT type for a dialog
// notification that sets none of its own, so it covers the unnamed remainder;
// the elicitation and agent types set their own and would otherwise be missed.
// hookMatcherNotification carries the full type vocabulary and the reasons the
// remaining types are excluded.
const (
	notificationTypePermissionPrompt = "permission_prompt"
	notificationTypeElicitation      = "elicitation_dialog"
	notificationTypeElicitationURL   = "elicitation_url_dialog"
	notificationTypeAgentNeedsInput  = "agent_needs_input"
)

// notificationRoute is where a state-driving notification_type is dispatched.
// It is the map's VALUE rather than a predicate over its keys, so the
// classification is data: adding a type means adding one row, and a type whose
// route nobody chose cannot silently inherit another one's.
type notificationRoute int

const (
	// routeIdlePrompt: the turn is over and the agent is waiting at the prompt.
	routeIdlePrompt notificationRoute = iota
	// routeBlockingDialog: a dialog is on screen and the user is blocked on it.
	routeBlockingDialog
)

// notificationTypesDrivingState maps each notification_type the daemon acts on
// to the route it takes. Claude Code's Notification matcher already filters on
// notification_type; the handler re-checks against this map as
// defense-in-depth, so a hand-broadened matcher in a user's settings.json can
// only ever deliver types this code understands rather than dispatching
// auth_success, agent_completed, push_notification or a type a future release
// adds.
//
// The route is the split that matters: idle_prompt and the blocking dialogs are
// NOT interchangeable. They hold different signals with different staleness
// rules — SignalIdlePrompt is dropped the moment IsAgentDone goes false, which
// is precisely the state a mid-turn dialog is already in, so routing a dialog
// through the idle path would discard it on the next pass.
var notificationTypesDrivingState = map[string]notificationRoute{
	notificationTypeIdlePrompt:       routeIdlePrompt,
	notificationTypePermissionPrompt: routeBlockingDialog,
	notificationTypeElicitation:      routeBlockingDialog,
	notificationTypeElicitationURL:   routeBlockingDialog,
	notificationTypeAgentNeedsInput:  routeBlockingDialog,
}

// compactTriggerManual is the PreCompact trigger value for a user-invoked
// /compact (as opposed to "auto"). Only manual compaction forces working — an
// auto-compaction fires mid-turn while the session is already working (#657).
const compactTriggerManual = "manual"

// logComponentHookReceiver is the Logger component tag for every log line
// emitted by the hook HTTP handler below.
const logComponentHookReceiver = "hook-receiver"

// transcriptExt is the file extension of a Claude Code transcript. The hook
// receiver confines caller-supplied paths to this extension, and the session id
// is the filename stem once it is stripped.
const transcriptExt = ".jsonl"

// Tool names that suspend the agent waiting for user input. PreToolUse hooks
// must match one of these — anything else is rejected by the handler, even
// if the matcher in settings.json was edited to be broader. Defense-in-depth
// against the matcher being the sole filter.
const (
	toolAskUserQuestion = "AskUserQuestion"
	toolExitPlanMode    = "ExitPlanMode"
)

// hookPayload is the JSON body sent by Claude Code hook events.
// Only the fields used by the handler are decoded; the rest is ignored.
type hookPayload struct {
	SessionID      string          `json:"session_id"`
	TranscriptPath string          `json:"transcript_path"`
	HookEventName  string          `json:"hook_event_name"`
	ToolName       string          `json:"tool_name"`
	ToolUseID      string          `json:"tool_use_id,omitempty"`
	PermissionMode string          `json:"permission_mode,omitempty"`
	IsInterrupt    bool            `json:"is_interrupt,omitempty"`
	ToolInput      json.RawMessage `json:"tool_input,omitempty"`
	// Trigger is "manual" or "auto" on PreCompact events (the compaction
	// cause). Empty on other hook events.
	Trigger string `json:"trigger,omitempty"`
	// LastAssistantMessage is the full text of the turn's final assistant
	// message, carried by the Stop hook (issue #1161). Empty on other events.
	LastAssistantMessage string `json:"last_assistant_message,omitempty"`
	// NotificationType is the Notification hook's discriminator (e.g.
	// "idle_prompt", "permission_prompt"). Empty on other events (issue #1173).
	NotificationType string `json:"notification_type,omitempty"`
}

// HookTarget is the interface the handler calls into. Satisfied by
// *services.SessionDetector without importing the services package.
type HookTarget interface {
	HandlePermissionHook(sessionID, transcriptPath, hookEventName string)
	// HandleCompactHook forces a session to working for the duration of a
	// manual /compact, whose compaction window writes nothing to the transcript
	// (#657). trigger is the PreCompact cause ("manual" / "auto").
	HandleCompactHook(sessionID, transcriptPath, trigger string)
	// HandleStopHook records the authoritative turn-done signal from Claude
	// Code's Stop hook (#1161). lastAssistantText is the turn's final assistant
	// text, already display-truncated; waitingCue reports whether that message
	// carried a question or imperative cue (computed from the full text so a cue
	// beyond the display tail still routes the turn to waiting, not ready).
	HandleStopHook(sessionID, transcriptPath, lastAssistantText string, waitingCue bool)
	// HandleIdlePromptHook records the Notification/idle_prompt signal — the
	// agent finished its turn and is idle at the prompt waiting for the user
	// (issue #1173). An authoritative waiting signal for the case the prose
	// waiting-cue heuristic can't detect (a turn that ended on a plain statement).
	HandleIdlePromptHook(sessionID, transcriptPath string)
	// HandlePermissionPromptHook records the Notification/permission_prompt
	// signal — a blocking dialog is on screen right now (issue #1861). The only
	// hook signal for a dialog that carries no tool name, so it holds
	// SignalPermissionPrompt directly rather than going through the tool-keyed
	// PermissionRequest path. See handleNotificationHook for what retires it.
	//
	// hookName is the wire event name for the synthetic activity the detector
	// dispatches ("Notification" here). The detector method predates this caller
	// — it was added for gemini-cli's identically-named Notification hook
	// (#1717), and the reason it is a dedicated method rather than a
	// session.HookSignal row is precisely that "Notification" means different
	// things per adapter, which a single flat table cannot express.
	HandlePermissionPromptHook(sessionID, transcriptPath, hookName string)
}

// MarkerTarget is the narrow interface for hook-carried task-estimate
// markers (#604) — the same method shape as the MetricsCollector port, so
// the metrics adapter satisfies it directly (mirrors RateLimitIngester in
// statusline.go). Nil disables the scan (tests).
type MarkerTarget interface {
	IngestTaskEstimate(transcriptPath string, est *session.TaskEstimate)
	IngestTaskSummary(transcriptPath, text string, observedAt int64)
}

// scanToolInput walks the decoded tool_input once, scanning every string value
// for both an in-band task-estimate marker (#604) and a task-summary marker
// (#738) — they share the same carrier (e.g. a Bash description), so a single
// walk picks up both, and they reach the daemon even when the transcript drops
// the surrounding prose. The raw JSON can't be scanned directly: inside a JSON
// string the marker's quotes are escaped (\"marker\") and the captured comment
// body would not unmarshal. Tool inputs are small, shallow objects — the walk
// recurses into nested objects/arrays for completeness. Latest valid of each
// wins, matching the transcript scan.
func scanToolInput(raw json.RawMessage, observedAt time.Time) (*tailer.TaskEstimate, *tailer.TaskSummary, *tailer.TaskQuestion) {
	if len(raw) == 0 {
		return nil, nil, nil
	}
	// Fast reject before decoding. The comment opener survives JSON string
	// escaping (a backslash-quote doesn't touch "<!--"), but HTML-escaping
	// encoders write "<" as a unicode escape — Go's json.Marshal does,
	// Claude Code's JSON.stringify doesn't. Accept both encodings; the
	// raw-string needle below is the escaped opener byte-for-byte.
	htmlEscapedOpener := `\u003c!--`
	if s := string(raw); !strings.Contains(s, "<!--") && !strings.Contains(s, htmlEscapedOpener) {
		return nil, nil, nil
	}
	var decoded interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, nil, nil
	}
	return scanValueForMarkers(decoded, observedAt)
}

// scanValueForMarkers walks a decoded JSON value, scanning every string for
// task-estimate, task-summary and task-question markers; latest valid of each
// wins. Shared by the PostToolUse hook (scanToolInput) and the transcript
// parser so a marker emitted inside a tool input — e.g. the Bash `description`
// carrier (#617) — is found by both the live hook and the transcript/replay
// path. The per-string fast-reject lives inside the Scan* functions.
func scanValueForMarkers(v interface{}, observedAt time.Time) (*tailer.TaskEstimate, *tailer.TaskSummary, *tailer.TaskQuestion) {
	var est *tailer.TaskEstimate
	var sum *tailer.TaskSummary
	var q *tailer.TaskQuestion
	walkMarkerValue(v, observedAt, &est, &sum, &q)
	return est, sum, q
}

// walkMarkerValue is the recursive walk behind scanValueForMarkers, pulled out
// of the closure it used to be so the switch/loops below aren't scored as
// nested inside an enclosing function literal. est/sum/q are accumulated by
// pointer since the walk must propagate a find back up through recursive
// map/slice traversal; latest valid of each wins, matching the transcript scan.
func walkMarkerValue(v interface{}, observedAt time.Time, est **tailer.TaskEstimate, sum **tailer.TaskSummary, q **tailer.TaskQuestion) {
	switch val := v.(type) {
	case string:
		if found := tailer.ScanTaskEstimate(val, observedAt); found != nil {
			*est = found // latest valid wins, matching the transcript scan
		}
		if found := tailer.ScanTaskSummary(val, observedAt); found != nil {
			*sum = found
		}
		if found := tailer.ScanTaskQuestion(val, observedAt); found != nil {
			*q = found
		}
	case map[string]interface{}:
		for _, child := range val {
			walkMarkerValue(child, observedAt, est, sum, q)
		}
	case []interface{}:
		for _, child := range val {
			walkMarkerValue(child, observedAt, est, sum, q)
		}
	}
}

// ConsentGranter is the shared consent check every JSON-hook receiver gates on
// (issue #570) — an alias, not a copy, so this adapter and codex cannot drift
// on it (issue #1179). See hookjson for why the HookTarget above is NOT shared
// the same way.
type ConsentGranter = hookjson.ConsentGranter

// sessionIDFromTranscriptPath extracts irrlicht's session ID (the UUID
// filename stem) from a Claude Code transcript path. The hook payload's
// session_id may differ from the transcript filename, so we always derive
// from the path — matching how fswatcher assigns session IDs.
func sessionIDFromTranscriptPath(p string) string {
	if p == "" {
		return ""
	}
	return strings.TrimSuffix(filepath.Base(p), transcriptExt)
}

// NewHookHandler returns the hook receiver for Claude Code hook events
// (PermissionRequest, PostToolUse, PostToolUseFailure), which dispatches them
// to the target. The returned hookjson.HookHandler is an http.Handler carrying
// the confiner it guards caller-supplied transcript paths with (issue #1390).
//
// The handler returns 200 with an empty body for recognized events. For
// PermissionRequest, an empty response means Claude Code shows its normal
// permission prompt (no auto-approve/deny).
//
// markers receives task-estimate markers found in PreToolUse tool inputs
// (#604) — a transport that bypasses the transcript writer, which drops
// mid-task assistant text on claude ≥2.1.162. The marker scan runs for ALL
// tools; the permission dispatch below stays strictly gated to the
// user-input tools — two independent paths, not a relaxation of the gate.
//
// gate is the consent check for the "hooks" permission; while not granted
// the payload is dropped with 200 (so the curl hook stays quiet). A nil
// gate means no gating — used by tests.
func NewHookHandler(target HookTarget, markers MarkerTarget, gate ConsentGranter, log outbound.Logger) hookjson.HookHandler {
	return hookjson.NewHandler(transcriptConfiner(),
		hookjson.RequireConsent(gate, AdapterName, PermissionKeyHooks, PermissionKeyTranscripts),
		func(consent hookjson.Consent, c *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) {
			serveHookRequest(target, markers, consent, log, c, w, r)
		})
}

// transcriptConfiner returns the confiner this adapter's hook receiver guards
// caller-supplied transcript paths with, rooted in the adapter's own
// agent.Source declaration so it cannot drift from the tree the daemon watches
// (issue #1361).
func transcriptConfiner() *hookjson.PathConfiner {
	return hookjson.ConfinerForSource(Source, runtime.GOOS, transcriptExt)
}

// admitHookRequest applies both consents this receiver needs and records the
// channel receipt between them. It answers the request itself on refusal and
// returns false; a true means the caller may proceed to read the body.
//
// All three steps live here together because their ORDER is the contract, and
// each boundary is load-bearing in a different direction:
//
//   - The "hooks" consent comes first. It authorizes WRITING our entries into
//     the user's settings.json — which is what makes a POST arrive at all.
//   - The receipt is counted between them, so a hooks-granted /
//     transcripts-denied install still reads as a LIVE channel. #1368's
//     liveness watchdog demotes a channel that produces no receipts and
//     releases the holds it placed; counting only fully-granted requests would
//     have it falsely accuse a working install of being dead. See receipt.go
//     for why each later rejection still counts and why a consent-denied
//     request must not.
//   - The "transcripts" consent comes last, and still BEFORE the decode and so
//     before confinement, so a denied session yields a quiet 200 and the path
//     is never resolved on that branch. Dispatching makes the detector open
//     the transcript, so that read needs its own consent: granting "hooks" is
//     not a licence to read the file those entries point at.
//
// codex and copilot have carried the transcripts gate since #1174 and mirror
// each other step for step; claudecode — the receiver the other two were
// modelled on — never got it, so a hook POST reached the detector's open on a
// transcript the user had denied (issue #1466). A hooks-granted /
// transcripts-denied session is not monitored anyway, so dropping the hook
// here is both consent-correct and behaviourally harmless.
//
// Both keys are asked of the receiver's OWN hookjson.Consent (issue #1488), so
// the pair this order is written for is the pair the handler publishes and the
// contract derives — not a second list that could drift from it. A key this
// receiver did not declare answers false, so the drift fails closed rather than
// silently gating on nothing.
func admitHookRequest(consent hookjson.Consent, w http.ResponseWriter) bool {
	if !consent.Granted(PermissionKeyHooks) {
		w.WriteHeader(http.StatusOK)
		return false
	}

	hookjson.ObserveHookReceipt(AdapterName)

	if !consent.Granted(PermissionKeyTranscripts) {
		w.WriteHeader(http.StatusOK)
		return false
	}
	return true
}

// serveHookRequest is NewHookHandler's request logic, pulled out of the
// returned closure so its branching isn't counted at the closure's extra
// nesting depth (go:S3776 — this dropped the reported complexity from 31 to
// within the 15-point budget without changing any behavior).
func serveHookRequest(target HookTarget, markers MarkerTarget, consent hookjson.Consent, log outbound.Logger, confiner *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !admitHookRequest(consent, w) {
		return
	}

	// transcript_path arrives in an HTTP body on a local, unauthenticated
	// endpoint, so it is untrusted: every dispatch below hands it to the
	// detector, which opens it. DecodeConfined reads the body and confines the
	// path in one step, so this receiver cannot reach its payload without
	// having supplied the confiner — and the caller's raw string is overwritten
	// with the confined one, so nothing below can pick it back up (issues
	// #1361, #1389). It has already answered the request when it returns false.
	var payload hookPayload
	if !hookjson.DecodeConfined(w, r, log, logComponentHookReceiver, consent, confiner, &payload,
		func(p *hookPayload) string { return p.TranscriptPath },
		func(p *hookPayload, confined string) { p.TranscriptPath = confined },
	) {
		return
	}
	transcriptPath := payload.TranscriptPath

	// Confinement already rejected an empty or non-.jsonl path, so the only
	// input still reaching this is a basename that is bare ".jsonl" — an empty
	// session id, which nothing downstream can key on.
	sessionID := sessionIDFromTranscriptPath(transcriptPath)
	if sessionID == "" {
		http.Error(w, "bad request: transcript_path has no session id", http.StatusBadRequest)
		return
	}

	dispatch := func() {
		log.LogInfo(logComponentHookReceiver, sessionID,
			fmt.Sprintf("received %s (tool=%s)", payload.HookEventName, payload.ToolName))
		target.HandlePermissionHook(sessionID, payload.TranscriptPath, payload.HookEventName)
	}

	switch payload.HookEventName {
	case HookPermissionRequest, HookPostToolUse, HookPostToolUseFailure:
		dispatch()
	case HookPreCompact:
		handlePreCompactHook(target, log, sessionID, payload)
	case HookPreToolUse:
		handlePreToolUseHook(markers, log, sessionID, payload, dispatch)
	case HookStop:
		handleStopHook(target, log, sessionID, payload)
	case HookNotification:
		handleNotificationHook(target, log, sessionID, payload)
	default:
		// Unrecognized hook event — accept but ignore, counted per name and
		// reported once, in the one place that behaviour is decided for every
		// receiver (issue #1364). The 200 below is unaffected.
		hookjson.IgnoreUnknownEvent(log, logComponentHookReceiver, AdapterName, sessionID, payload.HookEventName)
	}

	w.WriteHeader(http.StatusOK)
}

// handlePreCompactHook processes a PreCompact hook event. A manual /compact
// replaces the context; the compaction window (tens of seconds to minutes)
// writes nothing to the transcript, so without this hook the session stays
// frozen in its pre-compact state instead of showing working (#657). Force
// working now; the compact_boundary then releases it back to ready (#656).
// The installer matches "manual"; the trigger check here is defense-in-depth
// so an auto-compaction (already working, fires mid-turn) never gets a
// spurious working blip.
func handlePreCompactHook(target HookTarget, log outbound.Logger, sessionID string, payload hookPayload) {
	if payload.Trigger == compactTriggerManual {
		log.LogInfo(logComponentHookReceiver, sessionID,
			fmt.Sprintf("received %s (trigger=%s)", payload.HookEventName, payload.Trigger))
		target.HandleCompactHook(sessionID, payload.TranscriptPath, payload.Trigger)
	} else {
		log.LogInfo(logComponentHookReceiver, sessionID,
			fmt.Sprintf("ignored %s (trigger=%q, not manual)", payload.HookEventName, payload.Trigger))
	}
}

// handleStopHook processes a Claude Code Stop hook — the authoritative
// turn-done push delivered at true turn end (#1161). It forwards a turn-done
// signal plus the turn's final assistant text so the classifier decides
// ready-vs-waiting from the same message IsWaitingForUserInput reads, without
// depending on the transcript-tail heuristic (and its codex carve-out).
//
// The forwarded text is display-truncated; the waiting-cue verdict is computed
// from a bounded tail window (WaitingScanWindow) of the FULL message — the same
// window parser.go uses for PendingWaitingCue — so a question or cue sitting
// before the display tail still routes the turn to waiting, while ExtractWaitingCue
// is not fed the whole (possibly very long) turn, where it over-fires.
func handleStopHook(target HookTarget, log outbound.Logger, sessionID string, payload hookPayload) {
	log.LogInfo(logComponentHookReceiver, sessionID,
		fmt.Sprintf("received %s (%d chars of assistant text)", payload.HookEventName, len(payload.LastAssistantMessage)))

	target.HandleStopHook(sessionID, payload.TranscriptPath,
		tailer.TruncateAssistantText(payload.LastAssistantMessage),
		session.ProseIndicatesWaiting(tailer.WaitingScanWindow(payload.LastAssistantMessage)))
}

// handleNotificationHook processes a Claude Code Notification hook, whose
// notification_type discriminates two states worth acting on and a dozen that
// are not. Anything outside notificationTypesDrivingState is accepted and
// ignored, mirroring handlePreToolUseHook's defense-in-depth reject: the
// installer's matcher already narrows the delivery, but the handler re-checks
// so a hand-broadened settings.json matcher cannot dispatch a type this code
// has never seen.
//
//   - idle_prompt → HandleIdlePromptHook. The agent finished its turn and is
//     idle at the prompt waiting for the user (issue #1173).
//   - every other type in notificationTypesDrivingState →
//     HandlePermissionPromptHook. A dialog is open and the user is blocked on
//     it (issue #1861). See that map for why the split is not cosmetic.
//
// WHY THESE ARE HANDLED HERE AT ALL, given PermissionRequest exists.
// PermissionRequest is keyed on a tool name (WQn returns `e.tool_name` for it),
// and Claude Code renders a whole class of blocking dialog that carries none —
// sandbox network access, the auto-mode dialogs, managed-settings review, MCP
// elicitation, a blocked sub-agent. No PermissionRequest fires for any of them
// at any matcher width; this notification is their only hook signal.
//
// HOW RELIABLY IT ARRIVES — weaker than "it fires", and the limit is worth
// knowing. The interactive emitter is not the SDK transport's one-shot
// `setTimeout(…, 6000)` (that one belongs to the stream-json host and is not
// the path an interactive session takes). It is `GUe`, which polls on a
// REPEATING 6s chain and, on each tick, emits only if
// `Date.now() - max(lastInteraction, mount) >= 6000`. So:
//
//   - it repeats while the dialog stays up, which is harmless here — a re-Hold
//     only refreshes HeldSince on a hold that is already correct;
//   - it is SUPPRESSED while the user keeps interacting. A user who arrows
//     through a dialog every few seconds may never trigger it, and the session
//     keeps reading `working`. That fails open — the same way it did before
//     #1861 — so it bounds how much this fix delivers rather than breaking it;
//   - nothing at all is emitted when the dialog CLOSES. There is no "dismissed"
//     notification to release on, which is the whole reason the hold below
//     needs a clearing edge from somewhere else.
//
// WHAT CLEARS THE HOLD IT PLACES. Deliberately the SAME SignalPermissionPrompt
// kind as the tool-keyed path, so it inherits every clearing edge that path
// already has: PostToolUse / PostToolUseFailure release it (and with hookMatcher
// now match-all that fires for every tool, covering every dialog raised during a
// tool call — the elicitation and sandbox-network cases included); a transcript
// tool-denial marks it stale; the Stop hook retires it at a turn boundary with
// no tool open (#1861); and permissionPromptHoldTimeout backstops the rest.
func handleNotificationHook(target HookTarget, log outbound.Logger, sessionID string, payload hookPayload) {
	route, drivesState := notificationTypesDrivingState[payload.NotificationType]
	if !drivesState {
		log.LogInfo(logComponentHookReceiver, sessionID,
			fmt.Sprintf("ignored Notification of type %q (not one of the types that drive state)",
				payload.NotificationType))
		return
	}
	log.LogInfo(logComponentHookReceiver, sessionID,
		fmt.Sprintf("received %s (type=%s)", payload.HookEventName, payload.NotificationType))
	switch route {
	case routeBlockingDialog:
		// Discriminated wire name, not a bare "Notification". Both routes
		// record a lifecycle KindHookReceived, and they hold DIFFERENT signals,
		// so a single shared name would make the two indistinguishable in the
		// trace and in every recording — which is exactly the ambiguity
		// hook_signal.go's "no HookNotification row" note depends on not
		// existing.
		target.HandlePermissionPromptHook(sessionID, payload.TranscriptPath,
			payload.HookEventName+"/"+payload.NotificationType)
	case routeIdlePrompt:
		target.HandleIdlePromptHook(sessionID, payload.TranscriptPath)
	}
}

// handlePreToolUseHook processes a PreToolUse hook event: scans the tool
// input for task-estimate/task-summary markers for ALL tools (#604), then
// dispatches (via dispatch) only for the user-input tools (AskUserQuestion /
// ExitPlanMode) — rejecting anything else even if the settings.json matcher
// was misconfigured to be broader.
func handlePreToolUseHook(markers MarkerTarget, log outbound.Logger, sessionID string, payload hookPayload, dispatch func()) {
	// Marker scan first, for ALL tools (#604): the rules block lets
	// the agent carry its progress marker in a tool input (e.g. the
	// Bash description), and the payload reaches the daemon even
	// when the transcript drops the surrounding prose.
	if markers != nil {
		scanAndIngestMarkers(markers, log, sessionID, payload)
	}
	if payload.ToolName == toolAskUserQuestion || payload.ToolName == toolExitPlanMode {
		dispatch()
	} else {
		log.LogInfo(logComponentHookReceiver, sessionID,
			fmt.Sprintf("ignored PreToolUse for unexpected tool %q", payload.ToolName))
	}
}

// scanAndIngestMarkers scans a PreToolUse payload's tool input for
// task-estimate (#604) and task-summary (#738) markers and forwards any found
// to markers. The question marker rides end-of-turn assistant prose, not a
// tool-input carrier, so the hook path (tool inputs only) drops it (#759):
// the transcript text-block scan delivers it, and the deterministic
// compactor covers the no-marker case regardless.
func scanAndIngestMarkers(markers MarkerTarget, log outbound.Logger, sessionID string, payload hookPayload) {
	est, sum, _ := scanToolInput(payload.ToolInput, time.Now())
	if est != nil {
		log.LogInfo(logComponentHookReceiver, sessionID,
			fmt.Sprintf("task-estimate marker via %s tool input: %d/%d", payload.ToolName, est.CompletedRounds, est.TotalRounds))
		markers.IngestTaskEstimate(payload.TranscriptPath, &session.TaskEstimate{
			TotalRounds:     est.TotalRounds,
			CompletedRounds: est.CompletedRounds,
			Risk:            est.Risk,
			Confidence:      est.Confidence,
			UpdatedAt:       est.ObservedAt,
		})
	}
	if sum != nil {
		log.LogInfo(logComponentHookReceiver, sessionID,
			fmt.Sprintf("task-summary marker via %s tool input", payload.ToolName))
		markers.IngestTaskSummary(payload.TranscriptPath, sum.Text, sum.ObservedAt)
	}
}
