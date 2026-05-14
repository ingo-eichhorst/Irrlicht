// Package viewer serves the Phase 7 replay viewer: a localhost web UI
// for inspecting recordings (signals.jsonl + events.jsonl + frames/ +
// optional ground_truth.jsonl + optional validate result).
//
// API:
//   GET  /                                          — embedded SPA
//   GET  /api/scenarios                             — list of recordings
//   GET  /api/scenarios/{agent}/{subtree}/{id}      — recording detail (signals + meta + gt + transitions)
//   GET  /api/scenarios/{agent}/{subtree}/{id}/frame/{name} — single frame's text
//
// `subtree` is "scenarios" or "regression"; the recordings live at
// replaydata/agents/<agent>/<subtree>/<id>/.
package viewer

import (
	"bufio"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"irrlicht/tools/agent-onboarding/internal/groundtruth"
)

// slugRE constrains user-supplied URL components (agent, scenario id) so
// they can never traverse out of replaydata/agents/. Matches the same
// shape the survey schema enforces for agent slugs.
var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

//go:embed web/*
var webFS embed.FS

// Server is the viewer HTTP server.
type Server struct {
	RepoRoot string // path containing replaydata/

	// playback manages the single active replay session. Lazily created
	// on Handler() so callers can construct a Server with just RepoRoot
	// (matching the pre-playback API).
	playback *PlaybackManager
}

// PlaybackManager returns the server's playback manager, initialising it
// if necessary. Used by main.go to seed an auto-playback at boot.
func (s *Server) PlaybackManager() *PlaybackManager {
	if s.playback == nil {
		s.playback = NewPlaybackManager(s.RepoRoot)
	}
	return s.playback
}

// Handler returns the http.Handler the CLI wires into ListenAndServe.
func (s *Server) Handler() http.Handler {
	if s.playback == nil {
		s.playback = NewPlaybackManager(s.RepoRoot)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/scenarios/", s.handleScenarioDetail) // path with trailing parts
	mux.HandleFunc("/api/scenarios", s.handleScenariosList)
	mux.HandleFunc("/api/catalog", s.handleCatalog)
	s.playback.registerPlaybackRoutes(mux)
	mux.Handle("/", s.staticHandler())
	return mux
}

// staticHandler serves the embedded web/ tree at /.
func (s *Server) staticHandler() http.Handler {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		// Embedded FS misconfiguration; fall back to "no UI" handler.
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "embedded UI unavailable", http.StatusInternalServerError)
		})
	}
	return http.FileServerFS(sub)
}

// handleCatalog serves the maintainer-curated scenario coverage
// catalog at `.specs/agent-scenarios-coverage.json` — the source of
// truth for the per-agent applicability matrix (38 scenarios × 5
// agents, each with agent_supports / irrlicht_observes verdicts and
// notes). Falls back to `.claude/skills/ir:onboard-agent/scenarios.json`
// (which only carries the 8 actively-driven cells) when the coverage
// file isn't available.
//
// `.specs/` is gitignored, so in a git worktree (`.git` is a file
// pointing back to the common `.git/worktrees/<id>/`) the coverage
// file lives in the user's main checkout, not the worktree. We
// resolve the main checkout via `git rev-parse --git-common-dir`
// equivalent — read the worktree's .git file, follow its gitdir, and
// walk back up to find the main worktree.
//
// Re-reads on every request so maintainer edits land on next page
// refresh without a server rebuild.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	// 1. Prefer the richer coverage file when it's reachable.
	if covPath := s.resolveCoveragePath(); covPath != "" {
		if b, err := os.ReadFile(covPath); err == nil {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.Header().Set("X-Catalog-Source", "coverage")
			w.Write(b)
			return
		}
	}
	// 2. Fall back to scenarios.json — the run-cell.sh catalog. Smaller
	//    surface (only declared cells, no verdicts), but always present
	//    in the repo so the matrix is never empty.
	path := filepath.Join(s.RepoRoot, ".claude", "skills", "ir:onboard-agent", "scenarios.json")
	b, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, fmt.Sprintf("read scenarios.json: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Catalog-Source", "scenarios")
	w.Write(b)
}

// resolveCoveragePath finds the maintainer's
// .specs/agent-scenarios-coverage.json. Looks in the repo root first,
// then in the main checkout when the repo root is a git worktree.
// Returns "" if neither has the file.
func (s *Server) resolveCoveragePath() string {
	// Direct hit (main checkout, or a worktree the user has populated).
	direct := filepath.Join(s.RepoRoot, ".specs", "agent-scenarios-coverage.json")
	if _, err := os.Stat(direct); err == nil {
		return direct
	}
	// Worktree: <repo>/.git is a file containing "gitdir: <path>" where
	// <path> is <main>/.git/worktrees/<id>. The main checkout is the
	// parent of <path>/../.. (two levels up: workouts/<id> → workouts/
	// → .git/), then one more for the .git dir itself.
	gitMeta := filepath.Join(s.RepoRoot, ".git")
	st, err := os.Stat(gitMeta)
	if err != nil || st.IsDir() {
		return ""
	}
	data, err := os.ReadFile(gitMeta)
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(data))
	const prefix = "gitdir:"
	if !strings.HasPrefix(line, prefix) {
		return ""
	}
	gitdir := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	// gitdir = <main>/.git/worktrees/<id>; main checkout = grandparent
	// of grandparent (worktrees/<id> → worktrees → .git → <main>).
	main := filepath.Dir(filepath.Dir(filepath.Dir(gitdir)))
	candidate := filepath.Join(main, ".specs", "agent-scenarios-coverage.json")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	return ""
}

// ScenarioListEntry is one row in /api/scenarios.
type ScenarioListEntry struct {
	Agent          string `json:"agent"`
	Subtree        string `json:"subtree"` // "scenarios" | "regression"
	ID             string `json:"id"`
	HasGroundTruth bool   `json:"has_ground_truth"`
	HasSignals     bool   `json:"has_signals"`
	HasFrames      bool   `json:"has_frames"`
}

func (s *Server) handleScenariosList(w http.ResponseWriter, r *http.Request) {
	root := filepath.Join(s.RepoRoot, "replaydata", "agents")
	entries, _ := os.ReadDir(root)
	var out []ScenarioListEntry
	for _, agentEntry := range entries {
		if !agentEntry.IsDir() {
			continue
		}
		agent := agentEntry.Name()
		for _, subtree := range []string{"scenarios", "regression"} {
			subPath := filepath.Join(root, agent, subtree)
			scns, _ := os.ReadDir(subPath)
			for _, sd := range scns {
				if !sd.IsDir() {
					continue
				}
				id := sd.Name()
				scenarioDir := filepath.Join(subPath, id)
				out = append(out, ScenarioListEntry{
					Agent: agent, Subtree: subtree, ID: id,
					HasGroundTruth: fileExists(filepath.Join(scenarioDir, "ground_truth.jsonl")),
					HasSignals:     fileExists(filepath.Join(scenarioDir, "signals.jsonl")),
					HasFrames:      fileExists(filepath.Join(scenarioDir, "frames.jsonl")),
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		if out[i].Subtree != out[j].Subtree {
			return out[i].Subtree < out[j].Subtree
		}
		return out[i].ID < out[j].ID
	})
	writeJSON(w, out)
}

// ScenarioDetail is the payload for /api/scenarios/{agent}/{subtree}/{id}.
type ScenarioDetail struct {
	Agent       string                  `json:"agent"`
	Subtree     string                  `json:"subtree"`
	ID          string                  `json:"id"`
	Meta        json.RawMessage         `json:"meta,omitempty"`         // recording-meta.json or null
	GroundTruth *GroundTruthBlob        `json:"ground_truth,omitempty"` // ground_truth.jsonl parsed
	Signals     []json.RawMessage       `json:"signals"`                // all signals.jsonl rows
	Transitions []json.RawMessage       `json:"transitions"`            // state_transition rows from events.jsonl
	Frames      []FrameRow              `json:"frames,omitempty"`       // frames.jsonl parsed
	Validate    json.RawMessage         `json:"validate,omitempty"`     // validate result JSON if present
}

// GroundTruthBlob is the JSON-friendly shape of ground_truth.jsonl.
type GroundTruthBlob struct {
	Meta   groundtruth.Meta    `json:"meta"`
	Labels []groundtruth.Label `json:"labels"`
}

// FrameRow mirrors one row of frames.jsonl plus the resolved relative URL.
type FrameRow struct {
	Ts     string `json:"ts"`
	Path   string `json:"path"`
	Format string `json:"format"`
}

func (s *Server) handleScenarioDetail(w http.ResponseWriter, r *http.Request) {
	// URL form: /api/scenarios/{agent}/{subtree}/{id}[/frame/{name}]
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
	scenarioDir := filepath.Join(s.RepoRoot, "replaydata", "agents", agent, subtree, id)
	if _, err := os.Stat(scenarioDir); err != nil {
		http.Error(w, "scenario not found", http.StatusNotFound)
		return
	}
	if len(parts) >= 5 && parts[3] == "frame" {
		s.serveFrame(w, scenarioDir, parts[4])
		return
	}

	d := ScenarioDetail{Agent: agent, Subtree: subtree, ID: id}
	if b, err := os.ReadFile(filepath.Join(scenarioDir, "recording-meta.json")); err == nil {
		d.Meta = b
	}
	if f, err := os.Open(filepath.Join(scenarioDir, "ground_truth.jsonl")); err == nil {
		gtMeta, labels, err := groundtruth.Read(f)
		f.Close()
		if err == nil {
			d.GroundTruth = &GroundTruthBlob{Meta: gtMeta, Labels: labels}
		}
	}
	d.Signals = readJSONLRaw(filepath.Join(scenarioDir, "signals.jsonl"))
	d.Transitions = readTransitionsRaw(filepath.Join(scenarioDir, "events.jsonl"))
	d.Frames = readFrames(filepath.Join(scenarioDir, "frames.jsonl"))
	// Synthesize meta from events.jsonl when the recording predates the
	// recorder (no recording-meta.json on disk). 27/27 committed recordings
	// fall into this bucket today — without synthesis, the Recording
	// Metadata panel is always empty.
	if d.Meta == nil {
		if synth := synthesizeMetaFromEvents(filepath.Join(scenarioDir, "events.jsonl")); synth != nil {
			d.Meta = synth
		}
	}
	if b, err := os.ReadFile(filepath.Join(scenarioDir, fmt.Sprintf("%s-%s-validate.json", agent, id))); err == nil {
		d.Validate = b
	}
	writeJSON(w, d)
}

func (s *Server) serveFrame(w http.ResponseWriter, scenarioDir, name string) {
	// Defense in depth: prevent path traversal.
	if strings.Contains(name, "..") || strings.ContainsRune(name, filepath.Separator) {
		http.Error(w, "invalid frame name", http.StatusBadRequest)
		return
	}
	p := filepath.Join(scenarioDir, "frames", name)
	b, err := os.ReadFile(p)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(b)
}

func readJSONLRaw(path string) []json.RawMessage {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []json.RawMessage
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		b := scanner.Bytes()
		if len(strings.TrimSpace(string(b))) == 0 {
			continue
		}
		cp := make([]byte, len(b))
		copy(cp, b)
		out = append(out, cp)
	}
	return out
}

// synthesizeMetaFromEvents builds a recording-meta.json-compatible
// summary by scanning events.jsonl. Used as a fallback when the actual
// recording-meta.json doesn't exist (the case for every committed
// pre-Phase-1 recording). Marked `synthesized: true` so the frontend
// can render the panel with an honest provenance label.
//
// Output shape (compact JSON):
//
//	{
//	  "synthesized": true,
//	  "adapter": "<first transcript_new's adapter>",
//	  "started_at": "<events[0].ts>",
//	  "ended_at":   "<events[last].ts>",
//	  "duration_ms": <int>,
//	  "total_events": <int>,
//	  "kinds":        {"transcript_new": 2, ...},
//	  "presession_session_ids": ["proc-XXXX"],
//	  "real_session_ids":       ["8f4d493a-..."]
//	}
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
		adapter            string
		firstTs, lastTs    string
		total              int
		kinds              = map[string]int{}
		presessionSet      = map[string]struct{}{}
		realSet            = map[string]struct{}{}
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

func readTransitionsRaw(path string) []json.RawMessage {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	dec := json.NewDecoder(bufio.NewReader(f))
	var out []json.RawMessage
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
		if kind != "state_transition" {
			continue
		}
		b, _ := json.Marshal(raw)
		out = append(out, b)
	}
}

func readFrames(path string) []FrameRow {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []FrameRow
	dec := json.NewDecoder(bufio.NewReader(f))
	for {
		var row FrameRow
		if err := dec.Decode(&row); err != nil {
			return out
		}
		out = append(out, row)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}
