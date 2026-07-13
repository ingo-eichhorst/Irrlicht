package transcript

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ExtractCWDFromVibeMetaJSON resolves the working directory for a mistral-vibe
// transcript. Vibe never writes cwd into messages.jsonl, and its sidecar
// (meta.json) doesn't follow the generic <transcript-basename>.json
// convention ExtractCWDFromSidecar guesses (Kiro CLI's shape) — it's a fixed
// filename sitting next to messages.jsonl, nesting cwd under
// environment.working_directory rather than a flat "cwd" key:
//
//	~/.vibe/logs/session/<id>/messages.jsonl
//	~/.vibe/logs/session/<id>/meta.json  → {"environment":{"working_directory":"/abs/cwd"}}
//
// Returns "" for any non-vibe transcript path (base name isn't
// messages.jsonl) or when meta.json is absent/unparseable — the caller falls
// through to its other resolution paths.
func ExtractCWDFromVibeMetaJSON(transcriptPath string) string {
	if filepath.Base(transcriptPath) != "messages.jsonl" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(transcriptPath), "meta.json"))
	if err != nil {
		return ""
	}
	var meta struct {
		Environment struct {
			WorkingDirectory string `json:"working_directory"`
		} `json:"environment"`
	}
	if json.Unmarshal(data, &meta) != nil {
		return ""
	}
	return meta.Environment.WorkingDirectory
}
