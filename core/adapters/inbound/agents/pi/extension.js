// irrlicht session-status extension for pi — installed and owned by irrlichd.
//
// DO NOT EDIT. This file is rewritten whenever irrlicht's own binary path
// changes and deleted when the "Install status hooks" permission is revoked
// (also via `irrlichd --uninstall-hooks`).
//
// It exists because pi has no config-file hook mechanism at all: the ONLY
// way to subscribe to pi's lifecycle is an extension module. See
// hookinstaller.go's header for the audit that established that, and for why
// this file is written directly into pi's auto-discovery directory rather
// than installed with `pi install`.
//
// Everything in here is deliberate and narrow:
//
//   - It subscribes to exactly ONE event, agent_settled, and does exactly one
//     thing with it: hand the session's transcript path — and, when herdr set
//     them, the two variables that name this pane — to the beacon on stdin. It
//     registers no tool, no command, no provider and no UI.
//
//   - Those two variables are the ONLY thing it reads from the process it runs
//     in, they are read by literal name, and it reads them for a reason no
//     amount of work on the daemon side removes (#1936). The daemon reads a
//     process's environment from outside, which on darwin means
//     sysctl(kern.procargs2); a Node agent that sets process.title overwrites
//     the argv+env region that call exposes, so for pi the daemon reads an
//     empty environment and the session gets no pane (#1934). Inside the
//     process the same values are still in process.env. This extension is
//     therefore the one vantage point from which they can be seen at all.
//     Nothing else about the process is read, and nothing is sent that the
//     daemon would not have read for a non-Node agent.
//
//   - It never subscribes to `tool_call`. That is not a style preference:
//     pi's own runner dispatches every OTHER event inside a try/catch and
//     logs a throwing handler, but emitToolCall (dist/core/extensions/
//     runner.js) has no catch, and a `tool_call` handler that returns
//     `{block: true}` — or throws — BLOCKS the user's tool call. A monitoring
//     extension must not be able to do that. extension_test.go's
//     TestShippedExtensionSubscribesToNoBlockingEvent is the guard.
//
//   - Nothing in here is awaited on I/O. pi awaits each handler, so a
//     handler that waited for the daemon to answer would stall the agent
//     whenever irrlicht is slow or down. spawn() returns immediately and
//     every failure path is swallowed.
//
// Plain JavaScript, not TypeScript, and no imports beyond node:child_process.
// pi's loader accepts both (`isExtensionFile` matches .ts OR .js) and loads
// through jiti, so no compile step and no toolchain is involved either way —
// but a file with no types and no third-party import is a file whose whole
// behaviour is readable in one screen, which is the point when the artifact
// being installed is code.
import { spawn } from "node:child_process";

// The command irrlichd renders at install time: `<abs irrlichd> --version
// >/dev/null && <abs irrlichd> hook-post pi >/dev/null || true`. It carries
// NO host and NO port — the beacon reads the daemon's published address at
// the moment the hook fires, so this file never goes stale when the daemon
// moves. hookinstaller.go substitutes the literal below and reads it back
// out again to decide whether an installed copy is current.
const IRRLICHT_BEACON = "__IRRLICHT_BEACON_COMMAND__";

// The single pi lifecycle event this extension subscribes to. agent_settled
// means "pi will not continue running automatically" — no retry, no
// auto-compaction, no queued follow-up left — which is the authoritative
// turn-end signal irrlicht's lifecycle state model wants, and which pi's
// transcript alone only implies.
const IRRLICHT_EVENT = "agent_settled";

export default function (pi) {
	pi.on(IRRLICHT_EVENT, (_event, ctx) => {
		// getSessionFile() is undefined for an ephemeral session (pi run with
		// no session file). There is no transcript for irrlicht to correlate
		// to in that case, so there is nothing to report.
		let transcriptPath;
		try {
			transcriptPath = ctx && ctx.sessionManager && ctx.sessionManager.getSessionFile();
		} catch {
			return;
		}
		if (!transcriptPath) return;
		post({
			hook_event_name: IRRLICHT_EVENT,
			transcript_path: transcriptPath,
			herdr_pane_id: herdrVar("HERDR_PANE_ID"),
			herdr_socket_path: herdrVar("HERDR_SOCKET_PATH"),
		});
	});
}

// herdrVar returns one herdr environment variable, or undefined when it is
// unset, empty or not a string. JSON.stringify drops an undefined value, so a
// session running outside herdr sends byte-for-byte the payload it sent
// before this was added.
//
// The two names are literal arguments at the single call site above rather
// than a prefix scan, so what this function can return is fixed at read time
// and readable here: no other variable is reachable through it.
//
// Wrapped in try/catch for the same reason everything else here is — pi awaits
// this handler, and a monitoring extension must not be able to fail the
// agent's turn. `process` is a Node global, so this needs no import.
function herdrVar(name) {
	try {
		const value = process.env[name];
		return typeof value === "string" && value !== "" ? value : undefined;
	} catch {
		return undefined;
	}
}

// post hands one payload to the beacon on stdin and forgets about it. Every
// failure is swallowed: this extension runs inside the user's agent, and a
// monitoring tool that can break a coding session is worse than no
// monitoring at all.
//
// The child is deliberately NOT unref()'d. An unref'd child stops holding
// the event loop open, and in pi's print mode (`pi -p`) agent_settled is
// followed almost immediately by process exit — which can tear the pipe down
// before the payload is flushed. The beacon bounds its own wait and always
// exits 0, so holding the loop open costs a bounded moment at exit rather
// than a hang.
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
