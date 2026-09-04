package viewer

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"sort"

	"irrlicht/tools/onboarding-factory/internal/matrix"
	"irrlicht/tools/onboarding-factory/internal/validate"
)

// manifestFileName is the per-recording provenance file every archive carries.
const manifestFileName = "manifest.json"

// archiveRequest names one archived recording within one execution profile.
type archiveRequest struct {
	scenarioDir string
	rawName     string
	profile     matrix.ExecutionProfile
}

// evidenceRequest names one raw Desktop identity-evidence file inside one
// archived recording, within one execution profile.
type evidenceRequest struct {
	scenarioDir string
	rawName     string
	file        string
	profile     matrix.ExecutionProfile
}

// handleRecordingsList walks the scenario's recordings/ subdir and returns
// a sorted (newest-first) list of the archived recordings BELONGING TO
// profile, with their manifest contents. Empty array when the dir is absent,
// has no entries, or has none in this profile — the two profiles keep
// separate histories, so a Desktop view never lists a CLI recording (#1889).
func (s *Server) handleRecordingsList(w http.ResponseWriter, scenarioDir string, profile matrix.ExecutionProfile) {
	recordings, err := matrix.RecordingsForProfile(scenarioDir, profile)
	if err != nil {
		http.Error(w, "cannot read recording manifests: "+err.Error(), http.StatusInternalServerError)
		return
	}
	out := make([]RecordingArchive, 0, len(recordings))
	for _, rec := range recordings {
		out = append(out, s.archiveRow(scenarioDir, rec))
	}
	// Newest-first by NAME. Recording names are timestamp-prefixed, so
	// lexicographic descending == chronological newest-first — and it matches
	// the profile-scoped newest-recording selection, so list[0] is the same
	// recording the detail view embeds as the newest. "Ordered by name" is the
	// contract.
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	writeJSON(w, out)
}

// archiveRow builds one recordings-list row. The manifest is re-read from disk
// for the promote-time fields the RecordingManifest struct does not carry
// (promoted_at, recipe_hash, expected_pass_rate, recording_started_at); the
// execution profile is taken from the already-parsed manifest, so a legacy
// manifest with no execution_profile is stamped as cli-local rather than
// rendering profile-less.
func (s *Server) archiveRow(scenarioDir string, rec matrix.Recording) RecordingArchive {
	archive := RecordingArchive{Name: rec.Name}
	if b, ok := s.store().readFile(filepath.Join(scenarioDir, "recordings", rec.Name, manifestFileName)); ok {
		if err := json.Unmarshal(b, &archive); err != nil {
			logViewerError("archiveRow: malformed manifest.json in archive %q: %v", rec.Name, err)
		}
		archive.Name = rec.Name // defensive: manifest may not echo name
	}
	archive.ExecutionProfile = string(rec.Manifest.ExecutionProfile)
	archive.DesktopAppVersion = rec.Manifest.DesktopAppVersion
	return archive
}

// handleArchivedRecording returns the events / transcript / ground truth
// for one archived recording. Mirrors the main scenario-detail shape but
// pulls from recordings/<name>/ and re-validates against the CURRENT
// top-level expected.jsonl (the drift signal).
//
// The archive must belong to the request's execution profile. Asking a
// desktop-local view for a cli-local recording is a 404, not a rendering: the
// endpoint is the last place a CLI recording could sneak into a Desktop page.
func (s *Server) handleArchivedRecording(w http.ResponseWriter, req archiveRequest) {
	// Defense in depth — the URL slug regex constrained agent + id, not the
	// archive name. NewSafeArchiveName disallows path traversal, and every
	// path built below is only reachable through the SafeArchiveName it
	// returns, so a future call site can't bypass the check.
	name, ok := s.resolveProfileArchive(w, req)
	if !ok {
		return
	}
	store := s.store()
	d := ArchivedRecordingDetail{Name: string(name)}
	if b, ok := store.readFile(store.archiveFilePath(req.scenarioDir, name, manifestFileName)); ok {
		if err := json.Unmarshal(b, &d.Manifest); err != nil {
			logViewerError("handleArchivedRecording: malformed manifest.json in archive %q: %v", name, err)
		}
		d.Manifest.Name = string(name)
	}
	d.Manifest.ExecutionProfile = string(req.profile)
	d.Transitions = readTransitionsRaw(store.archiveFilePath(req.scenarioDir, name, "events.jsonl"))
	// Re-evaluate the archive against the CURRENT top-level expected.jsonl.
	// Drift signal: archive may have passed at promote-time but fail today
	// because the spec moved.
	if rep, err := validate.ValidateExpectedAgainst(
		filepath.Join(req.scenarioDir, "expected.jsonl"),
		store.archiveFilePath(req.scenarioDir, name, "events.jsonl"),
	); err == nil && rep != nil {
		d.Expected = rep
	}
	d.Tools = extractToolCalls(store.archiveFilePath(req.scenarioDir, name, "transcript.jsonl"))
	writeJSON(w, d)
}

// resolveProfileArchive validates the archive name, confirms the archive
// exists, and confirms its manifest claims the requested execution profile. It
// writes the error response itself and reports ok=false when any of the three
// fails, so no caller can reach an archive outside its profile.
func (s *Server) resolveProfileArchive(w http.ResponseWriter, req archiveRequest) (SafeArchiveName, bool) {
	name, err := NewSafeArchiveName(req.rawName)
	if err != nil {
		http.Error(w, "invalid archive name", http.StatusBadRequest)
		return "", false
	}
	store := s.store()
	if !store.exists(store.archiveFilePath(req.scenarioDir, name, "")) {
		http.Error(w, "archive not found", http.StatusNotFound)
		return "", false
	}
	manifest, err := matrix.LoadRecordingManifestOrLegacy(store.archiveFilePath(req.scenarioDir, name, manifestFileName))
	if err != nil {
		http.Error(w, "cannot read recording manifest: "+err.Error(), http.StatusInternalServerError)
		return "", false
	}
	if manifest.ExecutionProfile != req.profile {
		// Not "not found by accident": the archive exists but belongs to the
		// other profile. Say which, so the 404 can't be read as "no evidence".
		http.Error(w,
			"archive "+string(name)+" is "+string(manifest.ExecutionProfile)+
				" evidence; this view is scoped to "+string(req.profile),
			http.StatusNotFound)
		return "", false
	}
	return name, true
}

// handleDesktopEvidence serves one raw Desktop identity-evidence file from a
// desktop-local recording, so an observed Desktop result links to the bytes it
// rests on rather than merely naming them.
//
// The file name is matched against desktopresults' closed six-file allowlist
// BEFORE it is ever joined onto a path, and the recording must itself pass the
// profile check — so no caller-supplied path component reaches the filesystem,
// and CLI bytes can never be served through a Desktop evidence link.
func (s *Server) handleDesktopEvidence(w http.ResponseWriter, req evidenceRequest) {
	if req.profile != matrix.ProfileDesktopLocal {
		http.Error(w, "raw identity evidence exists only for "+string(matrix.ProfileDesktopLocal), http.StatusNotFound)
		return
	}
	canonical, allowed := canonicalEvidenceFile(req.file)
	if !allowed {
		http.Error(w, "not a Desktop identity-evidence file", http.StatusBadRequest)
		return
	}
	name, ok := s.resolveProfileArchive(w, archiveRequest{
		scenarioDir: req.scenarioDir, rawName: req.rawName, profile: req.profile,
	})
	if !ok {
		return
	}
	// `canonical` is one of the six package constants, never the raw URL
	// segment, so this join carries no caller-controlled component.
	body, found := s.store().readFile(s.store().archiveFilePath(req.scenarioDir, name, canonical))
	if !found {
		http.Error(w, "evidence file not present in this recording", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if _, err := w.Write(body); err != nil {
		logViewerError("handleDesktopEvidence: write %s: %v", req.file, err)
	}
}
