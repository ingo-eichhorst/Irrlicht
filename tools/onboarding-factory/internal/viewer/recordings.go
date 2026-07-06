package viewer

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"sort"

	"irrlicht/tools/onboarding-factory/internal/validate"
)

// handleRecordingsList walks the scenario's recordings/ subdir and returns
// a sorted (newest-first) list of archived recordings with their manifest
// contents. Empty array when the dir is absent or has no entries.
func (s *Server) handleRecordingsList(w http.ResponseWriter, scenarioDir string) {
	names := s.store().listArchiveDirs(scenarioDir)
	out := make([]RecordingArchive, 0, len(names))
	for _, name := range names {
		archive := RecordingArchive{Name: name}
		if b, ok := s.store().readFile(filepath.Join(scenarioDir, "recordings", name, "manifest.json")); ok {
			if err := json.Unmarshal(b, &archive); err != nil {
				logViewerError("handleRecordingsList: malformed manifest.json in archive %q: %v", name, err)
			}
			archive.Name = name // defensive: manifest may not echo name
		}
		out = append(out, archive)
	}
	// Newest-first by NAME. Recording names are timestamp-prefixed, so
	// lexicographic descending == chronological newest-first — and it matches
	// validate.NewestRecordingDir (name-max), so list[0] is the same recording
	// the detail view embeds as the newest. "Ordered by name" is the contract.
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	writeJSON(w, out)
}

// handleArchivedRecording returns the events / transcript / ground truth
// for one archived recording. Mirrors the main scenario-detail shape but
// pulls from recordings/<name>/ and re-validates against the CURRENT
// top-level expected.jsonl (the drift signal).
func (s *Server) handleArchivedRecording(w http.ResponseWriter, scenarioDir, rawName string) {
	// Defense in depth — the URL slug regex constrained agent + id, not the
	// archive name. NewSafeArchiveName disallows path traversal, and every
	// path built below is only reachable through the SafeArchiveName it
	// returns, so a future call site can't bypass the check.
	name, err := NewSafeArchiveName(rawName)
	if err != nil {
		http.Error(w, "invalid archive name", http.StatusBadRequest)
		return
	}
	store := s.store()
	archiveDir := store.archiveFilePath(scenarioDir, name, "")
	if !store.exists(archiveDir) {
		http.Error(w, "archive not found", http.StatusNotFound)
		return
	}
	d := ArchivedRecordingDetail{Name: string(name)}
	if b, ok := store.readFile(store.archiveFilePath(scenarioDir, name, "manifest.json")); ok {
		if err := json.Unmarshal(b, &d.Manifest); err != nil {
			logViewerError("handleArchivedRecording: malformed manifest.json in archive %q: %v", name, err)
		}
		d.Manifest.Name = string(name)
	}
	d.Transitions = readTransitionsRaw(store.archiveFilePath(scenarioDir, name, "events.jsonl"))
	// Re-evaluate the archive against the CURRENT top-level expected.jsonl.
	// Drift signal: archive may have passed at promote-time but fail today
	// because the spec moved.
	if rep, err := validate.ValidateExpectedAgainst(
		filepath.Join(scenarioDir, "expected.jsonl"),
		store.archiveFilePath(scenarioDir, name, "events.jsonl"),
	); err == nil && rep != nil {
		d.Expected = rep
	}
	d.Tools = extractToolCalls(store.archiveFilePath(scenarioDir, name, "transcript.jsonl"))
	writeJSON(w, d)
}
