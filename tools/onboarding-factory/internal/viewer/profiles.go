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
// An unreadable manifest is REPORTED on the option rather than counted as
// zero recordings: matrix.RecordingsForProfile fails on the first bad manifest
// under recordings/ and returns nothing, so "we could not read this cell" and
// "this cell has nothing" would otherwise be the same answer.
func profileOptions(scenarioDir string, result *DesktopResult) []ProfileOption {
	out := make([]ProfileOption, 0, len(matrix.ExecutionProfiles()))
	for _, profile := range matrix.ExecutionProfiles() {
		recordings, err := matrix.RecordingsForProfile(scenarioDir, profile)
		option := ProfileOption{
			ID:         string(profile),
			Label:      profileLabels[profile],
			Recordings: len(recordings),
			Selectable: profile == defaultViewerProfile || len(recordings) > 0,
		}
		if err != nil {
			logViewerError("profileOptions: %s in %s: %v", profile, scenarioDir, err)
			option.Error = err.Error()
			// A profile whose history cannot be read stays selectable, so the
			// user can open it and see WHY rather than finding it hidden.
			option.Selectable = true
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
	doc, failure := loadDesktopDocument(scenarioDir)
	if doc == nil {
		return failure // nil,nil = no artifact; nil,failure = unreadable one
	}
	result, ok := desktopLocalResult(*doc)
	if !ok {
		return &DesktopResult{Error: fmt.Sprintf("%s carries no %s result", desktopresults.FileName, matrix.ProfileDesktopLocal)}
	}
	return buildDesktopResult(scenarioDir, result)
}

// loadDesktopDocument reads the cell's result artifact. (nil, nil) means the
// cell simply has none; (nil, failure) means it has one that cannot be read,
// which is a finding rather than an absence.
func loadDesktopDocument(scenarioDir string) (*desktopresults.Document, *DesktopResult) {
	doc, err := desktopresults.Load(filepath.Join(scenarioDir, desktopresults.FileName))
	if err == nil {
		return &doc, nil
	}
	if os.IsNotExist(err) {
		return nil, nil
	}
	return nil, &DesktopResult{Error: fmt.Sprintf("cannot read %s: %v", desktopresults.FileName, err)}
}

// desktopLocalResult picks the artifact's desktop-local entry. The contract
// permits at most one (of validate rejects duplicates), so the first wins.
func desktopLocalResult(doc desktopresults.Document) (desktopresults.Result, bool) {
	for i := range doc.Results {
		if doc.Results[i].ExecutionProfile == string(matrix.ProfileDesktopLocal) {
			return doc.Results[i], true
		}
	}
	return desktopresults.Result{}, false
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
	bindDesktopRecording(view, desktopRecordingRef{
		scenarioDir: scenarioDir, name: name, evidence: result.Evidence,
	})
	return view
}

// desktopRecordingRef is the recording a Desktop result points at, plus the
// evidence files it claims live inside it.
type desktopRecordingRef struct {
	scenarioDir string
	name        SafeArchiveName
	evidence    *desktopresults.Evidence
}

// bindDesktopRecording is the isolation guard. It re-reads the named
// recording's own manifest instead of trusting the result artifact's word, and
// binds the recording, its versions, and its raw evidence files to the view
// ONLY when that manifest says desktop-local. A CLI recording named by a
// Desktop result leaves Recording empty and Error set.
func bindDesktopRecording(view *DesktopResult, ref desktopRecordingRef) {
	recDir := filepath.Join(ref.scenarioDir, "recordings", string(ref.name))
	manifest, err := matrix.LoadRecordingManifest(filepath.Join(recDir, "manifest.json"))
	if err != nil {
		view.Error = fmt.Sprintf("cannot read the manifest of recording %q: %v", ref.name, err)
		return
	}
	view.RecordingProfile = string(manifest.ExecutionProfile)
	if manifest.ExecutionProfile != matrix.ProfileDesktopLocal {
		view.Error = fmt.Sprintf(
			"recording %q is %s evidence, not %s — refusing to show it under a Desktop result",
			ref.name, manifest.ExecutionProfile, matrix.ProfileDesktopLocal)
		return
	}
	view.Recording = string(ref.name)
	view.Versions = &DesktopVersions{
		DesktopApp: manifest.DesktopAppVersion,
		AgentCLI:   manifest.AgentCLIVersion,
		Irrlicht:   manifest.DaemonVersion,
	}
	view.Evidence = desktopEvidenceLinks(recDir, ref.evidence)
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
	// only ever SERVES these six names, so no result-supplied string reaches
	// the filesystem as a path component of the raw-evidence route.
	for _, canonical := range desktopresults.RequiredRecordingFiles() {
		out = append(out, evidenceLink(recDir, canonical, named[canonical]))
	}
	return out
}

// evidenceLink describes one reference. Present is a real stat of the file the
// result ACTUALLY referenced (not of the canonical name), and Canonical says
// whether that reference is the one the contract requires. Keeping them apart
// matters: a present file referenced under a non-canonical name is a contract
// violation, not a missing file, and must not read as one.
func evidenceLink(recDir, canonical, referenced string) DesktopEvidenceLink {
	link := DesktopEvidenceLink{Field: canonical, File: referenced, Canonical: referenced == canonical}
	safe, err := NewSafeArchiveName(referenced)
	if err != nil {
		return link // an unusable reference names nothing on disk
	}
	_, statErr := os.Stat(filepath.Join(recDir, string(safe)))
	link.Present = statErr == nil
	return link
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
