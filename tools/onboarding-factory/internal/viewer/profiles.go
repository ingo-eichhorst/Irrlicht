package viewer

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"irrlicht/tools/onboarding-factory/internal/desktopresults"
	"irrlicht/tools/onboarding-factory/internal/matrix"
)

// This file is the viewer's execution-profile layer (#1889). Claude Code CLI
// Local and Claude Desktop Local both produce claudecode transcripts, so the
// viewer has to keep their evidence apart: one profile's recording history,
// status, and versions may never stand in for the other's.
//
// Two rules shape everything here:
//
//   - The default is cli-local. A viewer URL with no ?profile= keeps showing
//     exactly what it showed before this file existed, because every recording
//     in replaydata carries no execution_profile and therefore reads as the
//     legacy cli-local form (matrix.LoadRecordingManifestOrLegacy).
//   - A Desktop result may never display a CLI recording. The result artifact
//     names a recording by directory name; that name is resolved and its
//     manifest re-read here, and a recording that is not desktop-local is
//     REPORTED rather than linked. Refusing silently would be the same output
//     as "no evidence", which is the one thing the campaign cannot afford.

// defaultViewerProfile is the backward-compatible view: existing viewer URLs
// carry no ?profile=, and they keep rendering Claude Code CLI Local evidence.
const defaultViewerProfile = matrix.ProfileCLILocal

// profileFromRequest reads the ?profile= query parameter. An absent parameter
// is the CLI Local default; an unknown value is an error, never a silent
// fallback — a typo must not quietly serve the other profile's evidence.
func profileFromRequest(r *http.Request) (matrix.ExecutionProfile, error) {
	raw := r.URL.Query().Get("profile")
	if raw == "" {
		return defaultViewerProfile, nil
	}
	return matrix.ParseExecutionProfile(raw)
}

// profileLabels are the human-readable names the viewer's profile selector
// shows. Keyed by the wire value so the UI and the manifest never diverge.
var profileLabels = map[matrix.ExecutionProfile]string{
	matrix.ProfileCLILocal:     "Claude Code CLI Local",
	matrix.ProfileDesktopLocal: "Claude Desktop Local",
}

// profileOptions describes every execution profile this cell can be viewed
// under, in matrix.ExecutionProfiles() order. Selectable is what the UI uses
// to decide whether to offer a profile: the CLI Local default is always
// selectable, and Desktop Local becomes selectable as soon as the cell has
// either a desktop-local recording or an execution-results.json entry — so a
// declared-but-unrecorded Desktop result is still reachable and readable.
func profileOptions(scenarioDir string, result *DesktopResult) []ProfileOption {
	out := make([]ProfileOption, 0, len(matrix.ExecutionProfiles()))
	for _, profile := range matrix.ExecutionProfiles() {
		recordings, err := matrix.RecordingsForProfile(scenarioDir, profile)
		if err != nil {
			logViewerError("profileOptions: %s in %s: %v", profile, scenarioDir, err)
		}
		option := ProfileOption{
			ID:         string(profile),
			Label:      profileLabels[profile],
			Recordings: len(recordings),
			Selectable: profile == defaultViewerProfile || len(recordings) > 0,
		}
		if profile == matrix.ProfileDesktopLocal && result != nil {
			option.HasResult = true
			option.Selectable = true
		}
		out = append(out, option)
	}
	return out
}

// desktopResultView reads the cell's execution-results.json and returns its
// desktop-local entry. nil means the cell has no result artifact at all — the
// frontend says so explicitly rather than rendering an empty Desktop page that
// would be indistinguishable from a passing one. Every other failure comes
// back as a populated DesktopResult carrying Error, so an unreadable or
// profile-less artifact is visible in the UI instead of swallowed.
func desktopResultView(scenarioDir string) *DesktopResult {
	path := filepath.Join(scenarioDir, desktopresults.FileName)
	doc, err := desktopresults.Load(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return &DesktopResult{Error: fmt.Sprintf("cannot read %s: %v", desktopresults.FileName, err)}
	}
	for i := range doc.Results {
		if doc.Results[i].ExecutionProfile != string(matrix.ProfileDesktopLocal) {
			continue
		}
		return buildDesktopResult(scenarioDir, doc.Results[i])
	}
	return &DesktopResult{Error: fmt.Sprintf("%s carries no %s result", desktopresults.FileName, matrix.ProfileDesktopLocal)}
}

// buildDesktopResult turns one typed result into the viewer's wire shape. A
// non-observed outcome (not-applicable / unobservable / not-runnable) names no
// recording: its reason, missing control, and evidence_refs ARE the evidence,
// and they are carried through verbatim.
func buildDesktopResult(scenarioDir string, result desktopresults.Result) *DesktopResult {
	view := &DesktopResult{
		ScenarioID:     result.ScenarioID,
		Outcome:        string(result.Outcome),
		Reason:         result.Reason,
		MissingControl: result.MissingControl,
		EvidenceRefs:   result.EvidenceRefs,
	}
	if result.Recording == "" {
		return view
	}
	name, err := NewSafeArchiveName(result.Recording)
	if err != nil {
		view.Error = fmt.Sprintf("result names an unusable recording %q", result.Recording)
		return view
	}
	bindDesktopRecording(view, scenarioDir, name, result)
	return view
}

// bindDesktopRecording is the isolation guard. It re-reads the named
// recording's own manifest instead of trusting the result artifact's word, and
// binds the recording, its versions, and its raw evidence files to the view
// ONLY when that manifest says desktop-local. A CLI recording named by a
// Desktop result leaves Recording empty and Error set.
func bindDesktopRecording(view *DesktopResult, scenarioDir string, name SafeArchiveName, result desktopresults.Result) {
	recDir := filepath.Join(scenarioDir, "recordings", string(name))
	manifest, err := matrix.LoadRecordingManifest(filepath.Join(recDir, "manifest.json"))
	if err != nil {
		view.Error = fmt.Sprintf("cannot read the manifest of recording %q: %v", name, err)
		return
	}
	view.RecordingProfile = string(manifest.ExecutionProfile)
	if manifest.ExecutionProfile != matrix.ProfileDesktopLocal {
		view.Error = fmt.Sprintf(
			"recording %q is %s evidence, not %s — refusing to show it under a Desktop result",
			name, manifest.ExecutionProfile, matrix.ProfileDesktopLocal)
		return
	}
	view.Recording = string(name)
	view.Versions = &DesktopVersions{
		DesktopApp: manifest.DesktopAppVersion,
		AgentCLI:   manifest.AgentCLIVersion,
		Irrlicht:   manifest.DaemonVersion,
	}
	view.Evidence = desktopEvidenceLinks(recDir, result.Evidence)
}

// desktopEvidenceLinks resolves the six raw identity-evidence files a Desktop
// result references. Present comes from an actual stat, so a reference to a
// file that is not on disk renders as missing instead of as a dead link that
// looks like evidence.
func desktopEvidenceLinks(recDir string, evidence *desktopresults.Evidence) []DesktopEvidenceLink {
	if evidence == nil {
		return nil
	}
	named := map[string]string{
		desktopresults.DesktopRegistryFile: evidence.DesktopRegistry,
		desktopresults.TranscriptFile:      evidence.Transcript,
		desktopresults.HooksFile:           evidence.Hooks,
		desktopresults.ProcessFile:         evidence.Process,
		desktopresults.IrrlichtSessionFile: evidence.IrrlichtSession,
		desktopresults.EnvironmentFile:     evidence.Environment,
	}
	out := make([]DesktopEvidenceLink, 0, len(named))
	// RequiredRecordingFiles() fixes the order AND the allowlist: the viewer
	// only ever serves these six names, so no result-supplied string reaches
	// the filesystem as a path component.
	for _, canonical := range desktopresults.RequiredRecordingFiles() {
		link := DesktopEvidenceLink{Field: canonical, File: named[canonical]}
		if link.File == canonical {
			_, err := os.Stat(filepath.Join(recDir, canonical))
			link.Present = err == nil
		}
		out = append(out, link)
	}
	return out
}

// canonicalEvidenceFile is the closed allowlist behind the raw-evidence route.
// It returns the CONSTANT equal to name — never the caller's own string — so
// the path the handler joins carries no caller-controlled component at all,
// and ok=false for anything that is not one of the six canonical Desktop
// identity-evidence files.
func canonicalEvidenceFile(name string) (string, bool) {
	for _, allowed := range desktopresults.RequiredRecordingFiles() {
		if name == allowed {
			return allowed, true
		}
	}
	return "", false
}
