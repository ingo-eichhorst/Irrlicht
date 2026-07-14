package codex

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// sessionIDFromPath returns Codex's per-thread metadata ID. A session tree's
// payload.session_id is deliberately not used: it is shared by the parent and
// every child. The rollout filename ends in the same thread UUID, so it is a
// safe fallback while a newly-created file has not yet received its header.
func sessionIDFromPath(path string) string {
	if payload := sessionMetaPayload(path); payload != nil {
		if id, _ := payload["id"].(string); id != "" {
			return id
		}
	}
	return rolloutThreadID(path)
}

// parentSessionIDFromPath reads only a rollout's first metadata record. The
// nested thread_spawn parent is authoritative; the two top-level fields cover
// compatible Codex shapes. Ordinary conversation forks remain top-level:
// their fallback fields are considered only when thread_source is subagent.
func parentSessionIDFromPath(path string) string {
	payload := sessionMetaPayload(path)
	if payload == nil || payload["thread_source"] != "subagent" {
		return ""
	}
	if source, ok := payload["source"].(map[string]interface{}); ok {
		if subagent, ok := source["subagent"].(map[string]interface{}); ok {
			if spawn, ok := subagent["thread_spawn"].(map[string]interface{}); ok {
				if parent, _ := spawn["parent_thread_id"].(string); parent != "" {
					return parent
				}
			}
		}
	}
	for _, key := range []string{"parent_thread_id", "forked_from_id"} {
		if parent, _ := payload[key].(string); parent != "" {
			return parent
		}
	}
	return ""
}

func sessionMetaPayload(path string) map[string]interface{} {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// session_meta can embed the user's instructions, which routinely exceeds
	// Scanner's small default token limit.
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	if !scanner.Scan() {
		return nil
	}
	var record map[string]interface{}
	if err := json.Unmarshal(scanner.Bytes(), &record); err != nil || record["type"] != "session_meta" {
		return nil
	}
	payload, _ := record["payload"].(map[string]interface{})
	return payload
}

func rolloutThreadID(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if !strings.HasPrefix(base, "rollout-") {
		return ""
	}
	if i := strings.LastIndex(base, "-"); i >= 0 && len(base)-i-1 == 12 {
		// A UUID's final group is 12 characters. The preceding groups remain
		// in the suffix and are restored by the fixed-width slice below.
		const uuidLen = 36
		if len(base) >= uuidLen {
			return base[len(base)-uuidLen:]
		}
	}
	return ""
}
