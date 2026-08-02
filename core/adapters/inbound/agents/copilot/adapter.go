// Package copilot provides an inbound adapter that watches GitHub Copilot CLI
// transcript files under $COPILOT_HOME/session-state/<session-id>/events.jsonl
// (default ~/.copilot/session-state).
//
// GitHub Copilot CLI (https://github.com/github/copilot-cli) is GitHub's
// terminal coding agent. It appends one JSON object per line to a per-session
// events.jsonl using a uniform envelope — {id, parentId, timestamp, type, data}
// — so a single JSONLineParser covers every signal irrlicht needs. Unlike
// kiro-cli, antigravity and mistral-vibe, no sidecar read is required: the cwd,
// the agent version, explicit turn boundaries, tool-call pairing, permission
// prompts and cumulative token/credit usage are all carried in the transcript
// itself.
//
// The same store is shared by non-CLI clients (verified against the CLI's own
// --acp mode, which writes sessions with client_name "github/acp" rather than
// "github/cli"), so this one adapter covers editor-hosted sessions too.
package copilot

import (
	"path/filepath"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
)

// AdapterName identifies sessions originating from GitHub Copilot CLI. It
// matches the onboarding-factory column slug (replaydata/agents/copilot/).
const AdapterName = "copilot"

// transcriptFilename is the constant basename Copilot writes for every session;
// the session ID therefore comes from the parent directory, not the filename.
const transcriptFilename = "events.jsonl"

// defaultRootDir is the path relative to $HOME where Copilot stores session
// directories when $COPILOT_HOME is unset. Each session is a <session-id>/
// folder holding events.jsonl (the transcript) alongside workspace.yaml,
// session.db and checkpoint/file/research subfolders that this adapter ignores.
const defaultRootDir = ".copilot/session-state"

// copilotHomeEnvVar relocates Copilot's whole config directory, session store
// included. Verified live against CLI 1.0.77: it is the supported successor to
// the --config-dir flag, which still works but now warns
//
//	Warning: --config-dir is deprecated. Use the COPILOT_HOME environment
//	variable instead.
//
// which is also why --config-dir never appears in `copilot --help`. Setting it
// moves session-state/, logs/ and hooks/ together; authentication survives the
// move because credentials live in the OS keychain, not the config directory.
const copilotHomeEnvVar = "COPILOT_HOME"

// ProcessName is the executable basename of a running Copilot CLI agent.
//
// The npm distribution is two processes: `npm-loader.js` runs under node and
// spawnSync's the real ~159 MB native binary
// (@github/copilot-<platform>-<arch>/copilot). The agent is the CHILD, and an
// ExactName match binds it rather than the node parent — pgrep -x compares the
// kernel accounting name (the executable basename, truncated to 15/16 chars),
// which is "copilot" for the child and "node" for the parent. Verified on the
// npm install path only; Homebrew and the install script are untested.
const ProcessName = "copilot"

// sessionsDir returns the directory the Copilot adapter should watch:
//
//	$COPILOT_HOME/session-state   (when COPILOT_HOME is set)
//	~/.copilot/session-state      (default)
//
// Unlike vibe's resolver this reads no file — only the environment — so it is
// safe to evaluate eagerly from Agent() (matching codex and kiro-cli) rather
// than deferring behind DirFunc until the transcripts permission is granted.
func sessionsDir() string {
	return agentpaths.FromEnv("copilot", copilotHomeEnvVar, defaultRootDir, "session-state")
}

// sessionIDFromPath derives the session ID from a transcript path, and reports
// "" for any file the adapter does not own so the watcher skips it. Copilot
// writes
//
//	~/.copilot/session-state/<session-id>/events.jsonl
//
// so the ID is the <session-id> directory one level above the file. Only the
// constant events.jsonl is accepted, so the sibling workspace.yaml, session.db
// and vscode.metadata.json never mint a second session for the same directory.
//
// The directory name is authoritative: it equals session.start.data.sessionId
// and the value the CLI prints for `copilot --resume=<id>` (verified live).
func sessionIDFromPath(path string) string {
	if filepath.Base(path) != transcriptFilename {
		return ""
	}
	id := filepath.Base(filepath.Dir(path))
	if isEmptyOrRootDirName(id) {
		return ""
	}
	return id
}

// isEmptyOrRootDirName reports whether filepath.Base/Dir bottomed out at the
// filesystem root instead of yielding a real directory name.
func isEmptyOrRootDirName(name string) bool {
	return name == "" || name == "." || name == string(filepath.Separator)
}
