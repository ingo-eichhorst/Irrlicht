// hookinstaller.go manages Claude Code hook entries in ~/.claude/settings.json
// for the irrlicht daemon. It installs PermissionRequest, PreToolUse,
// PostToolUse, PostToolUseFailure, PreCompact, Stop and Notification hooks that
// POST to the daemon's HTTP endpoint via native `type: http` delivery, and can
// remove them cleanly. (Issues #108, #307, #657, #1161, #1173.)
package claudecode

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/atomicfile"
	"irrlicht/core/pkg/daemonaddr"
)

// HookEndpointPath is the daemon's Claude Code hook path. Host and port are
// resolved at install time from the daemon's own bind address, so a daemon on
// an alternate port installs hooks that reach it rather than whatever owns
// :7837 (issue #1178).
//
// Exported so the daemon registers its route from the same constant the
// installer writes and matches on. Since #1178 this path *is* the sentinel, so
// drift between route and installer would not 404 — it would stop recognizing
// every existing entry as ours.
const HookEndpointPath = "/api/v1/hooks/claudecode"

// hookSentinel is the substring in the hook entry (curl command or http url)
// that identifies irrlicht-managed entries. Used by both install (idempotency
// check) and uninstall, and by the delivery-form migration. It is deliberately
// port-independent: a sentinel carrying a port would stop recognizing our own
// entries the moment the daemon moves, orphaning them and appending a
// duplicate group instead of upgrading in place (#1178). As the bare endpoint
// path it is a substring of the legacy curl command, of a pre-#1178 :7837
// url, and of the current url alike.
//
// It is also a prefix of statuslineSentinel, which is harmless: hook entries
// and statusLine.command live under disjoint keys of settings.json and are
// never matched against each other.
const hookSentinel = HookEndpointPath

// hookEndpointURL is the daemon endpoint the installed hook posts to. Claude
// Code delivers the hook payload as a JSON POST body directly to this URL via
// its native `type: http` hook — no shell, no curl (issue #1161). Removing the
// curl dependency means hook delivery no longer silently no-ops when curl is
// missing from PATH, which was the failure mode the OpenToolStalled transcript
// fallback (#488) exists to cover; that fallback is retained (it still covers a
// down/unreachable daemon), but its primary trigger is now gone.
func hookEndpointURL() string {
	return daemonaddr.LocalURL(HookEndpointPath)
}

// hookTimeoutSeconds bounds how long Claude Code waits on the daemon before
// giving up on a hook delivery. The daemon's handler is near-instant (an
// in-memory map write plus a channel send) and a down daemon fails the
// connection immediately, so this is only a safety ceiling against a wedged
// daemon — well below Claude Code's 600s default so a hook can never stall a
// turn.
const hookTimeoutSeconds = 5

// hookMatcher is the matcher used by PermissionRequest, PostToolUse, and
// PostToolUseFailure. It matches every tool.
//
// THE SPELLING IS "*", NOT ".*", and that is a measured choice rather than a
// stylistic one. Claude Code decides delivery in bCt(query, matcher, …), read
// in the 2.1.259 bundle:
//
//	if(!n||n==="*")return!0;                       // literal short-circuit
//	let y=Wcr(n,r,o); …                            // exact-name list, only if
//	                                               // n matches /^[a-zA-Z0-9_|, -]+$/
//	try{ let C=new RegExp(n); if(C.test(e))… }     // otherwise: compile + test
//
// Only "" and "*" short-circuit. ".*" contains characters Wcr's class rejects,
// so it falls through to `new RegExp(".*")` — correct, but it compiles and
// tests a regexp on every hook evaluation, which since this matcher went
// match-all means every tool call. "*" skips both.
//
// (Do NOT justify this from `matcherIsMatchAll = !m || m==="*" || m===".*"`,
// which also exists in the bundle and looks authoritative. It is read in
// exactly two places, both `s("tengu_run_hook", {numMatchAllMatchers, …})`
// analytics calls, and decides no delivery at all. An earlier revision of this
// comment cited it and was wrong.)
//
// WHY NOT AN ALLOWLIST (#1861). Until #1861 this was a nine-alternative list of
// tool names — Bash|Write|Edit|MultiEdit|NotebookEdit|WebFetch|mcp__.*|
// AskUserQuestion|ExitPlanMode. Claude Code matches it against the TOOL NAME
// (executePermissionRequestHooks passes matchQuery: <tool name>), so a
// permission modal on any other tool delivered no hook at all and the session
// kept reading `working` behind an overlay the user was blocked on. Its tool
// set also carries Read, Glob, Grep, LS, WebSearch, KillShell, PowerShell,
// Tmux, Monitor, REPL, Skill, Task and SlashCommand, and it grows every
// release — so the list was not merely missing entries, it was the wrong shape.
// The issue's own reproduction is a `permissions.ask` rule on Read.
//
// WHY A BROAD MATCH IS SAFE HERE, WHICH IT IS NOT FOR PreToolUse.
// matcherForEvent hands this one constant to all three events, so the ARMING
// edge (PermissionRequest holds SignalPermissionPrompt) and the CLEARING edges
// (PostToolUse / PostToolUseFailure release it) widen together by construction
// rather than by anyone remembering to. That symmetry is the whole safety
// argument: a tool that can arm a hold nothing can clear pins the session at
// waiting until permissionPromptHoldTimeout's 12-hour ceiling. PermissionRequest
// is not a per-tool-call event — Claude Code raises it only when a permission
// decision is actually needed — so a match-all ASSERT here does not hold
// `waiting` on every tool call, which is exactly why hookMatcherPreToolUse below
// must stay narrow.
//
// WHAT THE BROAD RELEASE COSTS, measured rather than waved past. The Release
// itself is genuinely free — releasing an unheld signal is a no-op
// (session.SignalHolds.Release), the same argument gemini-cli's
// fires-on-every-tool AfterTool row records in hook_signal.go. The DELIVERIES
// it justifies are not free: PostToolUse now arrives for every tool rather than
// for nine names, and each POST costs one path confine (two
// filepath.EvalSymlinks, ~12µs), one log line (~9µs under the logger's global
// mutex), and one forced classify pass that bypasses the 2s activity debounce
// and unconditionally rewrites the session ledger (~450µs). Call it ~0.5ms per
// tool call, dominated by the ledger write. Figures are from scratch benchmarks
// on the maintainer's machine (warm), not a committed benchmark — treat them as
// an order of magnitude, not a contract.
//
// That is judged worth it, because the alternative is asymmetry: arming wider
// than clearing is what pins a session at `waiting`. The ledger rewrite is
// unconditional even on a zero-byte pass and is the obvious thing to make
// conditional; it is filed rather than fixed here because it lives in another
// package and already fires this way for gemini-cli's AfterTool.
//
// ONE TOOL IS WORTH CALLING OUT: Task. Its PostToolUse — the release — does not
// fire until the SUB-AGENT finishes, which is minutes, so an `ask` rule on Task
// leaves the session reading `waiting` from the user's approval until the
// sub-agent returns. That is the same shape a long-running Bash approval
// already had before #1861 (Bash was in the old allowlist), so it is not new in
// kind; it is newly reachable for the longest-running tool in the set, and
// claudecode has no transcript-tier re-derivation to correct it —
// SignalOpenToolStalled covers only the five edit/write names.
const hookMatcher = "*"

// hookMatcherPreToolUse is the narrow matcher for the PreToolUse event. We
// only want to flip working→waiting on the user-input tools — matching every
// Bash/Write/… would hold SignalPermissionPrompt on every tool call. (Issue #307.)
const hookMatcherPreToolUse = "AskUserQuestion|ExitPlanMode"

// hookMatcherPreCompact is the matcher for the PreCompact event. Unlike the
// other events (whose matcher is a tool-name regex), Claude Code's PreCompact
// matcher matches the compaction *trigger* — "manual" or "auto". We install
// "manual" so the hook fires only for a user-invoked /compact, gating at the
// source: auto-compaction fires mid-turn while the session is already working,
// so forcing working there would be a spurious blip (#657).
const hookMatcherPreCompact = "manual"

// hookMatcherNotification is the matcher for the Notification event. Like
// PreCompact (whose matcher is the compaction trigger), Claude Code's
// Notification matcher matches the notification_type
// (`case"Notification":return e.notification_type` in the bundle's WQn), not a
// tool name. It stays an explicit list rather than going match-all like
// hookMatcher above: an unlisted type is a POST the handler exists only to
// drop, and the list is short.
//
// THE AUTHORITATIVE SET OF TYPES is the bundle's own `odr` array, which is what
// this list must be diffed against when Claude Code updates — not a list of
// dialog names, which is a different vocabulary and the mistake #1861's first
// revision made (it listed `mcp_elicitation` / `mcp_url_elicitation`, which are
// DIALOG kinds; the notification types they emit are `elicitation_dialog` and
// `elicitation_url_dialog`, and neither was installed). As of 2.1.259:
//
//	permission_prompt, idle_prompt, auth_success, elicitation_dialog,
//	agent_needs_input, agent_completed, elicitation_url_dialog,
//	worker_permission_prompt, push_notification, computer_use_enter,
//	computer_use_exit, quota_auto_resume_{fired,stale,disabled}
//
// INSTALLED, because each means a human is blocked:
//
//   - idle_prompt: the turn ended and the agent is idle at the prompt (#1173).
//   - permission_prompt: a blocking dialog is on screen (#1861). It is also the
//     DEFAULT for any dialog notification that sets no type of its own
//     (`GUe(n.text, n.type ?? "permission_prompt", …)`), which is what makes it
//     cover the dialogs that carry no tool name and so raise no
//     PermissionRequest at any matcher width — sandbox network access, the
//     auto-mode dialogs, managed-settings review.
//   - elicitation_dialog / elicitation_url_dialog: an MCP server is waiting on
//     the user (`{waitingFor:"input needed", notification: … type:
//     Qwo(w)?"elicitation_url_dialog":"elicitation_dialog"}`). These set their
//     own type, so permission_prompt does NOT cover them.
//   - agent_needs_input: a sub-agent or teammate is blocked
//     (`band==="blocked"` → "<label> needs your input").
//
// DELIBERATELY NOT INSTALLED:
//
//   - worker_permission_prompt ("<worker> needs permission for <tool>",
//     "<worker> needs network access to <host>"): emitted by the lead's
//     InboxPoller, so the hook would arrive stamped with the LEAD's session id
//     while the session actually blocked is the worker's. Holding the lead at
//     `waiting` would mislabel a session that is genuinely working. Wiring it
//     needs the worker→session mapping this adapter does not have; filed rather
//     than guessed.
//   - auth_success, agent_completed, push_notification, computer_use_*,
//     quota_auto_resume_*: none of them means a human is blocked right now.
//
// This matcher takes bCt's exact-name path rather than hookMatcher's
// short-circuit: it matches Wcr's /^[a-zA-Z0-9_|, -]+$/ class, so Claude Code
// splits it on | and compares names — no regexp is compiled.
const hookMatcherNotification = "agent_needs_input|elicitation_dialog|elicitation_url_dialog|idle_prompt|permission_prompt"

// minCLIVersion is the lowest Claude Code version this install may be written
// into (issue #1365). Declared here, enforced generically by PermissionService
// via agent.ManagedUserFile.Version — before #1365 this adapter had no gate at all
// and wrote all seven entries into ~/.claude/settings.json whatever version was
// installed.
//
// 2.1.122 rather than the 2.1.63 that the `type: "http"` entry alone would
// require, because below 2.1.122 a hook entry Claude Code cannot make sense of
// does not degrade to "that hook doesn't fire" — it takes the user's whole
// settings.json with it. Both fixes are named in the upstream changelog:
//
//	2.1.101 — "an unrecognized hook event name in settings.json no longer
//	           causes the entire file to be ignored"
//	2.1.122 — "Fixed a malformed hooks entry in settings.json no longer
//	           invalidating the entire file"
//
// That is what decides the direction of this gate. Installing into a too-old
// Claude Code is not inert-and-slightly-untidy; it can silently disable every
// other setting the user has — other hooks, permissions, model choice. A floor
// high enough that a mistake degrades to one dead hook is worth more than the
// handful of versions of reach it costs.
const minCLIVersion = "2.1.122"

// hookEventSince records, per installed event, the Claude Code version at
// which that event is PROVEN to exist — not necessarily the version that
// introduced it. minCLIVersion must be >= every value (AssertHookVersionGate
// enforces it), which is what makes "do not write an entry the installed CLI
// does not know" (#1365 scope item 2) mechanical rather than aspirational.
//
// Provenance, all from the upstream changelog cached at
// ~/.claude/cache/changelog.md and re-checkable there:
//
//   - 1.0.38 "Released hooks" — the four original events.
//   - 2.0.45 "Added PermissionRequest hook to automatically approve or deny
//     tool permission requests with custom logic".
//   - 2.1.119 "Hooks: PostToolUse and PostToolUseFailure hook inputs now
//     include duration_ms" — an enhancement, so PostToolUseFailure existed BY
//     2.1.119. Its introducing version is not stated anywhere in the changelog;
//     2.1.119 is recorded here as the proven upper bound, which is the
//     conservative direction (it can only push the floor up, never down).
//
// The `type: "http"` delivery all seven entries use arrived in 2.1.63 ("Added
// HTTP hooks, which can POST JSON to a URL and receive JSON instead of running
// a shell command") — a property of the entry rather than of any one event, so
// it constrains minCLIVersion directly rather than appearing in this map.
var hookEventSince = map[string]string{
	HookPermissionRequest:  "2.0.45",
	HookPreToolUse:         "1.0.38",
	HookPostToolUse:        "1.0.38",
	HookPostToolUseFailure: "2.1.119",
	HookPreCompact:         "1.0.38",
	HookStop:               "1.0.38",
	HookNotification:       "1.0.38",
}

// installedHookEvents are the Claude Code hook events we install handlers for.
var installedHookEvents = []string{
	HookPermissionRequest,
	HookPreToolUse,
	HookPostToolUse,
	HookPostToolUseFailure,
	HookPreCompact,
	HookStop,
	HookNotification,
}

// matcherForEvent returns the matcher we install for the given event. What the
// matcher is compared AGAINST is per-event and decided by Claude Code's WQn:
// for the tool events it is the tool name (match-all since #1861); for
// PreCompact it is the compaction trigger ("manual"); for Notification it is
// the notification_type (a five-name list — see hookMatcherNotification, and
// do not assume it is still idle_prompt-only); for Stop it is empty — Claude
// Code's Stop hook takes no matcher (it fires at every turn end) and rejects
// settings.json that gives it one, so hookjson omits the matcher key entirely
// for an empty matcher.
func matcherForEvent(event string) string {
	switch event {
	case HookPreToolUse:
		return hookMatcherPreToolUse
	case HookPreCompact:
		return hookMatcherPreCompact
	case HookNotification:
		return hookMatcherNotification
	case HookStop:
		return ""
	default:
		return hookMatcher
	}
}

// ourHookEntry builds the inner hook object we install: native `type: http`
// delivery straight to the daemon (issue #1161), no shell wrapper. Claude Code
// POSTs the hook payload as a JSON body to url and treats a 2xx with no body as
// "no decision" — exactly the daemon's behaviour.
func ourHookEntry() map[string]interface{} {
	return map[string]interface{}{
		"type":    "http",
		"url":     hookEndpointURL(),
		"timeout": hookTimeoutSeconds,
	}
}

// hookConfig describes this adapter's install to the shared hookjson
// machinery (issue #1179): which file to merge into, which sentinel marks our
// entries, which events get installed with which matcher, and what the
// canonical inner entry looks like. Everything the two JSON-hook adapters have
// in common — the merge, idempotency, in-place upgrade and removal logic —
// lives there; everything Claude-Code-specific stays here.
func hookConfig(path string) hookjson.Config {
	return hookjson.Config{
		Path:        path,
		Sentinel:    hookSentinel,
		Events:      installedHookEvents,
		MatcherFor:  matcherForEvent,
		Entry:       ourHookEntry,
		IsCanonical: hookEntryIsCanonical,
		WriteFile:   atomicfile.WriteFile,
	}
}

// EnsureHooksInstalled adds irrlicht hook entries to ~/.claude/settings.json
// if they are not already present. Creates the file if it doesn't exist.
// Returns true if the file was modified, false if hooks were already installed.
func EnsureHooksInstalled() (bool, error) {
	path, err := claudeSettingsPath()
	if err != nil {
		return false, err
	}
	return hookjson.EnsureInstalled(hookConfig(path))
}

// VerifyHooksInstalled reports which of our entries are missing from, or stale
// in, ~/.claude/settings.json — without writing anything (issue #1372).
//
// Same hookConfig as the installer, so "stale" here means exactly what
// EnsureHooksInstalled would rewrite, including an entry left pointing at a
// port this daemon is not listening on.
func VerifyHooksInstalled() (agent.HookEntryStatus, error) {
	path, err := claudeSettingsPath()
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	return hookjson.Verify(hookConfig(path))
}

// UninstallHooks removes irrlicht hook entries from ~/.claude/settings.json.
// Returns true if the file was modified, false if no hooks were found.
func UninstallHooks() (bool, error) {
	path, err := claudeSettingsPath()
	if err != nil {
		return false, err
	}
	return hookjson.Uninstall(hookConfig(path))
}

// --- helpers ---

func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

// readClaudeSettings / writeClaudeSettings are thin adapters onto the shared
// codec so the statusline installer, which merges into the same settings.json,
// keeps reading and writing it exactly the way the hook installer does.
func readClaudeSettings(path string) (map[string]interface{}, error) {
	return hookjson.ReadSettings(path)
}

func writeClaudeSettings(path string, settings map[string]interface{}) error {
	return hookjson.WriteSettings(path, settings, atomicfile.WriteFile)
}

// hookEntryIsCanonical reports whether an inner hook object is already in the
// current native-http form: type "http", our endpoint url, and no leftover
// legacy `command` key. The timeout value is deliberately not part of the
// identity check, so tuning hookTimeoutSeconds never forces a churny rewrite of
// every existing install.
//
// The url comparison is against the *currently resolved* endpoint, so an entry
// left behind by a daemon on a different port reads as stale and is rewritten
// in place by upgradeStaleHookDelivery (#1178).
func hookEntryIsCanonical(hook map[string]interface{}) bool {
	if _, hasCmd := hook["command"]; hasCmd {
		return false
	}
	t, _ := hook["type"].(string)
	u, _ := hook["url"].(string)
	return t == "http" && u == hookEndpointURL()
}

// maxVersionScanLines bounds how far into a transcript newestObservedCLIVersion
// reads looking for the version stamp. Claude Code puts it on every user and
// assistant line, so it is found within the first few; the cap is there so a
// multi-megabyte transcript cannot turn a consent click into a long read.
const maxVersionScanLines = 200

// newestObservedCLIVersion returns the Claude Code version stamped on the most
// recently modified transcript, or "" if none can be read.
//
// This is the passive half of the #1365 gate, and without it the gate would be
// decorative for this adapter. The declared fallback is `claude --version`, but
// the production daemon is launched by Irrlicht.app and inherits a LaunchServices
// PATH of /usr/bin:/bin:/usr/sbin:/sbin, while the official installer puts the
// binary in ~/.local/bin — so the probe misses, the version reads as unknown,
// and unknown fails open. Every install would take the fail-open branch and no
// version would ever actually be checked. The transcripts are already on disk
// and already parsed for this exact field (parser.go reads raw["version"] into
// AgentVersion), so reading it here costs no process and no PATH.
//
// transcriptsDir() is resolved through agentpaths.Abs rather than used
// directly: it returns a $HOME-relative path when CLAUDE_CONFIG_DIR is unset
// (expansion happens downstream in fswatcher), which would make this walk run
// against the daemon's CWD and always find nothing.
//
// A stale answer is tolerable by construction: the version is the one that
// wrote the newest transcript, so it can only lag the installed CLI downward,
// and PermissionService re-confirms with the probe before acting on any
// "too old" reading (shouldConfirmByProbe). So a stale value costs at most one
// process, never a false refusal.
func newestObservedCLIVersion() string {
	dir, err := agentpaths.AbsRoot(transcriptsDir())
	if err != nil {
		return ""
	}
	newestPath := agentpaths.NewestFileWithSuffix(dir, ".jsonl")
	if newestPath == "" {
		return ""
	}
	return versionInTranscript(newestPath)
}

// versionInTranscript returns the first "version" string found in the file.
func versionInTranscript(path string) string {
	// Plain ".." check: the path is built by WalkDir from a root this package
	// resolved itself, but this is the spelling CodeQL's go/path-injection
	// query recognizes as a sanitizer (same reasoning as codex's
	// sessionMetaPayload).
	if strings.Contains(path, "..") {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// Transcript lines carry whole tool results and routinely exceed
	// Scanner's small default token limit.
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for i := 0; i < maxVersionScanLines && scanner.Scan(); i++ {
		var record map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			continue
		}
		if v, ok := record["version"].(string); ok && v != "" {
			return v
		}
	}
	return ""
}
