package viewer

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"irrlicht/tools/agent-onboarding/internal/validate"
)

// handleScenariosList serves /api/scenarios — every recording cell under
// replaydata/agents/, sorted by (agent, subtree, id).
func (s *Server) handleScenariosList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.store().listScenarios())
}

func (s *Server) handleScenarioDetail(w http.ResponseWriter, r *http.Request) {
	// URL form: /api/scenarios/{agent}/{subtree}/{id}[/recordings[/{name}]]
	rest := strings.TrimPrefix(r.URL.Path, "/api/scenarios/")
	parts := strings.Split(rest, "/")
	if len(parts) < 3 {
		http.Error(w, "usage: /api/scenarios/{agent}/{subtree}/{id}", http.StatusBadRequest)
		return
	}
	agent, subtree, id := parts[0], parts[1], parts[2]
	if subtree != "scenarios" && subtree != "regression" {
		http.Error(w, "subtree must be 'scenarios' or 'regression'", http.StatusBadRequest)
		return
	}
	if !slugRE.MatchString(agent) || !slugRE.MatchString(id) {
		http.Error(w, "agent and id must match ^[a-z0-9][a-z0-9_-]*$", http.StatusBadRequest)
		return
	}
	store := s.store()
	scenarioDir := store.scenarioDir(agent, subtree, id)
	if !store.exists(scenarioDir) {
		http.Error(w, "scenario not found", http.StatusNotFound)
		return
	}
	// Recording history endpoints:
	//   /api/scenarios/{a}/{s}/{id}/recordings        → list archived recordings
	//   /api/scenarios/{a}/{s}/{id}/recordings/{name}  → one archive's detail
	if len(parts) >= 4 && parts[3] == "recordings" {
		if len(parts) == 4 {
			s.handleRecordingsList(w, scenarioDir)
			return
		}
		if len(parts) == 5 {
			s.handleArchivedRecording(w, scenarioDir, parts[4])
			return
		}
	}

	d := ScenarioDetail{Agent: agent, Subtree: subtree, ID: id}
	if b, ok := store.readFile(filepath.Join(scenarioDir, "recording-meta.json")); ok {
		d.Meta = b
	}
	// No events.jsonl sidecar → the viewer will synthesize the timeline from
	// the transcript via the shared classifier engine. Flag it so the UI
	// badges a reconstructed arc rather than passing it off as recorded.
	d.Degraded = !store.exists(filepath.Join(scenarioDir, "events.jsonl"))
	d.Transitions = readTransitionsRaw(filepath.Join(scenarioDir, "events.jsonl"))
	// Synthesize meta from events.jsonl when no recording-meta.json exists —
	// every committed recording falls into this bucket today.
	if d.Meta == nil {
		if synth := synthesizeMetaFromEvents(filepath.Join(scenarioDir, "events.jsonl")); synth != nil {
			d.Meta = synth
		}
	}
	// Spec-grounded expected.jsonl validation. Errors are swallowed so a
	// malformed expected.jsonl doesn't 500 the whole response.
	if rep, err := validate.ValidateExpected(scenarioDir); err == nil && rep != nil {
		d.Expected = rep
	}
	d.Tools = extractToolCalls(filepath.Join(scenarioDir, "transcript.jsonl"))
	d.LatestManifest = buildLatestManifest(scenarioDir, agent, &d, s.RepoRoot)
	d.Assessment = loadAssessment(scenarioDir)
	writeJSON(w, d)
}

// loadAssessment reads <scenarioDir>/assessment.json if present. Returns
// nil on any error — the frontend treats absence as "no assessment yet".
func loadAssessment(scenarioDir string) *AssessmentReport {
	b, err := os.ReadFile(filepath.Join(scenarioDir, "assessment.json"))
	if err != nil {
		return nil
	}
	var rep AssessmentReport
	if err := json.Unmarshal(b, &rep); err != nil {
		return nil
	}
	return &rep
}

// buildLatestManifest produces a RecordingArchive-shaped manifest for the
// live top-level recording so the viewer renders a uniform metadata panel
// for both Latest and archives. Prefers a real manifest.json at the
// scenario root; otherwise synthesizes from already-loaded data. Returns
// nil when there isn't even a top-level events.jsonl to describe.
func buildLatestManifest(scenarioDir, agent string, d *ScenarioDetail, repoRoot string) *RecordingArchive {
	if _, err := os.Stat(filepath.Join(scenarioDir, "events.jsonl")); err != nil {
		return nil
	}
	m := &RecordingArchive{Name: "latest", DaemonVersion: "dev"}
	if b, err := os.ReadFile(filepath.Join(scenarioDir, "manifest.json")); err == nil {
		if err := json.Unmarshal(b, m); err != nil {
			logViewerError("buildLatestManifest: malformed manifest.json in %s: %v", scenarioDir, err)
		}
		m.Name = "latest" // `name` is internal-only — force regardless of file content
		return m
	}
	// Fall back to synthesis from in-memory data.
	if d.Expected != nil {
		if !d.Expected.RecordingStart.IsZero() {
			m.RecordingStartedAt = d.Expected.RecordingStart.Format(time.RFC3339Nano)
		}
		m.ExpectedPassRate = d.Expected.Summary
	}
	if m.RecordingStartedAt == "" && d.Meta != nil {
		var meta struct {
			StartedAt string `json:"started_at"`
		}
		if err := json.Unmarshal(d.Meta, &meta); err == nil {
			m.RecordingStartedAt = meta.StartedAt
		}
	}
	scenarioName := filepath.Base(scenarioDir)
	m.RecipeHash = computeRecipeHash(repoRoot, agent, scenarioName)
	return m
}

// computeRecipeHash mirrors promote-recording.sh's recipe_hash: sha256 of
// the compact-JSON serialization of scenarios.json's
// .scenarios[name].by_adapter[agent] block. Empty string on any failure.
func computeRecipeHash(repoRoot, agent, scenarioName string) string {
	path := filepath.Join(repoRoot, ".claude", "skills", "ir:onboard-agent", "scenarios.json")
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc struct {
		Scenarios []struct {
			Name      string                     `json:"name"`
			ByAdapter map[string]json.RawMessage `json:"by_adapter"`
		} `json:"scenarios"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return ""
	}
	for _, sc := range doc.Scenarios {
		if sc.Name != scenarioName {
			continue
		}
		raw, ok := sc.ByAdapter[agent]
		if !ok {
			return ""
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return ""
		}
		compact, err := json.Marshal(v) // re-marshal compact to match jq -c spacing
		if err != nil {
			return ""
		}
		sum := sha256.Sum256(compact)
		return hex.EncodeToString(sum[:])
	}
	return ""
}

// extractToolCalls walks transcript.jsonl for Anthropic-style tool_use
// blocks inside message.content[], in chronological order. Empty when the
// transcript has no tool calls or isn't JSONL (e.g. aider's .md).
func extractToolCalls(transcriptPath string) []ToolCall {
	f, err := os.Open(transcriptPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var out []ToolCall
	for scanner.Scan() {
		var raw map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &raw); err != nil {
			continue
		}
		msg, _ := raw["message"].(map[string]any)
		if msg == nil {
			continue
		}
		content, _ := msg["content"].([]any)
		if len(content) == 0 {
			continue
		}
		ts, _ := raw["timestamp"].(string)
		sid, _ := raw["sessionId"].(string)
		for _, blkRaw := range content {
			blk, ok := blkRaw.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := blk["type"].(string); t != "tool_use" {
				continue
			}
			name, _ := blk["name"].(string)
			id, _ := blk["id"].(string)
			out = append(out, ToolCall{Ts: ts, SessionID: sid, Name: name, ID: id})
		}
	}
	return out
}

// synthesizeMetaFromEvents builds a recording-meta.json-compatible summary
// by scanning events.jsonl. Used as a fallback when recording-meta.json
// doesn't exist. Marked `synthesized: true` so the frontend renders the
// panel with honest provenance.
func synthesizeMetaFromEvents(path string) json.RawMessage {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	type rawEvent struct {
		Ts        string `json:"ts"`
		Kind      string `json:"kind"`
		SessionID string `json:"session_id"`
		Adapter   string `json:"adapter,omitempty"`
	}
	var (
		adapter         string
		firstTs, lastTs string
		total           int
		kinds           = map[string]int{}
		presessionSet   = map[string]struct{}{}
		realSet         = map[string]struct{}{}
	)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		b := scanner.Bytes()
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		var ev rawEvent
		if err := json.Unmarshal(b, &ev); err != nil {
			continue
		}
		total++
		if firstTs == "" {
			firstTs = ev.Ts
		}
		lastTs = ev.Ts
		if ev.Kind != "" {
			kinds[ev.Kind]++
		}
		if adapter == "" && ev.Adapter != "" {
			adapter = ev.Adapter
		}
		if ev.SessionID != "" {
			if strings.HasPrefix(ev.SessionID, "proc-") {
				presessionSet[ev.SessionID] = struct{}{}
			} else {
				realSet[ev.SessionID] = struct{}{}
			}
		}
	}
	if total == 0 {
		return nil
	}
	var durationMs int64
	if t0, err0 := time.Parse(time.RFC3339Nano, firstTs); err0 == nil {
		if t1, err1 := time.Parse(time.RFC3339Nano, lastTs); err1 == nil {
			durationMs = t1.Sub(t0).Milliseconds()
		}
	}
	keys := func(m map[string]struct{}) []string {
		out := make([]string, 0, len(m))
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}
	doc := map[string]any{
		"synthesized":            true,
		"adapter":                adapter,
		"started_at":             firstTs,
		"ended_at":               lastTs,
		"duration_ms":            durationMs,
		"total_events":           total,
		"kinds":                  kinds,
		"presession_session_ids": keys(presessionSet),
		"real_session_ids":       keys(realSet),
		"session_count":          map[string]int{"presession": len(presessionSet), "real": len(realSet)},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil
	}
	return b
}

// readTransitionsRaw extracts the state_transition rows from events.jsonl,
// plus the three session-end lifecycle kinds reshaped into a synthetic
// "<state> → ∅" transition so the panel shows the session disappearing.
func readTransitionsRaw(path string) []json.RawMessage {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReader(f))
	var out []json.RawMessage
	// First of {presession_removed, transcript_removed, process_exited} per
	// session_id wins, so a re-fired removal doesn't double up.
	ended := make(map[string]bool)
	// Each session's last observed new_state, so the synthetic "ended" row
	// reads as e.g. ready → ∅ instead of ∅ → ∅.
	lastState := make(map[string]string)
	for {
		var raw map[string]json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			if err == io.EOF {
				return out
			}
			return out
		}
		var kind string
		if v, ok := raw["kind"]; ok {
			_ = json.Unmarshal(v, &kind)
		}
		var sid string
		if v, ok := raw["session_id"]; ok {
			_ = json.Unmarshal(v, &sid)
		}
		switch kind {
		case "state_transition":
			var newState string
			if v, ok := raw["new_state"]; ok {
				_ = json.Unmarshal(v, &newState)
			}
			if newState != "" {
				lastState[sid] = newState
			}
			b, _ := json.Marshal(raw)
			out = append(out, b)
		case "transcript_removed", "process_exited", "presession_removed":
			if ended[sid] {
				continue
			}
			ended[sid] = true
			// Reshape into a state_transition-shaped row so the existing
			// renderer just works. "∅" renders as a neutral grey chip.
			raw["kind"] = json.RawMessage(`"state_transition"`)
			raw["new_state"] = json.RawMessage(`"∅"`)
			if kindJSON, err := json.Marshal(kind); err == nil {
				raw["reason"] = json.RawMessage(kindJSON)
			}
			if prev := lastState[sid]; prev != "" {
				if prevJSON, err := json.Marshal(prev); err == nil {
					raw["prev_state"] = json.RawMessage(prevJSON)
				}
			}
			b, _ := json.Marshal(raw)
			out = append(out, b)
		}
	}
}
