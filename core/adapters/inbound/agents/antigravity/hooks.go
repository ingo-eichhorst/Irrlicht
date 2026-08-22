// hooks.go provides the HTTP handler for receiving antigravity lifecycle
// events (issue #1723, epic #1355).
//
// Exactly one event is installed and dispatched: Stop, routed to
// HandleStopHook with an empty text and a false waiting cue. See
// hookinstaller.go's installedHookEvents for what is deliberately NOT
// installed and why.
//
// # What the turn-end edge buys, measured rather than argued
//
// Before this channel, antigravity's only turn-end signal was the transcript
// tail: parser.go maps a `PLANNER_RESPONSE` carrying no `tool_calls` to
// turn_done, and the classifier's total default is "transcript activity →
// working" — there is no idle-based demotion anywhere in
// services.stateRules. So a conversation whose last parser-visible line is
// anything else leaves the session pinned `working` indefinitely.
//
// That is not a corner case. Measured over the 103 real conversations in this
// machine's CLI brain store, classifying each transcript's last line the way
// parser.ParseLine does:
//
//	85  turn_done                     (PLANNER_RESPONSE, no tool_calls)
//	14  assistant_message             (PLANNER_RESPONSE WITH tool_calls)
//	 2  user_message                  (USER_INPUT)
//	 1  function_call_output          (GENERIC)
//	 1  function_call_output          (LIST_DIRECTORY)
//
// 18 of 103 — 17.5% — never reach turn_done from the transcript at all. The
// hook's Stop closes that for every turn where it fires.
//
// That is a measurement of ONE machine's store on 2026-08-22, not a property
// of antigravity, and it is not pinned by any test — nothing in the repo can
// hold a figure taken from a developer's own home directory honest. It is
// reproduced read-only, with no daemon and no live turn, by
// tools/antigravity-transcript-tail-census.sh, which REFUSES rather than
// reporting zeros when the brain store is absent or holds no transcript.
//
// # Stop does NOT fire on every turn end, and that is stated rather than
// # discovered later
//
// #1723's probes measured a turn that terminated on a DENIED tool call firing
// `PostToolUse` three times and `Stop` **zero** times, while a normally
// completing turn fired Stop once. The negative is trustworthy because the
// same hooks.json registered both events, so PostToolUse was a positive
// control: the file demonstrably loaded on the run where Stop produced
// nothing.
//
// The decision this adapter takes, deliberately: **nothing here compensates
// for it.** The transcript path is untouched and remains the only other
// turn-end source, so on a denial-terminated turn the session is left exactly
// as pinned as it is today — the hook adds no new failure mode, it simply does
// not close that particular one. Three alternatives were considered and
// rejected:
//
//   - Installing PostToolUse as a proxy for "the turn is probably over" would
//     spawn a beacon per tool call to assert something no tool call implies.
//   - Holding a timer that reports turn-end after N seconds of hook silence
//     invents a signal from an absence, and the absence is indistinguishable
//     from a long-running tool.
//   - Treating the transcript fallback as covering it is what the census above
//     refutes.
//
// So the honest position is that antigravity gains an authoritative turn end
// on the paths where antigravity emits one, and keeps its existing (measured
// incomplete) transcript inference everywhere else.
//
// # Why the payload names no path this receiver reads
// # (contracttesting.PathDaemonDerived)
//
// Every hook payload antigravity sends DOES carry a `transcriptPath` — and it
// names `transcript_full.jsonl`, the unfiltered view, which is precisely the
// file this adapter refuses to own: adapter.go's sessionIDFromPath accepts
// only `transcript.jsonl` and returns "" for its sibling, so that a
// conversation mints exactly one session rather than two. A path the adapter
// cannot key a session on is a path there is no reason to accept from a local,
// unauthenticated endpoint, so this receiver does not decode the field at all.
//
// What it uses instead is `conversationId`, which the probes established IS
// this adapter's session id — it is the brain directory name, which is exactly
// what sessionIDFromPath derives. The path every consumer keys the session on
// is then composed by the daemon from its own declared roots
// (transcriptPathFor), so the hook's key and the fswatcher's key cannot drift
// into two sessions. TestTranscriptPathForRoundTripsThroughSessionIDFromPath
// pins that equality rather than leaving it to inspection.
//
// Two further facts from the same captures make cwd-based attribution
// impossible and are recorded so nobody tries: the hook's working directory is
// `~/.gemini/config` (the directory containing hooks.json), not the agent's
// workspace, and `workspacePaths` came back `[]` on every capture.
//
// # Fields deliberately not branched on
//
// The Stop payload carries `terminationReason` and `fullyIdle`. Neither
// influences anything here; both are logged only. The doc's documented value
// set for terminationReason (`model_stop`, `max_steps_exceeded`, `error`) is
// WRONG — the observed value is `NO_TOOL_CALL`, uppercase and absent from that
// list — so a matcher built from it would fail closed on the only value anyone
// has actually seen. There is no matcher: every Stop is a turn end.
// TestStopHook_DispatchesForAnyTerminationReason locks that in both
// directions.
//
// `modelName` is also deliberately ignored. It came back
// `gemini-3.5-flash-extra-low` on a CLI invoked with
// `--model gemini-3.5-flash-low`, so something between the flag and the hook
// rewrites it; it is not the user's selected model and must not be shown as
// one. The model this adapter displays continues to come from the transcript's
// <USER_SETTINGS_CHANGE> block and the sibling conversation store.
package antigravity

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/ports/outbound"
)

// PermissionKeyHooks gates installing and receiving antigravity lifecycle
// events (issue #570). Aliased to the shared domain constant rather than
// restated — see agent.HooksPermissionKey's own doc for why two external
// projections narrow on it.
const PermissionKeyHooks = agent.HooksPermissionKey

// logComponentHookReceiver is the Logger component tag for every log line this
// receiver emits.
const logComponentHookReceiver = "antigravity-hook-receiver"

// conversationIDRE is the accepted shape of the conversation id the payload
// carries. Every id observed is a UUID, but this is deliberately WIDER than a
// UUID pattern: the id is antigravity's to change, and a receiver that fails
// closed on a format change would silently stop reporting turn ends for a
// reason no log names.
//
// What it must reject is narrower and non-negotiable: the id is joined into a
// filesystem path by transcriptPathFor, so it has to be a single safe path
// segment. No separator (either platform's), no leading dot — which excludes
// "." and ".." outright — no control characters, no whitespace, and a length
// bound. The containment that follows is the stat in transcriptPathFor, but
// this is what stops a hostile id from being turned into a path at all.
var conversationIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// antigravityHookPayload is the JSON body `irrlichd hook-post antigravity`
// forwards verbatim from antigravity's own hook stdin.
//
// It is a deliberately NARROW view of a wider payload. Antigravity sends
// camelCase protojson with `conversationId`, `workspacePaths`,
// `transcriptPath`, `artifactDirectoryPath`, `modelName`, plus per-event
// fields; encoding/json drops every key with no field here, so the fields NOT
// listed are the security property rather than an omission — see this file's
// header on why `transcriptPath` in particular must not travel.
type antigravityHookPayload struct {
	// HookEventName is irrlicht's OWN field, and antigravity never sends it:
	// its payload identifies the event by which config key the handler was
	// registered under, not by a field. An absent value therefore means "the
	// one event this adapter installs" — see eventOf, and the lock
	// TestEventOf_DefaultIsSoundOnlyWhileOneEventIsInstalled, which is what
	// stops that default from silently becoming wrong if a second event is
	// ever added.
	HookEventName string `json:"hook_event_name"`

	// ConversationID is antigravity's own conversation id — the brain
	// directory name, and this adapter's session id. It is the ONLY handle in
	// the payload: cwd is ~/.gemini/config and workspacePaths was empty on
	// every capture.
	ConversationID string `json:"conversationId"`

	// TerminationReason and FullyIdle are logged and never branched on. See
	// this file's header.
	TerminationReason string `json:"terminationReason"`
	FullyIdle         bool   `json:"fullyIdle"`
}

// HookTarget is the interface the handler calls into. Satisfied by
// *services.SessionDetector without importing the services package — the same
// agent-agnostic surface every other receiver uses, narrowed to the one method
// this adapter's single installed event needs.
type HookTarget interface {
	HandleStopHook(sessionID, transcriptPath, lastAssistantText string, waitingCue bool)
}

// ConsentGranter is the shared consent check every JSON-hook receiver gates on
// (issue #570) — an alias, not a copy, so the receivers cannot drift.
type ConsentGranter = hookjson.ConsentGranter

// NewHookHandler returns an http.Handler that receives antigravity lifecycle
// events and dispatches them to the target.
//
// gate is the consent check; while the "hooks" permission is not granted the
// payload is dropped with 200, so an install left behind by a pre-consent
// daemon stays quiet. A nil gate means no gating — used by tests.
//
// The returned hookjson.HookHandler carries a NIL Confiner, and that is the
// declaration rather than an omission: this receiver reads no caller-supplied
// path, and a nil confiner is what makes hookjson.DecodeConfined fail CLOSED
// if a later edit switched this receiver back to the confining decode without
// giving the adapter roots. AssertHookPathConfined's PathDaemonDerived route
// asserts it.
func NewHookHandler(target HookTarget, gate ConsentGranter, log outbound.Logger) hookjson.HookHandler {
	return hookjson.NewHandler(nil,
		hookjson.RequireConsent(gate, AdapterName, PermissionKeyHooks, PermissionKeyTranscripts),
		func(consent hookjson.Consent, _ *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) {
			serveHookRequest(target, consent, log, w, r)
		})
}

// serveHookRequest is NewHookHandler's request logic. The step order matches
// every sibling receiver's exactly, and each step is load-bearing — see the
// contract assertions in hook*_test.go.
func serveHookRequest(target HookTarget, consent hookjson.Consent, log outbound.Logger, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Both keys come off the receiver's OWN hookjson.Consent (issue #1488), so
	// the set this sequence is written for is the set the handler publishes and
	// the contract derives.
	if !consent.Granted(PermissionKeyHooks) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// The channel delivered (issue #1368). Counted against the "hooks" consent
	// only, ahead of the transcripts check below.
	hookjson.ObserveHookReceipt(AdapterName)

	// Dispatching makes the detector read the transcript (and the sibling
	// conversation store), so it is gated behind "transcripts" — this adapter's
	// read permission — not merely the "hooks" write consent. The check comes
	// BEFORE the decode, so a denied session still yields a quiet 200 and the
	// body is never read.
	if !consent.Granted(PermissionKeyTranscripts) {
		w.WriteHeader(http.StatusOK)
		return
	}

	var payload antigravityHookPayload
	if !hookjson.DecodeSealed(w, r, log, logComponentHookReceiver, consent, &payload) {
		return
	}

	sessionID := payload.ConversationID
	if !conversationIDRE.MatchString(sessionID) {
		// Not an id this adapter could key a session on — drop rather than
		// invent one, and never join it into a path. The transcript tailer
		// covers the state regardless.
		w.WriteHeader(http.StatusOK)
		return
	}

	// Composed by the daemon from its own declared roots, never from the body.
	transcriptPath := transcriptPathFor(sessionID)

	switch eventOf(payload) {
	case HookEventStop:
		log.LogInfo(logComponentHookReceiver, sessionID,
			"received Stop (terminationReason="+payload.TerminationReason+
				", fullyIdle="+boolText(payload.FullyIdle)+")")
		// Payload-free, exactly as pi's, vibe's and opencode's single turn-end
		// events are: HandleStopHook asserts SignalTurnDone unconditionally and
		// only CONDITIONALLY overlays the text and cue, so the authoritative
		// "the turn truly ended" claim is real without them, and it can never
		// clear or mask a cue the transcript found on its own. Antigravity's
		// Stop payload carries no assistant text to forward.
		target.HandleStopHook(sessionID, transcriptPath, "", false)
	default:
		// Unrecognized event — accept but ignore, counted per name and reported
		// once, in the one place that behaviour is decided for every receiver
		// (issue #1364). This is where an antigravity event rename, or a
		// handler a user pointed at our beacon from a config key this daemon
		// does not know, becomes visible instead of silent.
		hookjson.IgnoreUnknownEvent(log, logComponentHookReceiver, AdapterName, sessionID, payload.HookEventName)
	}
	w.WriteHeader(http.StatusOK)
}

// eventOf resolves which antigravity event a body reports.
//
// Antigravity's payload names no event — the event is implied by the config
// key the handler was registered under, and the beacon forwards the body
// verbatim without adding to it — so an absent name means the sole event this
// adapter registers. A body that DOES name one is taken at its word, which is
// what keeps the #1364 unrecognized-event path reachable and what would make a
// future antigravity that started sending an event name work unchanged.
//
// The default is sound only while exactly one event is installed.
// TestEventOf_DefaultIsSoundOnlyWhileOneEventIsInstalled is the guard; it is a
// lock, and it fails the moment installedHookEvents grows.
func eventOf(payload antigravityHookPayload) string {
	if payload.HookEventName != "" {
		return payload.HookEventName
	}
	return installedHookEvents[0]
}

// boolText renders a bool for a log line without pulling in strconv for one
// call site.
func boolText(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// transcriptPathFor composes the transcript path a conversation id names,
// using the adapter's OWN declared brain stores rather than anything a caller
// supplied.
//
// One adapter covers two surfaces, so there are two roots to choose between.
// The choice is made by looking for the conversation directory: the CLI store
// first, then the IDE store, falling back to the CLI spelling when neither
// exists yet. That fallback is what makes the function total and deterministic
// — a hook can arrive before the daemon has ever seen the directory, and a
// receiver that answered "" there would drop a real turn end.
//
// It returns the FILTERED transcript.jsonl, which is the file the fswatcher
// tails and the only one sessionIDFromPath accepts — deliberately NOT the
// transcript_full.jsonl the hook payload names.
func transcriptPathFor(conversationID string) string {
	for _, brain := range []string{cliBrainDir, ideBrainDir} {
		root, err := agentpaths.AbsRoot(brain)
		if err != nil {
			continue
		}
		dir := filepath.Join(root, conversationID)
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return conversationTranscriptPath(dir)
		}
	}
	root, err := agentpaths.AbsRoot(cliBrainDir)
	if err != nil {
		return ""
	}
	return conversationTranscriptPath(filepath.Join(root, conversationID))
}

// conversationTranscriptPath is the layout half of transcriptPathFor, spelled
// from the same constants sessionIDFromPath walks back up, so the composer and
// the parser cannot disagree about where a transcript lives.
func conversationTranscriptPath(conversationDir string) string {
	return filepath.Join(conversationDir, systemGeneratedDirName, logsDirName, transcriptFilename)
}
