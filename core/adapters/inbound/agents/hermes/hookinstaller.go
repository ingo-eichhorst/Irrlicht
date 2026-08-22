// hookinstaller.go installs irrlicht's Hermes Agent shell hooks — the two
// files this adapter writes into a user's Hermes installation (issue #1722,
// epic #1355).
//
// # The epic's verdict, and what the source actually says
//
// Epic #1355 cut hermes as "cost without payoff for now. hermes is a
// ProcessOwnedStore whose metrics `of verify` cannot validate anyway", and
// #1722 restated the payoff half as "a hook would buy event-driven
// invalidation rather than latency". Both halves are wrong, and the second
// one is wrong in the same way opencode's was.
//
// Measured against Hermes Agent v0.19.0 — read out of the Python package's own
// source, and fired live through `hermes hooks test` against an isolated
// HERMES_HOME:
//
//   - **hermes has a first-class hook system with its own CLI subcommand.**
//     `hermes hooks {list|test|revoke|doctor}` (hermes_cli/hooks.py) and a
//     growing set of lifecycle events (`VALID_HOOKS` in
//     hermes_cli/plugins.py — 23 at v0.19.0, 37 at v0.20.5 five weeks later;
//     #1773. No single count is asserted here because nothing in this
//     adapter re-derives it, and one already went stale), and THREE
//     independent subscription mechanisms — a gateway-only Python handler
//     directory, in-process Python plugins under `~/.hermes/plugins/`, and
//     SHELL hooks declared as a `hooks:` block in `~/.hermes/config.yaml`
//     (agent/shell_hooks.py). Nobody had looked; `--help` was never the
//     evidence.
//
//   - **The payoff is not invalidation.** hermes fires
//     `pre_approval_request` and `post_approval_response` around every
//     dangerous-command approval, and the pending request they bracket lives
//     in a process-local `_gateway_queues` map and a `contextvars` session
//     key. NOTHING about a request awaiting an answer reaches `state.db` —
//     the schema has `sessions`, `messages`, `session_model_usage`,
//     `state_meta`, `gateway_routing`, `compression_locks` and
//     `async_delegations`, and no approval table at all. So hermes's store
//     cannot express "the user is being asked right now", and before this
//     channel this adapter had no `waiting` state whatsoever. That is
//     opencode's shape exactly (#1719), arrived at from the other end.
//
//   - **The shell-hook route ships no code.** Unlike opencode and pi, where
//     irrlicht writes an executable artifact into the agent's plugin
//     directory, a hermes shell hook is a COMMAND LINE: hermes spawns it as
//     a subprocess with `shlex.split(...)`, `shell=False`, and pipes JSON to
//     its stdin. The command irrlicht installs names irrlicht's own binary.
//     hermes' own docs table calls this the mechanism with "inter-process
//     isolation: yes", against "no" for both Python routes.
//
//   - **hermes gates it behind its own consent, and irrlicht must satisfy
//     that consent to install anything that fires.** A configured hook does
//     not run until its `(event, command)` pair is recorded in
//     `~/.hermes/shell-hooks-allowlist.json`; without it hermes logs "not
//     allowlisted — skipped" and the permission would read `granted` with
//     nothing ever arriving. That is why this install writes TWO files and
//     declares both (Writes.Path plus Writes.Also). The allowlist entry is
//     the honest completion of the user's #570 grant, not a bypass of
//     hermes' gate: the grant IS a person answering the same question hermes'
//     TTY prompt asks, and the consent copy says so in those words.
//
// # Live re-verification against a repaired v0.19.0 install (#1766)
//
// #1765's audit ran against a hermes checkout that turned out to be stale
// (`_old/`, ~4 weeks behind) because the installed CLI was a broken shim;
// #1773 re-derived the same claims from upstream's git history instead
// (source-read only, HEAD vs origin/main, no CLI). #1766 repaired the local
// install — `hermes --version` now prints "Hermes Agent v0.19.0 (2026.7.20)
// · upstream 13f4cfeb · local e289e561" — and this section is what actually
// running it, live, against a working v0.19.0, confirmed or corrected:
//
//   - **The 23-event count.** `hermes hooks test <unknown-event>` prints
//     `VALID_HOOKS` verbatim in its "Valid events:" error line. Run live it
//     names exactly 23 events, including all three installed here and all
//     three of the control-bearing ones this file excludes — the same
//     number #1773 read out of the source tree, now shown by the CLI itself
//     rather than inferred from a set literal.
//   - **`extra.session_key`.** hermes ships no synthetic default payload for
//     either approval event (`hermes_cli/hooks.py`'s `_DEFAULT_PAYLOADS` has
//     no `pre_approval_request` / `post_approval_response` entry — a gap in
//     hermes' OWN test tooling, not this adapter), so reproducing the real
//     shape needs `hermes hooks test <event> --payload-file <f>` with a
//     `session_key` field in `<f>`. Fired live this way, `_serialize_payload`
//     (agent/shell_hooks.py, unchanged from the source #1773 read) puts it
//     under the response's `extra.session_key`, top-level `session_id`
//     absent — exactly the shape sessionID() above reads.
//   - **`hermes hooks doctor` against a real installed entry** — this
//     machine's actual `~/.hermes` (all three events installed, allowlisted,
//     and firing) — found all three hooks "ran clean … observer-only", but
//     also flagged all three with a false "script modified since approval"
//     on every run. That finding was real and is now fixed: see
//     formatScriptMtimeISO's doc comment below for the root cause (a
//     lexicographic-vs-parsed timestamp mismatch with hermes' own
//     zero-microsecond isoformat spelling) and
//     TestFormatScriptMtimeISO_MatchesHermesConditionalMicroseconds /
//     TestFormatScriptMtimeISO_NoFalseDriftAcrossAZeroMicrosecondRoundTrip
//     (hookinstaller_test.go) for the regression coverage.
//   - **The three subscription mechanisms.** Shell hooks (this adapter's
//     route) are confirmed live end-to-end above. The other two — hermes'
//     plugin loader (`hermes_cli/plugins.py`: bundled/user
//     `~/.hermes/plugins/`/project/pip sources) and the gateway-only handler
//     path — are unchanged in the installed source and this adapter uses
//     neither, so nothing here re-verifies them beyond confirming their code
//     is still present; #1773 already covered gateway exclusion in depth via
//     source, and nothing in that area is CLI-reachable to re-check live.
//
// Nothing in the event mapping or the `minHermesVersion` floor moved: this
// is a confirmation of #1773's source-read claims by a different method
// (a working CLI instead of git history), plus one genuine, previously
// undiscovered adapter-side bug the CLI surfaced that source-reading alone
// would not have — the mtime format mismatch above.
//
// # Why `pre_tool_call` and `pre_verify` are not installed
//
// This is the #1719 safety lesson in its hermes spelling, and it is a source
// fact rather than a preference. `agent/shell_hooks._parse_response` reads a
// hook's STDOUT as a control channel for exactly two events: `pre_tool_call`
// accepts `{"action": "block"}` / `{"decision": "block"}` and vetoes the tool
// call, and `pre_verify` accepts `{"action": "continue"}` and keeps the
// agent's turn running. A monitoring channel must not be able to do either,
// so neither event is installed — and because "we remembered not to" is not a
// mechanism, hooks_test.go's TestInstalledEventsAreObserverOnly fails the
// build if one appears in installedHookEvents.
//
// The command itself is wrapped in `/bin/sh -c` so its stdout is redirected to
// /dev/null before hermes can read it. That is defence in depth rather than
// the guard — but it is not theoretical: hookbeacon.LegacyGuardToken puts
// `--version` FIRST in the command line, so an irrlichd predating #1417
// prints a version banner on stdout, once per firing, straight into hermes'
// response parser.
//
// # Delivery
//
// Address-free (contracttesting.DeliveryAddressFree): the command names the
// `irrlichd hook-post hermes` beacon (core/pkg/hookbeacon), which reads the
// daemon's published address at fire time. Nothing address-shaped is written
// into the user's config, so the #1178 stale-port class is inexpressible.
//
// # What this does NOT cover
//
//   - Hermes PROFILES. `hermes -p <name>` relocates HERMES_HOME to
//     `~/.hermes/profiles/<name>/`, which has its own config.yaml and its own
//     allowlist. This installs into the home the daemon resolves — the
//     default one, or whatever HERMES_HOME names — and a session started
//     under another profile is watched by the store watcher alone, exactly as
//     it was before this channel existed.
//   - The gateway platforms (whatsapp, slack, …). The hooks fire there too,
//     but those sessions never reach a dispatch: gateway sessions are minted
//     `uuid.uuid4().hex[:8]` against the `hex[:6]` sessionIDShape (hooks.go)
//     actually accepts, so the exclusion is id-shape by hex length — not
//     `source`, which the shell-hook envelope does not carry at all. See
//     TestSessionIDShape_RejectsGatewayMintedIDs and
//     TestHookReceiver_GatewaySurfaceApprovalIsQuiet in hooks_test.go (#1773).
package hermes

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"irrlicht/core/adapters/inbound/agents/hookyaml"
	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/hookbeacon"
)

// Hermes' own wire names for the three lifecycle events this adapter
// subscribes to. They are hermes', not Claude Code's: #1722 and #1355 both
// warn against reusing `session.hookSignalEffects` rows keyed on Claude Code's
// vocabulary, and none of these three is dispatched through that map — hooks.go
// routes each to a dedicated SessionDetector entry point.
const (
	// HookEventPreApprovalRequest is fired by tools/approval.py immediately
	// before the user is asked to approve a dangerous command, on every
	// surface (CLI prompt, gateway, and the smart auxiliary-LLM decision).
	//
	// It is a NARROW assert, which is the property that makes it usable at
	// all: approval.py fires it only after `check_all_command_guards` has
	// matched a command against a danger pattern and only when the pattern is
	// not already approved for the session. It is emphatically not a
	// before-every-tool event, and must never be treated as one.
	//
	// hermes documents it as an observer: "Observers only: return values are
	// ignored. Plugins cannot veto or pre-answer an approval from these hooks."
	HookEventPreApprovalRequest = "pre_approval_request"

	// HookEventPostApprovalResponse is fired with the outcome for EVERY way an
	// approval ends — `once`, `session`, `always`, `deny`, `timeout`, and the
	// two smart-mode verdicts. A denial and a timeout both release, which is
	// what keeps this out of copilot's position where a hold has no matching
	// release and pins the session `waiting`.
	HookEventPostApprovalResponse = "post_approval_response"

	// HookEventOnSessionEnd is fired at the end of every run_conversation call
	// (agent/turn_finalizer.py) — i.e. once per TURN, not once per session,
	// despite the name. Its `extra` carries `completed` and `interrupted`, so
	// it covers the turn finishing and the user interrupting alike, which is
	// the same authoritative turn-end claim Claude Code's Stop makes.
	HookEventOnSessionEnd = "on_session_end"
)

// installedHookEvents is every hermes event this adapter subscribes to. The
// exclusions are the finding rather than an omission:
//
//   - `pre_tool_call` and `pre_verify` are the two events whose STDOUT hermes
//     reads as a decision. See this file's header; the exclusion is enforced
//     by a test, not by memory.
//
//   - `pre_llm_call` is the third event with a return contract
//     (`{"context": …}` is prepended to the user's message). It also fires
//     once per turn as the turn OPENS, which the store's own new `messages`
//     rows already report — the watcher emits activity off them.
//
//   - `on_session_start` would duplicate discovery: hermes INSERTs the
//     `sessions` row while the session is live, ~2s in, which is what this
//     adapter already discovers on.
//
//   - `post_api_request` carries `usage` and `finish_reason` and was the
//     candidate #1722 asked about for metrics. It is not installed: the store
//     already carries per-model token and cost rows (`session_model_usage`),
//     so the hook would add a process spawn per API request — several per
//     turn — for a number irrlicht already has. What `of verify` cannot
//     validate is the REPLAY golden for a ProcessOwnedStore adapter, and a
//     hook does not change that.
//
//   - `post_tool_call`, `subagent_start`/`subagent_stop`, the `kanban_*`
//     trio and the gateway hooks are either store-covered or out of this
//     adapter's scope (the gateway platforms are excluded by sessionIDShape's
//     hex length, not by `source` — see hooks.go and the tests named above).
var installedHookEvents = []string{
	HookEventPreApprovalRequest,
	HookEventPostApprovalResponse,
	HookEventOnSessionEnd,
}

// controlBearingHookEvents are the hermes events whose stdout the agent reads
// as a decision (agent/shell_hooks._parse_response). Installing one would give
// a monitoring channel the ability to veto a tool call or keep a turn running.
//
// Declared here rather than only in prose so hooks_test.go can assert the
// intersection with installedHookEvents is empty — the guard #1719's first
// version of this idea lacked.
var controlBearingHookEvents = []string{
	"pre_tool_call", // {"action":"block"} / {"decision":"block"} vetoes the tool
	"pre_verify",    // {"action":"continue"} keeps the turn running
	"pre_llm_call",  // {"context": "..."} is prepended to the user's message
}

// hookEventSince records the hermes version each installed event is KNOWN to
// exist in. All three are pinned to minHermesVersion because that is the
// version they were read out of and fired against; the installed package is a
// squashed source tree with no usable history to bisect an earlier number
// from, and inventing one would be a claim nothing here supports.
// AssertHookVersionGate enforces that minHermesVersion is >= every value here.
var hookEventSince = map[string]string{
	HookEventPreApprovalRequest:   minHermesVersion,
	HookEventPostApprovalResponse: minHermesVersion,
	HookEventOnSessionEnd:         minHermesVersion,
}

// minHermesVersion is the lowest Hermes Agent version this adapter's install
// requires.
//
// Pinned to the version everything here was measured against — 0.19.0, read
// out of the installed package and confirmed by `hermes --version`, which
// prints "Hermes Agent v0.19.0 (2026.7.20) · …" and which core/pkg/cliversion
// parses. The floor is not a claim about one event: it covers the `hooks:`
// config block, the allowlist file name and record shape, the stdin envelope,
// and the three event names and their payload fields — none of which was
// bisected. cliversion fails OPEN on an unknown or unparseable version, so
// overshooting costs a missing install on an old CLI rather than a wrong one
// on a new CLI.
const minHermesVersion = "0.19.0"

// HookEndpointPath is the daemon's hermes hook route. Exported so
// registerHookRoutes (core/cmd/irrlichd/startup.go) and beaconroute_test.go's
// pinned map both derive from the same segment the installed command carries.
var HookEndpointPath = hookbeacon.EndpointPath(AdapterName)

// hookBlockKey is the top-level key in hermes' config.yaml that holds shell
// hook definitions.
const hookBlockKey = "hooks"

// hookRegionOwner is the token embedded in the marker comments delimiting the
// region of config.yaml this installer owns.
const hookRegionOwner = "irrlicht"

// configFileName and allowlistFileName are the two files under HERMES_HOME
// this permission's Apply writes. Both are declared on the permission
// (Writes.Path and Writes.Also) — an install that wrote only the first would
// be an install that never fires.
const (
	configFileName    = "config.yaml"
	allowlistFileName = "shell-hooks-allowlist.json"
)

// displayConfigPath and displayAllowlistPath are the consent copy's
// home-relative spellings of the two files.
const (
	displayConfigPath    = "$HERMES_HOME/config.yaml"
	displayAllowlistPath = "$HERMES_HOME/shell-hooks-allowlist.json"
)

// hookTimeoutSeconds bounds hermes' wait for the beacon.
//
// hermes runs a shell hook SYNCHRONOUSLY in the agent's own thread
// (`subprocess.run(..., timeout=spec.timeout)`), so this number is a ceiling
// on how long a wedged daemon can stall the user's turn. hermes' own default
// is 60 seconds, which is exactly the stall #1373 built the beacon to avoid;
// the beacon bounds itself at one second (hookbeacon.DefaultTimeout) and
// always exits 0, so two is a ceiling that should never be reached and is
// imperceptible if it is.
const hookTimeoutSeconds = 2

// shellInterpreter wraps the beacon command. See this file's header: the
// wrapper is what puts `>/dev/null` between the beacon's stdout and hermes'
// response parser, and what keeps `|| true` for a binary that has been
// deleted. It has a second, smaller payoff — hermes derives
// `script_mtime_at_approval` from the first path-shaped token of the command
// (`agent/shell_hooks._command_script_path`), so an unwrapped command would
// name irrlichd itself and every irrlicht upgrade would make
// `hermes hooks doctor` warn "script modified since approval".
const shellInterpreter = "/bin/sh"

// --- path resolution ---

// homeDirAbs is homeDir with the failure reported rather than papered over.
//
// homeDir falls back to the RELATIVE ".hermes" when os.UserHomeDir fails,
// which is harmless for a store path that simply finds nothing, and is not
// harmless for a file this code WRITES: a relative path would land wherever
// the daemon happens to be running. #1449's shared-config refusal is lexical
// over the resolved path, so handing it a relative one is exactly the input
// it cannot judge.
func homeDirAbs() (string, error) {
	dir := homeDir()
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("hermes: cannot resolve an absolute Hermes home (got %q); set %s", dir, homeEnvVar)
	}
	return dir, nil
}

// ConfigPath is the file this permission's Writes.Path resolves — hermes'
// own config.yaml, where the `hooks:` block lives.
func ConfigPath() (string, error) {
	dir, err := homeDirAbs()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, configFileName), nil
}

// AllowlistPath is the second file the install writes, declared on
// Writes.Also. hermes will not run a configured hook whose (event, command)
// pair is absent from it.
func AllowlistPath() (string, error) {
	dir, err := homeDirAbs()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, allowlistFileName), nil
}

// --- command rendering ---

// hookCommand renders the value of a `command:` field for an already-resolved
// beacon line.
func hookCommand(beacon string) string {
	return shellInterpreter + " -c " + shellQuote(beacon)
}

// shellQuote wraps s in single quotes, escaping embedded quotes with the
// portable '\” form. hermes parses the field with Python's shlex in POSIX
// mode, which implements exactly this grammar, so a home directory containing
// a quote or a space survives the round trip intact.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hookEntries renders the hookyaml region contents for a resolved command.
func hookEntries(command string) []hookyaml.Entry {
	entries := make([]hookyaml.Entry, 0, len(installedHookEvents))
	for _, e := range installedHookEvents {
		entries = append(entries, hookyaml.Entry{
			Event:          e,
			Command:        command,
			TimeoutSeconds: hookTimeoutSeconds,
		})
	}
	return entries
}

func hookYAMLConfig(path, command string) hookyaml.Config {
	return hookyaml.Config{
		Path:     path,
		BlockKey: hookBlockKey,
		Owner:    hookRegionOwner,
		Entries:  hookEntries(command),
		// hermes rewrites its whole config.yaml from a parsed dict whenever it
		// saves one — `hermes config set`, the setup wizard, a dashboard save
		// (hermes_cli/config.save_config -> utils.atomic_yaml_write). Every
		// comment in the file goes with it, INCLUDING the markers that
		// delimit our region, while the entries survive as data and keep
		// firing. Without a sentinel this installer would then read its own
		// entries as a user's colliding hooks forever and
		// `--uninstall-hooks` would find nothing to remove, leaving them in
		// the user's config with no way to take them out.
		//
		// The beacon sentinel is the right token: it is in every command this
		// installer writes, it deliberately excludes the binary path so an
		// entry naming a moved irrlichd is still ours, and it is the same
		// string every other adapter identifies its entries by.
		Sentinel: hookbeacon.Sentinel(AdapterName),
	}
}

// --- install / verify / uninstall ---

// EnsureHooksInstalled writes irrlicht's `hooks:` region into hermes'
// config.yaml and records the matching approvals in hermes' shell-hook
// allowlist, for the irrlichd that is running now. Reports whether anything
// changed.
//
// Idempotent, and self-healing across a moved binary: an already-installed
// region naming a different irrlichd is rewritten in place.
func EnsureHooksInstalled() (bool, error) {
	beacon, err := hookbeacon.InstalledCommand(AdapterName)
	if err != nil {
		return false, err
	}
	return ensureInstalledWithCommand(beacon)
}

// ensureInstalledWithCommand is EnsureHooksInstalled with the beacon command
// resolved by the caller — the seam #1178's contract uses to seed the install
// a differently-situated daemon would have written.
// hookbeacon.InstalledCommand is called exactly once per entry point, which is
// the shape hookbeacon's doc comment asks adopters for.
func ensureInstalledWithCommand(beacon string) (bool, error) {
	configPath, err := ConfigPath()
	if err != nil {
		return false, err
	}
	command := hookCommand(beacon)

	changedConfig, err := hookyaml.EnsureInstalled(hookYAMLConfig(configPath, command))
	if err != nil {
		return false, err
	}
	changedAllowlist, err := ensureAllowlisted(command)
	if err != nil {
		return false, err
	}
	return changedConfig || changedAllowlist, nil
}

// VerifyHooksInstalled reports whether the install is still present and
// current, without writing anything (issue #1372).
//
// Both files are checked, and the allowlist is not the lesser half: hermes'
// own `hermes hooks revoke` removes an approval and leaves the config entry
// standing, which produces a config that LOOKS installed and a hook that
// never fires. That is the exact shape #1372 exists to notice.
func VerifyHooksInstalled() (agent.HookEntryStatus, error) {
	configPath, err := ConfigPath()
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	beacon, err := hookbeacon.InstalledCommand(AdapterName)
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	command := hookCommand(beacon)

	present, canonical, err := hookyaml.Inspect(hookYAMLConfig(configPath, command))
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	if !present {
		return agent.HookEntryStatus{Missing: allInstalledEvents()}, nil
	}
	if !canonical {
		return agent.HookEntryStatus{Stale: allInstalledEvents()}, nil
	}

	missing, err := unallowlistedEvents(command)
	if err != nil {
		return agent.HookEntryStatus{}, err
	}
	if len(missing) > 0 {
		// Configured but not approved: hermes skips registration entirely and
		// logs "not allowlisted". Reported as Missing rather than Stale
		// because from the channel's point of view the entry is not there.
		return agent.HookEntryStatus{Missing: missing}, nil
	}
	return agent.HookEntryStatus{}, nil
}

// UninstallHooks removes irrlicht's region from config.yaml and its approvals
// from the allowlist. Reports whether anything changed.
//
// Both halves are unconditional and independent: a user who ran
// `hermes hooks revoke` has an allowlist with nothing of ours in it and a
// config that still does, and the reverse happens if someone edits the config
// by hand. Neither file is created if it does not exist.
func UninstallHooks() (bool, error) {
	configPath, err := ConfigPath()
	if err != nil {
		return false, err
	}
	// The command is irrelevant to removal — hookyaml finds the region by its
	// markers and the allowlist filter matches on the beacon sentinel — so the
	// uninstall keeps working when the binary it names has been deleted.
	changedConfig, err := hookyaml.Uninstall(hookYAMLConfig(configPath, "placeholder"))
	if err != nil {
		return false, err
	}
	changedAllowlist, err := removeAllowlisted()
	if err != nil {
		return false, err
	}
	return changedConfig || changedAllowlist, nil
}

func allInstalledEvents() []string {
	return append([]string(nil), installedHookEvents...)
}

// --- hermes' shell-hook allowlist ---

// allowlistFile mirrors the JSON hermes reads and writes
// (agent/shell_hooks.load_allowlist / save_allowlist). Unknown top-level keys
// are preserved through a round trip so a future hermes field is not dropped
// by irrlicht recording an approval.
type allowlistFile struct {
	Approvals []allowlistEntry `json:"approvals"`
	Extra     map[string]json.RawMessage
}

// allowlistEntry is one (event, command) approval.
type allowlistEntry struct {
	Event                 string `json:"event"`
	Command               string `json:"command"`
	ApprovedAt            string `json:"approved_at"`
	ScriptMtimeAtApproval string `json:"script_mtime_at_approval,omitempty"`
}

func (f *allowlistFile) UnmarshalJSON(b []byte) error {
	raw := map[string]json.RawMessage{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	if a, ok := raw["approvals"]; ok {
		// hermes itself tolerates a non-list `approvals` by replacing it, so a
		// decode failure here is normalized the same way rather than blocking
		// the install on a file the agent would silently repair.
		if err := json.Unmarshal(a, &f.Approvals); err != nil {
			f.Approvals = nil
		}
		delete(raw, "approvals")
	}
	f.Extra = raw
	return nil
}

func (f allowlistFile) MarshalJSON() ([]byte, error) {
	out := map[string]json.RawMessage{}
	for k, v := range f.Extra {
		out[k] = v
	}
	approvals := f.Approvals
	if approvals == nil {
		approvals = []allowlistEntry{}
	}
	b, err := json.Marshal(approvals)
	if err != nil {
		return nil, err
	}
	out["approvals"] = b
	return json.Marshal(out)
}

func readAllowlist() (allowlistFile, error) {
	path, err := AllowlistPath()
	if err != nil {
		return allowlistFile{}, err
	}
	src, err := os.ReadFile(path) // #nosec G304 -- path resolved from the adapter's own Hermes home
	if errors.Is(err, os.ErrNotExist) {
		return allowlistFile{}, nil
	}
	if err != nil {
		return allowlistFile{}, err
	}
	var f allowlistFile
	if err := json.Unmarshal(src, &f); err != nil {
		// hermes' own loader returns an empty skeleton for unparseable JSON
		// and then overwrites it. Matching that is the only way an install can
		// succeed against a file the agent already treats as absent — the
		// alternative is a permanent refusal over a file nothing will repair.
		return allowlistFile{}, nil
	}
	return f, nil
}

func writeAllowlist(f allowlistFile) error {
	path, err := AllowlistPath()
	if err != nil {
		return err
	}
	// indent 2 + sorted keys is exactly what hermes' own save_allowlist emits
	// (json.dumps(data, indent=2, sort_keys=True)), so a subsequent write by
	// hermes produces no diff against ours. encoding/json sorts map keys, and
	// the struct's own field order matches hermes' alphabetical output.
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return hookyaml.AtomicWriteFile(path, append(b, '\n'))
}

// isOurApproval reports whether an allowlist entry is one irrlicht wrote. It
// matches the beacon SENTINEL rather than the exact command, deliberately: an
// approval naming an irrlichd that has since moved is still ours, and
// recognizing it is the precondition for replacing it instead of leaving a
// dead approval beside the live one.
func isOurApproval(e allowlistEntry) bool {
	return strings.Contains(e.Command, hookbeacon.Sentinel(AdapterName))
}

// ensureAllowlisted records one approval per installed event for command,
// replacing any previous irrlicht approval. Reports whether anything changed.
func ensureAllowlisted(command string) (bool, error) {
	f, err := readAllowlist()
	if err != nil {
		return false, err
	}
	want := desiredApprovals(command, existingApprovedAt(f, command))

	kept := make([]allowlistEntry, 0, len(f.Approvals))
	for _, e := range f.Approvals {
		if !isOurApproval(e) {
			kept = append(kept, e)
		}
	}
	next := append(kept, want...)
	if approvalsEqual(f.Approvals, next) {
		return false, nil
	}
	f.Approvals = next
	return true, writeAllowlist(f)
}

// existingApprovedAt returns the timestamp already recorded for command, so a
// re-run that changes nothing else does not churn the file — and so the user's
// original approval time survives an irrlicht upgrade. Empty when there is no
// current approval for this exact command.
func existingApprovedAt(f allowlistFile, command string) string {
	for _, e := range f.Approvals {
		if e.Command == command && e.ApprovedAt != "" {
			return e.ApprovedAt
		}
	}
	return ""
}

func desiredApprovals(command, approvedAt string) []allowlistEntry {
	if approvedAt == "" {
		approvedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000000Z")
	}
	mtime := scriptMtimeISO()
	out := make([]allowlistEntry, 0, len(installedHookEvents))
	for _, e := range installedHookEvents {
		out = append(out, allowlistEntry{
			Event:                 e,
			Command:               command,
			ApprovedAt:            approvedAt,
			ScriptMtimeAtApproval: mtime,
		})
	}
	return out
}

// scriptMtimeISO renders the modification time of the interpreter the command
// names, the way hermes' own _record_approval does.
//
// hermes derives the "script" from the first path-shaped token of the command,
// which for the wrapped form is the interpreter — a file that does not change
// when irrlicht is upgraded. An unreadable one yields "", which hermes' own
// helper also returns and which its drift check treats as "no baseline".
func scriptMtimeISO() string {
	info, err := os.Stat(shellInterpreter)
	if err != nil {
		return ""
	}
	return formatScriptMtimeISO(info.ModTime())
}

// formatScriptMtimeISO renders t the way hermes' own script_mtime_iso
// (agent/shell_hooks.py) renders a Python datetime: via
// datetime.isoformat().replace("+00:00", "Z"), which OMITS the
// fractional-seconds component entirely when microseconds are exactly zero
// and otherwise always emits exactly 6 digits — never any other precision.
//
// This is not a cosmetic choice: `hermes hooks doctor` (hermes_cli/hooks.py)
// compares its own freshly-recomputed mtime against the ScriptMtimeAtApproval
// this function writes with a LEXICOGRAPHIC `mtime_now > mtime_at` on these
// strings, not a parsed-time comparison. A format that always appends
// ".000000Z" disagrees with hermes' own zero-microsecond spelling even when
// the two instants are identical: "...17Z" sorts AFTER "...17.000000Z"
// because 'Z' (0x5A) is greater than '.' (0x2E) in a byte-wise compare — a
// permanent false "script modified since approval" on every doctor run
// against a file (like /bin/sh) whose mtime carries no fractional component.
//
// Verified live (issue #1766): this machine's `/bin/sh` has a
// zero-nanosecond mtime, and before this fix `hermes hooks doctor` reported
// all three installed hooks as drifted despite nothing having changed —
// `was 2026-04-30T19:33:17.000000Z, now 2026-04-30T19:33:17Z` for an
// UNCHANGED mtime, the ".000000Z" half written by the pre-fix version of
// this function and the "Z"-only half computed live by hermes itself.
func formatScriptMtimeISO(t time.Time) string {
	t = t.UTC()
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02T15:04:05Z")
	}
	return t.Format("2006-01-02T15:04:05.000000Z")
}

func approvalsEqual(a, b []allowlistEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// unallowlistedEvents returns the installed events that command is not
// approved for, in installedHookEvents order.
func unallowlistedEvents(command string) ([]string, error) {
	f, err := readAllowlist()
	if err != nil {
		return nil, err
	}
	approved := map[string]bool{}
	for _, e := range f.Approvals {
		if e.Command == command {
			approved[e.Event] = true
		}
	}
	var missing []string
	for _, e := range installedHookEvents {
		if !approved[e] {
			missing = append(missing, e)
		}
	}
	return missing, nil
}

// removeAllowlisted drops every irrlicht approval. Reports whether anything
// changed. A file that does not exist is not created.
func removeAllowlisted() (bool, error) {
	path, err := AllowlistPath()
	if err != nil {
		return false, err
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		return false, nil
	}
	f, err := readAllowlist()
	if err != nil {
		return false, err
	}
	kept := make([]allowlistEntry, 0, len(f.Approvals))
	for _, e := range f.Approvals {
		if !isOurApproval(e) {
			kept = append(kept, e)
		}
	}
	if len(kept) == len(f.Approvals) {
		return false, nil
	}
	f.Approvals = kept
	return true, writeAllowlist(f)
}
