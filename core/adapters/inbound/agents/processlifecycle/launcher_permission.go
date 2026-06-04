// launcher_permission.go declares the consent surface for launcher-identity
// capture (issue #570). ReadLauncherEnv reads a whitelist of environment
// variables from agent processes — a distinct read kind the wizard must
// name, even though it only ever runs for sessions that already passed an
// agent's observe gate. The daemon wires the actual gate as a Granted()
// check around the reader, so no Apply/Remove closures are needed.
package processlifecycle

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// LauncherName identifies the launcher-capture pseudo-entry in the
// permission store and wizard.
const LauncherName = "launcher"

// PermissionKeyLauncherEnv gates the launcher-identity env capture.
const PermissionKeyLauncherEnv = "env"

// LauncherPermissionDeclaration returns the consent declaration for
// launcher-identity capture. Like the Gas Town orchestrator it isn't a
// coding-agent adapter (no Source/Process axes) — it's a daemon-wide
// capability gated through the same wizard.
func LauncherPermissionDeclaration() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{Name: LauncherName, DisplayName: "Terminal focus"},
		Permissions: []agent.Permission{{
			Key:             PermissionKeyLauncherEnv,
			Kind:            permission.KindObserve,
			Title:           "Capture terminal identity",
			FeatureUnlocked: "Click-to-focus: jump from a session row or notification straight to the terminal window that runs it",
			Touches:         "Reads a fixed whitelist of environment variables from detected agent processes (TERM_PROGRAM, KITTY_*, TMUX, …)",
			Detail: "When a session is linked to its process, irrlicht reads only " +
				"these variables from that process's environment: TERM_PROGRAM, " +
				"ITERM_SESSION_ID, TERM_SESSION_ID, TMUX, TMUX_PANE, VSCODE_PID, " +
				"TERMINAL_EMULATOR, KITTY_PID, KITTY_LISTEN_ON, KITTY_WINDOW_ID — " +
				"never the full environment. Focusing itself only happens when you " +
				"click a session (kitty remote control / AppleScript, additionally " +
				"gated by macOS automation prompts). Toggling off stops the capture " +
				"immediately; without it, click-to-focus falls back to app-level " +
				"activation.",
		}},
	}
}
