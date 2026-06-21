// Package antigravity provides an inbound adapter that watches Google
// Antigravity session transcripts. Antigravity ships two surfaces on one agent
// harness — the `agy` CLI and the Antigravity IDE — that write a byte-compatible
// transcript format into sibling stores:
//
//	CLI: ~/.gemini/antigravity-cli/brain/<conv-id>/.system_generated/logs/transcript.jsonl
//	IDE: ~/.gemini/antigravity/brain/<conv-id>/.system_generated/logs/transcript.jsonl
//
// Discovery is transcript-first, so one adapter covers both surfaces: each
// transcript file is one session regardless of how many OS processes exist. The
// IDE hosts N conversations in one Electron process (no per-conversation PID),
// which is fine — sessions with PID==0 are first-class, with state derived
// entirely from transcript activity. The `agy` CLI is a standalone native
// binary (1 process ↔ 1 conversation), so the ExactName matcher adds optional
// liveness + terminal-jump enrichment for CLI sessions only.
//
// Two layout wrinkles drive the Source declaration (see agent.go):
//   - the CLI and IDE brain stores are sibling-but-separate, so the Source
//     declares the IDE store via ExtraDirs (rooting at the shared ~/.gemini
//     parent would collide with the Gemini CLI adapter's ~/.gemini/tmp);
//   - the transcript filename is the constant transcript.jsonl, so the session
//     ID comes from the <conv-id> directory via SessionIDFromPath, which also
//     skips the sibling transcript_full.jsonl (the unfiltered view).
package antigravity

import "path/filepath"

// AdapterName identifies sessions originating from Antigravity (CLI or IDE).
const AdapterName = "antigravity"

// ProcessName is the OS-level executable name of the CLI. Antigravity's `agy`
// is a standalone native binary, so ExactName{agy} matches it directly (no Node
// wrapper, unlike Gemini CLI). The IDE has no per-conversation process and is
// covered transcript-only.
const ProcessName = "agy"

// transcriptFilename is the constant basename of the filtered transcript view.
// The sibling transcript_full.jsonl (unfiltered) is ignored — see
// sessionIDFromPath.
const transcriptFilename = "transcript.jsonl"

// cliBrainDir / ideBrainDir are the two $HOME-relative brain stores. The CLI
// writes under antigravity-cli/, the IDE under antigravity/ (same layout).
const (
	cliBrainDir = ".gemini/antigravity-cli/brain"
	ideBrainDir = ".gemini/antigravity/brain"
)

// sessionIDFromPath derives the conversation ID from a transcript path and
// reports "" for any file the adapter does not own. Antigravity writes
//
//	<brain>/<conv-id>/.system_generated/logs/transcript.jsonl
//
// so the session ID is the <conv-id> directory three levels above the file.
// Only the filtered transcript.jsonl is accepted; the sibling
// transcript_full.jsonl (and anything else) returns "" so the watcher skips it,
// minting exactly one session per conversation.
func sessionIDFromPath(path string) string {
	if filepath.Base(path) != transcriptFilename {
		return ""
	}
	logs := filepath.Dir(path)
	if filepath.Base(logs) != "logs" {
		return ""
	}
	sysGen := filepath.Dir(logs)
	if filepath.Base(sysGen) != ".system_generated" {
		return ""
	}
	conv := filepath.Base(filepath.Dir(sysGen))
	if conv == "" || conv == "." || conv == string(filepath.Separator) {
		return ""
	}
	return conv
}
