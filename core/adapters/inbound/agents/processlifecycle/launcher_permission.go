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
			Touches:         "Reads a fixed whitelist of environment variables from detected agent processes (TERM_PROGRAM, KITTY_*, TMUX, HERDR_*, …), and for a herdr pane from the herdr client displaying it, or from herdr itself when the process hides its own environment",
			Detail: "When a session is linked to its process, irrlicht reads only " +
				"these variables from that process's environment: TERM_PROGRAM, " +
				"ITERM_SESSION_ID, TERM_SESSION_ID, TMUX, TMUX_PANE, VSCODE_PID, " +
				"TERMINAL_EMULATOR, KITTY_PID, KITTY_LISTEN_ON, KITTY_WINDOW_ID, " +
				"HERDR_PANE_ID, HERDR_SOCKET_PATH — " +
				"never the full environment. A session running in a herdr pane is " +
				"displayed by a separate herdr client process, so for those the same " +
				"whitelist is also read from that client (found via the session's own " +
				"socket path) — without it there is no window to jump to. Some agents " +
				"overwrite their own environment as they start, so nothing above can be " +
				"read from them at all; for those, irrlicht asks the local herdr server " +
				"over its control socket which pane holds the process, and reads no " +
				"other pane's contents (#1934). One agent can answer for itself " +
				"instead: a pi session runs an irrlicht extension, and that extension " +
				"reads the two herdr variables from inside the process and sends them " +
				"with its turn-end signal, so the pane is known without asking herdr " +
				"to find it (#1936). Such a report is confirmed against herdr before " +
				"it is used, and it is only stored while this permission is granted — " +
				"turned off, it is discarded on arrival. Focusing " +
				"itself only happens when you " +
				"click a session (kitty remote control / AppleScript, additionally " +
				"gated by macOS automation prompts). Toggling off stops the capture " +
				"immediately; without it, click-to-focus falls back to app-level " +
				"activation.",
		}},
	}
}
