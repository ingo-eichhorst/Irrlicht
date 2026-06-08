// Package geminicli provides an inbound adapter that watches Gemini CLI
// session transcripts under ~/.gemini/tmp/<project>/chats/*.jsonl.
//
// Gemini CLI (the `gemini` Node binary) writes one append-only JSONL file
// per session. The first line is a session header; subsequent lines are
// either bare message objects ({id,timestamp,type,content,...}) appended as
// the conversation advances, or {"$set":{...}} mutation envelopes that
// initialise the messages array (bootstrap session context) or merely bump
// `lastUpdated`. See parser.go for the line-shape details.
package geminicli

// AdapterName identifies sessions originating from Gemini CLI.
const AdapterName = "gemini-cli"

// ProcessName is the OS-level executable name. Gemini CLI runs under Node
// (`node .../bin/gemini`), so this is only the scanner's pgrep -x fallback —
// the CommandPattern matcher (see agent.go) is what actually finds the
// process via its command line.
const ProcessName = "gemini"

// defaultRootDir is the path relative to $HOME where Gemini CLI stores
// session transcripts. Sessions live under tmp/<project>/chats/*.jsonl;
// the fswatcher recurses into the per-project chats/ subdirectories and
// ignores the sibling logs.json / .project_root files (non-.jsonl).
const defaultRootDir = ".gemini/tmp"

// sessionsDir returns the directory the Gemini CLI adapter should watch.
func sessionsDir() string {
	return defaultRootDir
}
