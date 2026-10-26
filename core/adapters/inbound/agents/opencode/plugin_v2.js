// irrlicht session-status plugin for opencode 2.x — installed and owned by irrlichd.
//
// DO NOT EDIT. This file is rewritten whenever irrlicht's own binary path
// changes or the installed opencode changes major, and deleted when the
// "Install status hooks" permission is revoked (also via
// `irrlichd --uninstall-hooks`).
//
// This is the opencode 2.x artifact (#2108). opencode 2.x refuses the 1.x
// plugin shape on every load ("Plugin must export a default definition with an
// id and an effect or setup function"), and 1.x cannot load this one, so
// irrlichd writes exactly one of the two at the same path: this file for a 2.x
// opencode, the 1.x plugin.js for a 1.x one. See hookinstaller.go.
//
// Everything in here is deliberate and narrow:
//
//   - It uses exactly ONE capability of the context opencode hands setup:
//     ctx.event.subscribe(), the read-only event bus. It registers no tool, no
//     auth, no provider, no command, and nothing that could answer a
//     permission prompt for the user or change what opencode does.
//
//   - setup returns at once. The subscription runs in a loop setup starts and
//     does not wait for, so loading this plugin never holds up opencode.
//
//   - It runs inside opencode 2.x's shared background service (`opencode
//     serve --service`), not inside each session, and opencode sets it up once
//     per project directory the service has open. Every one of those instances
//     receives the SAME event stream, so each event reaches the daemon once per
//     instance. The event's own id travels with it as event_id and the daemon
//     drops the repeat — in the daemon, because the instances share nothing
//     this file could coordinate through.
//
//   - Every event that is not one of the five named below costs a string
//     compare and nothing else. Every failure path is swallowed: a monitoring
//     plugin that can break a coding session is worse than no monitoring.
//
// Plain JavaScript, no TypeScript, no imports beyond node:child_process, and
// no dependency on any opencode package: there is no transform between the
// bytes irrlichd writes and the code that runs, and no package resolution.
import { spawn } from "node:child_process";

// The command irrlichd renders at install time (hookbeacon.Command):
// `'<abs irrlichd>' --version hook-post opencode >/dev/null || true`. It
// carries NO host and NO port — the beacon reads the daemon's published
// address at the moment an event fires, so this file never goes stale when the
// daemon moves. hookinstaller.go substitutes the literal below and reads it
// back out again to decide whether an installed copy is current.
const IRRLICHT_BEACON = "__IRRLICHT_BEACON_COMMAND__";

// The five opencode 2.x bus events this plugin forwards, and nothing else.
//
//   permission.asked                — a permission prompt is open and the user
//                                     is blocked on it.
//   permission.replied              — the user answered it.
//   session.execution.succeeded,
//   session.execution.failed,
//   session.execution.interrupted   — the session's turn ended. opencode 2.x
//                                     publishes no session.idle at all.
const IRRLICHT_EVENTS = new Set([
	"permission.asked",
	"permission.replied",
	"session.execution.succeeded",
	"session.execution.failed",
	"session.execution.interrupted",
]);

export default {
	id: "irrlicht",
	setup: async function irrlichtSetup(ctx) {
		forward(ctx);
	},
};

// forward reads the event stream until opencode ends it (it does when the
// project directory's instance shuts down) and hands each event this plugin
// forwards to the beacon. It never rejects: every failure is swallowed.
async function forward(ctx) {
	try {
		for await (const event of ctx.event.subscribe()) {
			try {
				if (!event || !IRRLICHT_EVENTS.has(event.type)) continue;
				const sessionID = event.data?.sessionID;
				if (!sessionID || typeof sessionID !== "string") continue;
				post({
					hook_event_name: event.type,
					session_id: sessionID,
					// A string whatever opencode's own id type is, so the
					// daemon's decode can never refuse the payload over it.
					event_id: event.id == null ? "" : String(event.id),
				});
			} catch {
				// One bad event must not end the subscription.
			}
		}
	} catch {
		// The stream itself failed. Nothing to do; never surface it.
	}
}

// post hands one payload to the beacon on stdin and forgets about it. Every
// failure is swallowed. The beacon bounds its own wait and always exits 0.
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
