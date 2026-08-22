package kirocli

import (
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// PermissionKeyTranscripts gates all Kiro CLI monitoring (issue #570).
const PermissionKeyTranscripts = "transcripts"

// PermissionKeyHooks gates installing and receiving Kiro CLI hooks (issue
// #1716, epic #1355 Phase D).
//
// Aliased to the shared domain constant rather than restated: since #1383 two
// projections narrow on it — agents.HookConfigs (what --uninstall-hooks
// revokes) and the managed-user-file catalog — so a literal that drifted
// would silently drop this install out of both.
const PermissionKeyHooks = agent.HooksPermissionKey

// Kiro CLI — the Kiro ghost mark. Color picks contrast against the
// surrounding chrome: near-black on light themes, near-white on dark.
const iconSVGLight = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <path d="M50 10 C28 10 16 27 16 48 V88 L28 77 L39 88 L50 77 L61 88 L72 77 L84 88 V48 C84 27 72 10 50 10 Z" fill="none" stroke="#1A1A1A" stroke-width="8" stroke-linejoin="round"/>
  <circle cx="38" cy="46" r="6" fill="#1A1A1A"/>
  <circle cx="62" cy="46" r="6" fill="#1A1A1A"/>
</svg>`

const iconSVGDark = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <path d="M50 10 C28 10 16 27 16 48 V88 L28 77 L39 88 L50 77 L61 88 L72 77 L84 88 V48 C84 27 72 10 50 10 Z" fill="none" stroke="#E0E0E0" stroke-width="8" stroke-linejoin="round"/>
  <circle cx="38" cy="46" r="6" fill="#E0E0E0"/>
  <circle cx="62" cy="46" r="6" fill="#E0E0E0"/>
</svg>`

// Source is the adapter's transcript-tree declaration.
//
// The hook receiver's path confiner reads it per request (issue #1361), and
// Agent() below is its only other caller — one declaration, so the tree the
// daemon watches and the tree the receiver confines to cannot drift apart.
func Source() agent.Source {
	return agent.FilesUnderRoot{
		Dir: sessionsDir(),
		Parser: agent.JSONLineParser{
			NewParser: func() agent.LineParser { return &Parser{} },
		},
	}
}

// Agent returns the Kiro CLI adapter registration.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "Kiro CLI",
			IconSVGLight: iconSVGLight,
			IconSVGDark:  iconSVGDark,
		},
		Process: agent.Process{
			Match:         agent.ExactName{Name: ProcessName},
			PIDForSession: DiscoverPID,
		},
		Source:  Source(),
		Control: agent.Control{SupportsInput: true, Interrupt: agent.InterruptCtrlC},
		Permissions: []agent.Permission{
			agent.ControlPermission(),
			{
				Key:             PermissionKeyTranscripts,
				Kind:            permission.KindObserve,
				Title:           "Read session transcripts",
				FeatureUnlocked: "Session list, timeline & live state",
				Touches: "Reads Kiro CLI v2 session transcripts under ~/.kiro/sessions/cli/ " +
					"(or $KIRO_HOME/sessions/cli/ if KIRO_HOME is set)",
				Detail: "Tails Kiro CLI v2 *.jsonl session files under ~/.kiro/sessions/cli/ " +
					"(relocated to $KIRO_HOME/sessions/cli/ if KIRO_HOME is set) to " +
					"derive session state and activity, and reads the *.json " +
					"metadata sidecar for the working directory. Kiro CLI V3 uses an " +
					"incompatible per-workspace format and is not monitored. Read-only — no " +
					"file is ever modified. Toggling off stops all reading " +
					"immediately.",
			},
			{
				Key:             PermissionKeyHooks,
				Kind:            permission.KindModify,
				Title:           "Install status hooks",
				FeatureUnlocked: "Authoritative turn-end detection, and a faster tool-completion refresh",
				// Count and event list are derived from installedHookEvents,
				// never restated: this copy is the consent contract, and
				// hand-maintaining it is how claudecode's came to promise six
				// entries against a seven-event install (#1356).
				Touches: hookjson.EntriesTouched(displayAgentConfigPath, installedHookEvents),
				Detail: "Kiro CLI only fires hooks for an agent config selected via " +
					"`kiro-cli chat --agent` or `chat.defaultAgent` — your account was running " +
					"the built-in default, which has no file on disk to add a hooks key to. " +
					"So granting this creates a new agent, `irrlicht`, as a full copy of your " +
					"built-in default (kiro-cli agent create --from kiro_default), adds " +
					hookjson.EventList(installedHookEvents) + " hook entries that run " +
					"`irrlichd hook-post kiro-cli` (a tiny command irrlicht ships, reading the " +
					"daemon's own published address at the moment the hook fires so it never " +
					"blocks a tool call even when the daemon is not running), and sets it as " +
					"your default agent (kiro-cli agent set-default irrlicht) so " +
					"`kiro-cli chat` picks it up with no flag. Your previous default (or the " +
					"built-in, if you had none) is recorded and restored when you toggle this " +
					"off. When you upgrade Kiro CLI, irrlicht rebuilds that copy from the new " +
					"built-in default at its next start so it does not drift from what you " +
					"would get by upgrading normally — unless you have edited the copy " +
					"yourself, in which case your version is left alone. " +
					hookjson.RequiresVersion("Kiro CLI", minCLIVersion) +
					" Toggling off removes the hook entries, restores your prior default " +
					"agent, and leaves the `irrlicht` agent config in place inert (also " +
					"available via `irrlichd --uninstall-hooks`).",
				Apply:  func() error { _, err := EnsureHooksInstalled(); return err },
				Remove: func() error { _, err := UninstallHooks(); return err },
				Writes: &agent.ManagedUserFile{
					Path:      irrlichtAgentConfigPath,
					Uninstall: UninstallHooks,
					// Read-only; the repair it feeds runs through
					// PermissionService, which re-checks consent under its own
					// lock before writing (#1372).
					Verify: VerifyHooksInstalled,
					Version: &agent.VersionGate{
						Min:   minCLIVersion,
						Probe: []string{kiroCLIBinary, "--version"},
					},
					// Also: the SAME Apply closure writes THREE further files
					// beyond Path (issue #1718's Also field), and all are in
					// the user's real agent home:
					//   settings/cli.json          — chat.defaultAgent
					//                                (ensureDefaultAgent)
					//   .irrlicht-prior-default.json — what that setting held
					//                                first (recordPriorDefaultAgentOnce)
					//   .irrlicht-agent-snapshot.json — which kiro-cli built the
					//                                copy, and what its non-hook
					//                                content was when we wrote it
					//                                (#1736, agentrefresh.go)
					// Undeclared, either write is invisible to
					// agents.ManagedUserFiles / --print-managed-files, the
					// recording rig's snapshot/restore sweep — see
					// TestKiroCLIAlso_SettingsFileIsAManagedFile and
					// TestKiroCLIAlso_PriorDefaultSidecarIsAManagedFile
					// (core/cmd/irrlichd) for the mutation evidence.
					//
					// The sidecar is irrlicht's OWN document rather than a
					// file kiro-cli reads, which is why it was missed — but
					// "shared, user-owned" is about WHERE it lands, not who
					// authored it, and the residue it leaves is not inert:
					// recordPriorDefaultAgentOnce deliberately never
					// overwrites, so a copy a recording left in the
					// operator's real ~/.kiro is what a LATER real grant
					// reads instead of their actual chat.defaultAgent.
					//
					// What this does NOT change, stated because it would
					// otherwise be assumed: PermissionService.sharedConfigRefusal
					// (#1449) checks Path first and returns immediately on a
					// refusal, and Path and every Also entry here resolve under
					// the SAME root (kiroHome(), hookinstaller.go) — so for
					// THIS adapter Path and Also can never disagree on being
					// inside or outside the daemon's isolated home, and the
					// grant-all refusal VERDICT is identical whether or not
					// this field is declared; declaring it is a
					// defence-in-depth completeness property for that guard,
					// not a verdict-changing one. Declared anyway because the
					// two are independent consumers and the recorder's sweep
					// is not co-located with anything.
					Also: []func() (string, error){kiroSettingsPath, priorDefaultStatePath, agentSnapshotStatePath},
				},
			},
		},
	}
}
