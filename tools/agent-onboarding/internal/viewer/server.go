// Package viewer serves the replay viewer: a localhost web UI for
// inspecting recordings (events.jsonl + transcript.jsonl + expected.jsonl
// validation + archive history).
//
// API:
//
//	GET  /                                                  — embedded SPA
//	GET  /api/scenarios                                     — list of recordings
//	GET  /api/scenarios/{agent}/{subtree}/{id}              — recording detail
//	GET  /api/scenarios/{agent}/{subtree}/{id}/recordings   — archived recordings
//	GET  /api/scenarios/{agent}/{subtree}/{id}/recordings/{name} — one archive
//	GET  /api/catalog                                       — coverage matrix
//	GET  /api/recipes                                       — recipe catalog
//	GET  /api/scenario-spec/{id}                            — parsed spec
//
// `subtree` is "scenarios" or "regression"; recordings live at
// replaydata/agents/<agent>/<subtree>/<id>/.
//
// The handlers are split across cohesive files in this package:
//   - server.go      — HTTP plumbing (this file): routing, static UI, JSON
//   - store.go       — RecordingStore: the replaydata filesystem repository
//   - models.go      — the response DTOs
//   - scenarios.go   — /api/scenarios list + detail and their helpers
//   - recordings.go  — archived-recording browsing
//   - catalog.go     — /api/catalog coverage matrix + annotation passes
//   - recipe.go      — /api/recipes recipe catalog + coverage_id dedup
//   - spec.go        — /api/scenario-spec markdown parsing
package viewer

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"regexp"
)

// slugRE constrains user-supplied URL components (agent, scenario id) so
// they can never traverse out of replaydata/agents/.
var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

//go:embed web/*
var webFS embed.FS

// Server is the viewer HTTP server.
type Server struct {
	RepoRoot string // path containing replaydata/

	// playback manages the single active replay session. Lazily created on
	// Handler() so callers can construct a Server with just RepoRoot.
	playback *PlaybackManager
}

// store returns the filesystem repository for this server's replaydata
// tree — the single seam handlers use instead of inline os.ReadFile.
func (s *Server) store() RecordingStore {
	return RecordingStore{RepoRoot: s.RepoRoot}
}

// PlaybackManager returns the server's playback manager, initialising it if
// necessary. Used by main.go to seed an auto-playback at boot.
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
	mux.HandleFunc("/api/recipes", s.handleRecipes)
	mux.HandleFunc("/api/scenario-spec/", s.handleScenarioSpec)
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

// writeJSON encodes v as the JSON body of an HTTP response. A late encode
// error (after headers are sent) can't be recovered into a clean status, so
// it's logged rather than silently dropped.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logViewerError("writeJSON: encode response: %v", err)
	}
}

// logViewerError reports a non-fatal viewer error to stderr. Handlers use
// it instead of `_ = json.Encode(...)` so a truncated or malformed response
// leaves a trace rather than failing silently.
func logViewerError(format string, args ...any) {
	log.Printf("[viewer] "+format, args...)
}
