package muse

import (
	"encoding/json"
	"os"
	"path/filepath"

	"irrlicht/core/adapters/inbound/agents/agentpaths"
)

// catalogRootDir is the model-catalog directory Muse caches locally,
// sibling to defaultRootDir under ~/.local/share/muse/. Live-confirmed on
// this machine (2026-09-14, `cat ~/.local/share/muse/model-catalog/*.json`):
// one file, 6d657461__p746268.json, holding a "rows" array with an entry
// {"model_id":"muse-spark-1.3-contributor",...,"context_limit":1007997,...}
// for the session's own default model. Muse's own binary treats this
// directory as its canonical fallback source for a model's effective
// context limit: `strings ~/.local/bin/muse-bin-1.2.1-R2847.1` (run for
// replaydata/agents/muse/scenarios/1-8_model-context-display/metadata.json)
// turns up "[effective context limit from provider_context_limit_tokens or
// model catalog context limit (" and "tbh: model context-limit fetch failed
// (...); using the cached catalog with the default context budget" — muse
// itself falls back to this same file when no live provider override is
// present.
const catalogRootDir = ".local/share/muse/model-catalog"

// modelCatalogRow is one entry of a cached model-catalog file's rows[]
// array — only the two fields this adapter needs are decoded.
type modelCatalogRow struct {
	ModelID      string `json:"model_id"`
	ContextLimit int64  `json:"context_limit"`
}

// modelCatalogFile is the top-level shape of one
// ~/.local/share/muse/model-catalog/*.json file.
type modelCatalogFile struct {
	Rows []modelCatalogRow `json:"rows"`
}

// contextWindowForModel resolves model's context window from the local
// model-catalog cache, returning (0, false) whenever it cannot be resolved
// with confidence — a missing catalog directory, an unreadable or
// unparseable file, or no row naming this exact model — so a caller can
// tell "no context window available" apart from "found a window of zero"
// and never fabricates a number (the model-context-display cell this fixes
// explicitly rules out shipping a guess).
//
// Every *.json file directly under the catalog directory is scanned rather
// than computing the one expected filename from a session's provider_id/
// profile_id: the naming scheme (hex(provider_id)+"__p"+hex(profile_id)+
// ".json") was reverse-engineered from a single observed file on one
// machine (see the 1-8_model-context-display assessment's caveats,
// replaydata/agents/muse/scenarios/1-8_model-context-display/metadata.json)
// and is UNVERIFIED for a second provider/profile combination — scanning
// every file sidesteps that guess entirely and degrades gracefully if a
// future muse release changes the naming or ever writes more than one file.
func contextWindowForModel(model string) (int64, bool) {
	if model == "" {
		return 0, false
	}
	root, err := agentpaths.AbsRoot(catalogRootDir)
	if err != nil {
		return 0, false
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, false
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, e.Name()))
		if err != nil {
			continue
		}
		var catalog modelCatalogFile
		if err := json.Unmarshal(data, &catalog); err != nil {
			continue
		}
		for _, row := range catalog.Rows {
			if row.ModelID == model && row.ContextLimit > 0 {
				return row.ContextLimit, true
			}
		}
	}
	return 0, false
}
