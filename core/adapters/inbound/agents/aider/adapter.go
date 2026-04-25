package aider

// AdapterName identifies sessions originating from the Aider coding agent.
const AdapterName = "aider"

// ProcessName is the OS-level executable name for Aider, used by the
// process scanner via `pgrep -x`.
const ProcessName = "aider"

// rootDir is the path relative to $HOME where Aider stores chat history.
// Aider writes `.aider.chat.history.md` per project, so the root is the
// project working dir; this fallback path is rarely used by the fswatcher
// because Aider's transcript is markdown, not JSONL.
const rootDir = ".aider"
