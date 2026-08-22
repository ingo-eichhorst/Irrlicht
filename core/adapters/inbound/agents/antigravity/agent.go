package antigravity

import (
	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// PermissionKeyTranscripts gates all Antigravity monitoring (issue #570).
const PermissionKeyTranscripts = "transcripts"

// Antigravity's mark: a smooth multicolor arch (the brand's "lift" hump), not a
// literal arrow. The sweep is drawn as three solid-colored sub-arcs — blue leg,
// warm peak, green leg — meeting at shared endpoints with round caps so the
// joins blend. Solid segments (not a <linearGradient>) are deliberate: the
// macOS menu-bar renderer (NSImage(data:)) flattens an SVG gradient to a single
// flat color, so a gradient would lose the multicolor there; segments render in
// full color in both the web dashboard and the app. Dark uses Google's lighter
// tonal variants so the mark reads against dark chrome.
const iconSVGLight = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <g fill="none" stroke-width="15" stroke-linecap="round">
    <path d="M16 82 Q27.3 39.3 38.7 25.1" stroke="#4285F4"/>
    <path d="M38.7 25.1 Q50 10.9 61.3 25.1" stroke="#EA4335"/>
    <path d="M61.3 25.1 Q72.7 39.3 84 82" stroke="#34A853"/>
  </g>
</svg>`

const iconSVGDark = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 100 100">
  <g fill="none" stroke-width="15" stroke-linecap="round">
    <path d="M16 82 Q27.3 39.3 38.7 25.1" stroke="#8AB4F8"/>
    <path d="M38.7 25.1 Q50 10.9 61.3 25.1" stroke="#F28B82"/>
    <path d="M61.3 25.1 Q72.7 39.3 84 82" stroke="#81C995"/>
  </g>
</svg>`

// Agent returns the Antigravity adapter registration. One adapter covers both
// the `agy` CLI and the Antigravity IDE: the Source watches both brain stores
// (CLI via Dir, IDE via ExtraDirs) and derives each session's ID from the
// <conv-id> directory (SessionIDFromPath) since the transcript filename is
// constant. The ExactName matcher binds CLI processes for liveness; IDE
// sessions have no per-conversation process and stay transcript-only.
func Agent() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{
			Name:         AdapterName,
			DisplayName:  "Antigravity",
			IconSVGLight: iconSVGLight,
			IconSVGDark:  iconSVGDark,
		},
		Process: agent.Process{
			Match:            agent.ExactName{Name: ProcessName},
			PIDForSession:    DiscoverPID,
			RequireKnownHost: true,
		},
		Source: agent.FilesUnderRoot{
			Dir:               cliBrainDir,
			ExtraDirs:         []string{ideBrainDir},
			SessionIDFromPath: sessionIDFromPath,
			Parser: agent.JSONLineParser{
				NewParser: func() agent.LineParser { return &Parser{} },
			},
		},
		Permissions: []agent.Permission{
			{
				Key:             PermissionKeyTranscripts,
				Kind:            permission.KindObserve,
				Title:           "Read session transcripts",
				FeatureUnlocked: "Session list, timeline, state, and context-window usage",
				Touches: "Reads session transcripts and conversation stores under " +
					"~/.gemini/antigravity-cli/ and ~/.gemini/antigravity/ and the " +
					"working directory of running agy processes",
				Detail: "Tails transcript.jsonl files under " +
					"~/.gemini/antigravity{,-cli}/brain/<conversation>/.system_generated/logs/ " +
					"to derive session state, model, and timeline, and reads the sibling " +
					"conversation store ~/.gemini/antigravity{,-cli}/conversations/<conversation>.db " +
					"for token counts and the canonical model name (context-window usage). " +
					"Also scans for running agy CLI processes and reads their working " +
					"directory to bind a session to its process. Read-only — no file is " +
					"ever modified. Toggling off stops all reading immediately.",
			},
			{
				Key:             PermissionKeyHooks,
				Kind:            permission.KindModify,
				Title:           "Install turn-end hook",
				FeatureUnlocked: "Authoritative turn-end detection",
				// "1 hook entry" is what contracttesting's #1356 count arm
				// reads, and it is the honest number: one handler, on one
				// event. What Touches names in the same breath is the fact
				// that makes this adapter different from every other hook
				// install — the file is SHARED with the user's own /hooks
				// command, so the wizard row (which is what most users read)
				// has to say that irrlicht merges into it rather than owning
				// it.
				Touches: hookjson.EntriesTouched(displayHooksPath, installedHookEvents) +
					" — a file you may also write with Antigravity's own /hooks command; " +
					"your other named hooks are left untouched",
				Detail: "Adds one named hook, \"" + hookName + "\", to " + displayHooksPath +
					", which is the only file Antigravity loads hooks from and is shared " +
					"with anything else that writes hooks — your own /hooks command " +
					"included. Nothing else in the file is read, reordered or rewritten: " +
					"other named hooks, other keys and your comments are preserved, and " +
					"turning this off removes only irrlicht's own entry. The hook " +
					"subscribes to exactly one event, " + hookjson.EventList(installedHookEvents) +
					", which Antigravity fires when its execution loop finishes a turn. " +
					"It runs `irrlichd hook-post antigravity`, a small command irrlicht " +
					"ships, which reads the daemon's own published address at the moment " +
					"the hook fires — so nothing in your config names a host or a port and " +
					"it cannot go stale, and it never blocks Antigravity even when the " +
					"daemon is not running. The handler prints a fixed empty result, which " +
					"Antigravity reads as \"carry on\": irrlicht's monitoring channel is " +
					"structurally unable to allow, deny, continue, interrupt or alter " +
					"anything the agent does, and no event whose result could do so is " +
					"installed. Only the conversation id is used, to attach the turn end " +
					"to the session irrlicht is already watching. " +
					hookjson.RequiresVersion("agy", minAgyVersion) +
					" Toggling off removes the entry (also available via " +
					"`irrlichd --uninstall-hooks`).",
				Apply:  func() error { _, err := EnsureHooksInstalled(); return err },
				Remove: func() error { _, err := UninstallHooks(); return err },
				Writes: &agent.ManagedUserFile{
					Path:      HooksPath,
					Uninstall: UninstallHooks,
					Verify:    VerifyHooksInstalled,
					Version: &agent.VersionGate{
						Min:   minAgyVersion,
						Probe: []string{ProcessName, "--version"},
					},
				},
			},
		},
	}
}
