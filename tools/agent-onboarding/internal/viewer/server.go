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
	"sort"
	"strings"

	"irrlicht/tools/agent-onboarding/internal/groundtruth"
)

//go:embed web/*
var webFS embed.FS

// Server is the viewer HTTP server.
type Server struct {
	RepoRoot string // path containing replaydata/
}

// Handler returns the http.Handler the CLI wires into ListenAndServe.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/scenarios/", s.handleScenarioDetail) // path with trailing parts
	mux.HandleFunc("/api/scenarios", s.handleScenariosList)
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
