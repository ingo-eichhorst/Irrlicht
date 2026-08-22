// irrlicht session-status plugin for opencode — installed and owned by irrlichd.
//
// DO NOT EDIT. This file is rewritten whenever irrlicht's own binary path
// changes and deleted when the "Install status hooks" permission is revoked
// (also via `irrlichd --uninstall-hooks`).
//
// It exists because opencode has no config-file hook mechanism: `experimental`
// carries no `hook` key in the version this was measured against, and a plugin
// is the only subscription surface. See hookinstaller.go's header for the audit
// that established that, and for why this file is written into opencode's
// auto-discovery directory rather than registered with npm or a config entry.
//
// Everything in here is deliberate and narrow:
//
//   - It registers exactly ONE hook, `event`, which is opencode's read-only
//     bus tap. It registers no tool, no auth, no provider, no command, and
//     NOTHING that can change what opencode does.
//
//   - It never registers `permission.ask`. That is not a style preference:
//     opencode's Plugin.trigger dispatches named hooks with NO try/catch, and
//     `permission.ask` receives an `output` object whose `status` field the
//     handler may set to "allow" or "deny". A monitoring plugin must not be
//     able to answer a permission prompt on the user's behalf, or to throw
//     inside the agent loop. The `event` bus carries the same fact
//     (`permission.asked`) as an observation rather than a decision.
//
//   - The `event` hook is called for EVERY bus event, including one per
//     streamed message part. The switch below is the first thing it does, and
//     every event that is not one of the three named here costs a string
//     compare and nothing else. Nothing is spawned, allocated or awaited on
//     the common path.
//
//   - Nothing here is awaited on I/O and every failure path is swallowed.
//     opencode runs this in-process; a monitoring plugin that can break a
//     coding session is worse than no monitoring at all.
//
// Plain JavaScript, no TypeScript, no imports beyond node:child_process, and
// no dependency on @opencode-ai/plugin. opencode's loader accepts .ts OR .js
// (ConfigPlugin.load globs "{plugin,plugins}/*.{ts,js}"), so shipping .js means
// there is no transform between the bytes irrlichd writes and the code that
// runs, and no package resolution of any kind.
import { spawn } from "node:child_process";

// The command irrlichd renders at install time: `<abs irrlichd> --version
// >/dev/null && <abs irrlichd> hook-post opencode >/dev/null || true`. It
// carries NO host and NO port — the beacon reads the daemon's published
// address at the moment the hook fires, so this file never goes stale when the
// daemon moves. hookinstaller.go substitutes the literal below and reads it
// back out again to decide whether an installed copy is current.
const IRRLICHT_BEACON = "__IRRLICHT_BEACON_COMMAND__";

// The three opencode bus events this plugin forwards, and nothing else.
//
//   permission.asked   — a permission prompt is open and the user is blocked
//                        on it. opencode publishes this only when a pattern
//                        actually needs asking (Permission.ask returns early
//                        when every pattern already evaluates to "allow"), so
//                        it is a narrow blocked-on-user signal, not a
//                        fires-on-every-tool one.
//   permission.replied — the user answered, including a rejection (opencode
//                        publishes Replied for reply "reject" too, and
//                        cascades it to that session's other pending
//                        requests).
//   session.idle       — the session's runner went idle: the turn ended, was
//                        cancelled, or was interrupted. Authoritative turn end.
const IRRLICHT_EVENTS = new Set(["permission.asked", "permission.replied", "session.idle"]);

export default async function irrlichtPlugin() {
	return {
		// Called for every bus event. Async and fully wrapped: opencode
		// dispatches this inside an Effect.sync with no try/catch of its own,
		// so a synchronous throw here would tear down the subscription for
		// every plugin. An async function cannot throw synchronously, and the
		// try/catch means it cannot reject either.
		event: async function irrlichtEvent(input) {
			try {
				const event = input?.event;
				if (!event || !IRRLICHT_EVENTS.has(event.type)) return;
				const sessionID = event.properties?.sessionID;
				if (!sessionID || typeof sessionID !== "string") return;
				post({ hook_event_name: event.type, session_id: sessionID });
			} catch {
				// Never let a monitoring plugin surface an error into the
				// user's session.
			}
		},
	};
}

// post hands one payload to the beacon on stdin and forgets about it. Every
// failure is swallowed.
//
// The child is deliberately NOT unref()'d, for the reason pi's extension
// records: an unref'd child stops holding the event loop open, and a
// non-interactive run (`opencode run`) can exit immediately after the last
// event — tearing the pipe down before the payload is flushed. The beacon
// bounds its own wait and always exits 0, so holding the loop open costs a
// bounded moment at exit rather than a hang.
function post(payload) {
	try {
		const child = spawn("/bin/sh", ["-c", IRRLICHT_BEACON], {
			stdio: ["pipe", "ignore", "ignore"],
		});
		child.on("error", () => {});
		child.stdin.on("error", () => {});
		child.stdin.end(JSON.stringify(payload));
	} catch {
		// spawn itself threw (no /bin/sh, resource limits). Nothing to do.
	}
}
