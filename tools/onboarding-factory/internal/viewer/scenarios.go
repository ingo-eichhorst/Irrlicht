package viewer

import (
	"bufio"
	"bytes"
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

	"irrlicht/tools/onboarding-factory/internal/shard"
	"irrlicht/tools/onboarding-factory/internal/validate"
)

// eventsFileName is the lifecycle-events sidecar filename recorded alongside
// a recording's transcript.jsonl.
const eventsFileName = "events.jsonl"

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
	// filepath.Base reduces agent/id to a single path segment before the
	// ^[a-z0-9][a-z0-9_-]*$ slug check below — a no-op for any value that
	// already passes the regex (which forbids "/" and "." outright), but
	// filepath.Base is the sanitizer CodeQL's go/path-injection query
	// recognizes for the file reads several hops downstream (recDir,
	// scenarioDir, ...), where a regex match alone doesn't visibly clear
	// the taint (see shard.sanitizePathComponent for the same idiom).
	agent, subtree, id := filepath.Base(parts[0]), parts[1], filepath.Base(parts[2])
	if subtree != "scenarios" && subtree != "regressions" {
		http.Error(w, "subtree must be 'scenarios' or 'regressions'", http.StatusBadRequest)
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
	if s.handleRecordingHistoryRoute(w, scenarioDir, parts) {
		return
	}

	d := ScenarioDetail{Agent: agent, Subtree: subtree, ID: id}
	populateLatestRecordingFields(&d, store, scenarioDir, agent, s.RepoRoot)
	// Spec-grounded expected.jsonl validation (against the newest recording).
	// Errors are swallowed so a malformed expected.jsonl doesn't 500 the response.
	if rep, err := validate.ValidateExpected(scenarioDir); err == nil && rep != nil {
		d.Expected = rep
	}
	d.Assessment = loadAssessment(scenarioDir)
	writeJSON(w, d)
}

// handleRecordingHistoryRoute serves the /recordings and /recordings/{name}
// sub-routes of /api/scenarios/{agent}/{subtree}/{id} — writing the response
// itself when the URL matches one of them. Reports whether it handled the
// request, so the caller returns instead of falling through to the plain
// scenario-detail response.
func (s *Server) handleRecordingHistoryRoute(w http.ResponseWriter, scenarioDir string, parts []string) bool {
	if len(parts) < 4 || parts[3] != "recordings" {
		return false
	}
	if len(parts) == 4 {
		s.handleRecordingsList(w, scenarioDir)
		return true
	}
	if len(parts) == 5 {
		s.handleArchivedRecording(w, scenarioDir, parts[4])
		return true
	}
	return false
}

// populateLatestRecordingFields fills d's recording-derived fields (meta,
// degraded flag, transitions, tools, manifest) from the NEWEST recording
// under scenarioDir — the same recording the recordings list puts first —
// or marks d degraded when no recording has been captured yet.
func populateLatestRecordingFields(d *ScenarioDetail, store RecordingStore, scenarioDir, agent, repoRoot string) {
	recDir, hasRec := validate.NewestRecordingDir(scenarioDir)
	if !hasRec {
		d.Degraded = true
		return
	}
	// Rebuild recDir from its own filepath.Base() rather than trusting the
	// string NewestRecordingDir returned directly — a no-op round trip
	// (recDir is already exactly scenarioDir/recordings/<name>) that gives
	// every os.Open/os.ReadFile below a value CodeQL's path-injection query
	// recognizes as derived from a sanitizer, several hops closer to each
	// sink than the agent/id validation up in the URL parsing above.
	recDir = filepath.Join(scenarioDir, "recordings", filepath.Base(recDir))
	d.LatestRecording = filepath.Base(recDir)
	if b, ok := store.readFile(filepath.Join(recDir, "recording-meta.json")); ok {
		d.Meta = b
	}
	// No events.jsonl sidecar → the viewer synthesizes the timeline from the
	// transcript via the shared classifier engine. Flag it so the UI badges a
	// reconstructed arc rather than passing it off as recorded.
	d.Degraded = !store.exists(filepath.Join(recDir, eventsFileName))
	d.Transitions = readTransitionsRaw(filepath.Join(recDir, eventsFileName))
	if d.Meta == nil {
		if synth := synthesizeMetaFromEvents(filepath.Join(recDir, eventsFileName)); synth != nil {
			d.Meta = synth
		}
	}
	d.Tools = extractToolCalls(filepath.Join(recDir, "transcript.jsonl"))
	d.LatestManifest = buildLatestManifest(recDir, agent, d, repoRoot)
}

// loadAssessment returns the cell's Stage-1 assessment. Post-#510 a scenarios/
// cell's assessment lives in the per-scenario shard (the single source); a
// regression/ cell keeps its own on-disk assessment.json (regression fixtures
// are not in the shard catalog). Returns nil when absent or unparseable — the
// frontend treats absence as "no assessment yet".
//
// scenarioDir is …/replaydata/agents/<agent>/<subtree>/<id>; we recover the
// pieces from it so the call site stays a one-arg call.
func loadAssessment(scenarioDir string) *AssessmentReport {
	id := filepath.Base(scenarioDir)
	subtree := filepath.Base(filepath.Dir(scenarioDir))
	agent := filepath.Base(filepath.Dir(filepath.Dir(scenarioDir)))
	repoRoot := repoRootFromScenarioDir(scenarioDir)
	// Rebuild scenarioDir from the filepath.Base()-derived components above
	// instead of trusting the caller-supplied string directly for the disk
	// read below — a no-op round trip for any legitimate scenarioDir.
	scenarioDir = filepath.Join(repoRoot, "replaydata", "agents", agent, subtree, id)

	if subtree != "scenarios" {
		return loadAssessmentFromDisk(scenarioDir) // regression/ — on disk
	}

	cell, ok := shardCellForFolder(repoRoot, agent, id)
	if !ok || len(cell.Details.Assessment) == 0 {
		return nil
	}
	var rep AssessmentReport
	if err := json.Unmarshal(cell.Details.Assessment, &rep); err != nil {
		return nil
	}
	return &rep
}

// loadAssessmentFromDisk reads <scenarioDir>/assessment.json (the regression/
// path, where no shard exists). nil on any error.
func loadAssessmentFromDisk(scenarioDir string) *AssessmentReport {
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

// repoRootFromScenarioDir recovers the repo root from a scenario dir shaped
// …/replaydata/agents/<agent>/<subtree>/<id> (five segments up from <id>).
func repoRootFromScenarioDir(scenarioDir string) string {
	return filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(scenarioDir)))))
}

// shardCellForFolder finds the (agent) cell whose recording folder is `folder`.
// The detail endpoint is keyed by the on-disk recording folder (the id-prefixed
// scenario name for standard cells, or a variant name otherwise). metadata.json
// lives in the same directory as the recordings, so a direct load by folder
// name is always correct.
func shardCellForFolder(repoRoot, agent, folder string) (shard.ShardAgent, bool) {
	cell, ok := shard.LoadAgentCell(repoRoot, agent, folder)
	if !ok {
		return shard.ShardAgent{}, false
	}
	return *cell, true
}

// buildLatestManifest produces a RecordingArchive-shaped manifest for the
// live top-level recording so the viewer renders a uniform metadata panel
// for the newest and older recordings alike. recDir is the recording dir
// (recordings/<name>/); it prefers a real manifest.json there, otherwise
// synthesizes from already-loaded data. Returns nil when recDir has no
// events.jsonl to describe. The recipe-hash is keyed by the CELL folder
// (filepath.Base of recDir's grandparent), not the recording name.
func buildLatestManifest(recDir, agent string, d *ScenarioDetail, repoRoot string) *RecordingArchive {
	if _, err := os.Stat(filepath.Join(recDir, eventsFileName)); err != nil {
		return nil
	}
	m := &RecordingArchive{Name: filepath.Base(recDir), DaemonVersion: "dev"}
	if b, err := os.ReadFile(filepath.Join(recDir, "manifest.json")); err == nil {
		if err := json.Unmarshal(b, m); err != nil {
			logViewerError("buildLatestManifest: malformed manifest.json in %s: %v", recDir, err)
		}
		m.Name = filepath.Base(recDir)
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
	// Cell folder = recDir/../.. (recordings/<name> → cell).
	cellFolder := filepath.Base(filepath.Dir(filepath.Dir(recDir)))
	m.RecipeHash = computeRecipeHash(repoRoot, agent, cellFolder)
	return m
}

// computeRecipeHash mirrors promote-recording.sh's recipe_hash: sha256 of the
// compact-JSON recipe block. The recipe lives in the cell's metadata.json.
// scenarioName is the on-disk recording folder. Empty string on any failure.
func computeRecipeHash(repoRoot, agent, scenarioName string) string {
	cell, ok := shard.LoadAgentCell(repoRoot, agent, scenarioName)
	if !ok {
		return ""
	}
	return recipeHashOf(cell.Details.Recipe)
}

// recipeHashOf returns the sha256 of the compact-JSON form of a recipe block,
// matching promote-recording.sh's `jq -c … | shasum -a 256`. It uses
// json.Compact, which strips insignificant whitespace while PRESERVING source
// key order — exactly what `jq -c` does. The earlier Unmarshal→Marshal round
// trip sorted object keys alphabetically (Go marshals maps sorted), so its
// hash only matched jq when the source keys already happened to be alphabetical
// and silently diverged otherwise. Empty string on empty input or malformed
// JSON. Reused by the shard readers, which hash a recipe RawMessage directly.
func recipeHashOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return ""
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
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
		out = append(out, toolCallsInLine(scanner.Bytes())...)
	}
	return out
}

// toolCallsInLine extracts the tool_use blocks from one transcript.jsonl
// line's message.content[], in order. Empty when the line isn't a message
// event, has no content, or is malformed JSON.
func toolCallsInLine(line []byte) []ToolCall {
	var raw map[string]any
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil
	}
	msg, _ := raw["message"].(map[string]any)
	if msg == nil {
		return nil
	}
	content, _ := msg["content"].([]any)
	if len(content) == 0 {
		return nil
	}
	ts, _ := raw["timestamp"].(string)
	sid, _ := raw["sessionId"].(string)
	var out []ToolCall
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
	st := scanEventStats(f)
	if st.total == 0 {
		return nil
	}
	var durationMs int64
	if t0, err0 := time.Parse(time.RFC3339Nano, st.firstTs); err0 == nil {
		if t1, err1 := time.Parse(time.RFC3339Nano, st.lastTs); err1 == nil {
			durationMs = t1.Sub(t0).Milliseconds()
		}
	}
	doc := map[string]any{
		"synthesized":            true,
		"adapter":                st.adapter,
		"started_at":             st.firstTs,
		"ended_at":               st.lastTs,
		"duration_ms":            durationMs,
		"total_events":           st.total,
		"kinds":                  st.kinds,
		"presession_session_ids": sortedKeys(st.presessionSet),
		"real_session_ids":       sortedKeys(st.realSet),
		"session_count":          map[string]int{"presession": len(st.presessionSet), "real": len(st.realSet)},
	}
	b, err := json.Marshal(doc)
	if err != nil {
		return nil
	}
	return b
}

// eventScanStats aggregates the per-event fields synthesizeMetaFromEvents
// needs from one events.jsonl scan.
type eventScanStats struct {
	adapter         string
	firstTs, lastTs string
	total           int
	kinds           map[string]int
	presessionSet   map[string]struct{}
	realSet         map[string]struct{}
}

// scanEventStats scans events.jsonl-shaped lines from r, aggregating the
// first/last timestamp, per-kind counts, adapter, and the presession vs.
// real session_id sets. Malformed or blank lines are skipped.
func scanEventStats(r io.Reader) eventScanStats {
	type rawEvent struct {
		Ts        string `json:"ts"`
		Kind      string `json:"kind"`
		SessionID string `json:"session_id"`
		Adapter   string `json:"adapter,omitempty"`
	}
	st := eventScanStats{
		kinds:         map[string]int{},
		presessionSet: map[string]struct{}{},
		realSet:       map[string]struct{}{},
	}
	scanner := bufio.NewScanner(r)
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
		st.total++
		if st.firstTs == "" {
			st.firstTs = ev.Ts
		}
		st.lastTs = ev.Ts
		if ev.Kind != "" {
			st.kinds[ev.Kind]++
		}
		if st.adapter == "" && ev.Adapter != "" {
			st.adapter = ev.Adapter
		}
		recordSessionID(st.presessionSet, st.realSet, ev.SessionID)
	}
	return st
}

// recordSessionID buckets a non-empty session_id into the presession or
// real set, based on the "proc-" prefix synthetic pre-session IDs carry.
func recordSessionID(presessionSet, realSet map[string]struct{}, sessionID string) {
	if sessionID == "" {
		return
	}
	if strings.HasPrefix(sessionID, "proc-") {
		presessionSet[sessionID] = struct{}{}
		return
	}
	realSet[sessionID] = struct{}{}
}

// sortedKeys returns m's keys in sorted order.
func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
		// Both a clean EOF and any other decode error end the scan the same
		// way: return whatever rows have been collected so far.
		if err := dec.Decode(&raw); err != nil {
			return out
		}
		if b, ok := transitionRow(raw, ended, lastState); ok {
			out = append(out, b)
		}
	}
}

// transitionRow turns one decoded events.jsonl record into a transitions-panel
// row, if it's relevant: a state_transition row passes through (after
// recording its new_state); one of the three session-end lifecycle kinds is
// reshaped into a synthetic "<state> → ∅" row. Any other kind reports
// ok=false.
func transitionRow(raw map[string]json.RawMessage, ended map[string]bool, lastState map[string]string) (json.RawMessage, bool) {
	kind := decodeStringField(raw, "kind")
	sid := decodeStringField(raw, "session_id")
	switch kind {
	case "state_transition":
		return stateTransitionRow(raw, sid, lastState)
	case "transcript_removed", "process_exited", "presession_removed":
		return sessionEndedRow(raw, kind, sid, ended, lastState)
	default:
		return nil, false
	}
}

// decodeStringField unmarshals raw[key] into a string, or "" when the key
// is absent or its value isn't a JSON string.
func decodeStringField(raw map[string]json.RawMessage, key string) string {
	v, ok := raw[key]
	if !ok {
		return ""
	}
	var s string
	_ = json.Unmarshal(v, &s)
	return s
}

// stateTransitionRow records the row's new_state (so a later session-end row
// can report the state the session left from) and marshals the row unchanged.
func stateTransitionRow(raw map[string]json.RawMessage, sid string, lastState map[string]string) (json.RawMessage, bool) {
	if newState := decodeStringField(raw, "new_state"); newState != "" {
		lastState[sid] = newState
	}
	b, _ := json.Marshal(raw)
	return b, true
}

// sessionEndedRow reshapes a session-end lifecycle event into a synthetic
// state_transition-shaped row ("<prev_state> → ∅") so the existing renderer
// just works. Only the first ended event per session_id produces a row.
func sessionEndedRow(raw map[string]json.RawMessage, kind, sid string, ended map[string]bool, lastState map[string]string) (json.RawMessage, bool) {
	if ended[sid] {
		return nil, false
	}
	ended[sid] = true
	// "∅" renders as a neutral grey chip.
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
	return b, true
}
