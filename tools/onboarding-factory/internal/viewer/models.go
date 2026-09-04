package viewer

import (
	"encoding/json"

	"irrlicht/tools/onboarding-factory/internal/matrix"
	"irrlicht/tools/onboarding-factory/internal/validate"
)

// This file is the viewer's model layer: the response DTOs the HTTP
// handlers (in catalog.go / recipe.go / spec.go / scenarios.go /
// recordings.go) marshal. Keeping them here — rather than scattered next
// to whichever handler first needed them — gives the wire contract one
// place to read.

// ScenarioListEntry is one row in /api/scenarios.
type ScenarioListEntry struct {
	Agent   string `json:"agent"`
	Subtree string `json:"subtree"` // "scenarios" | "regressions"
	ID      string `json:"id"`
}

// ScenarioDetail is the payload for /api/scenarios/{agent}/{subtree}/{id}.
//
// Every recording-derived field below describes ONE execution profile —
// the one named by ExecutionProfile, selected by the request's ?profile=
// (cli-local by default). CLI and Desktop evidence is never merged into a
// single status or recording history: ask for the other profile and you get
// the other profile's recording, manifest, expectations and versions, or an
// explicit "nothing recorded" (#1889).
type ScenarioDetail struct {
	Agent            string                   `json:"agent"`
	Subtree          string                   `json:"subtree"`
	ID               string                   `json:"id"`
	ExecutionProfile string                   `json:"execution_profile"`          // the profile this payload describes
	Profiles         []ProfileOption          `json:"profiles,omitempty"`         // every profile this cell can be viewed under
	DesktopResult    *DesktopResult           `json:"desktop_result,omitempty"`   // execution-results.json's desktop-local entry, when the cell has one
	Meta             json.RawMessage          `json:"meta,omitempty"`             // recording-meta.json or null
	Degraded         bool                     `json:"degraded"`                   // true when there is no events.jsonl sidecar — the timeline is synthesized from the transcript via the shared classifier engine, not daemon-recorded
	Expected         *validate.ExpectedReport `json:"expected,omitempty"`         // expected.jsonl validated against events.jsonl (if file present)
	Transitions      []json.RawMessage        `json:"transitions"`                // state_transition rows from events.jsonl
	Tools            []ToolCall               `json:"tools,omitempty"`            // tool_use blocks extracted from the newest recording's transcript.jsonl
	LatestManifest   *RecordingArchive        `json:"latest_manifest,omitempty"`  // manifest of the newest recording, mirroring archive manifest fields so the viewer renders a uniform metadata panel
	LatestRecording  string                   `json:"latest_recording,omitempty"` // name (under recordings/) of the newest recording these fields describe; "" when none captured
	Assessment       *AssessmentReport        `json:"assessment,omitempty"`       // Stage 1 (Assessment) point-in-time record from assessment.json, if present
}

// ProfileOption is one selectable execution profile for a cell, as offered by
// the viewer's profile selector. Recordings counts only that profile's
// recordings, so the UI can say "0 recordings" for a profile that exists on
// paper but has never been captured.
type ProfileOption struct {
	ID         string `json:"id"`         // matrix.ExecutionProfile wire value
	Label      string `json:"label"`      // human-readable name
	Selectable bool   `json:"selectable"` // the UI offers this profile for this cell
	Recordings int    `json:"recordings"` // recordings captured under this profile
	HasResult  bool   `json:"has_result"` // execution-results.json carries an entry for it
}

// DesktopResult is the viewer's read of the desktop-local entry in a cell's
// execution-results.json (the typed contract in internal/desktopresults).
//
// Error is the loud channel. A Desktop result whose recording cannot be
// resolved — or that names a recording belonging to the OTHER profile — comes
// back with Recording empty and Error set, so "we refuse to show this" and
// "there is nothing to show" never render as the same thing.
type DesktopResult struct {
	ScenarioID       string                `json:"scenario_id"`
	Outcome          string                `json:"outcome"`
	Reason           string                `json:"reason,omitempty"`
	MissingControl   string                `json:"missing_control,omitempty"`
	Recording        string                `json:"recording,omitempty"`         // set ONLY when the named recording is desktop-local
	RecordingProfile string                `json:"recording_profile,omitempty"` // the profile the named recording actually claims
	Versions         *DesktopVersions      `json:"versions,omitempty"`
	Evidence         []DesktopEvidenceLink `json:"evidence,omitempty"`
	EvidenceRefs     []string              `json:"evidence_refs,omitempty"` // repo-relative evidence for a non-observed outcome
	Error            string                `json:"error,omitempty"`
}

// DesktopVersions are the three versions a Desktop Local observation pins,
// read from the linked recording's own manifest.json rather than copied into
// the result artifact.
type DesktopVersions struct {
	DesktopApp string `json:"desktop_app,omitempty"` // manifest.desktop_app_version
	AgentCLI   string `json:"agent_cli,omitempty"`   // manifest.agent_cli_version — the bundled Claude Code
	Irrlicht   string `json:"irrlicht,omitempty"`    // manifest.daemon_version
}

// DesktopEvidenceLink names one raw identity-evidence file inside the linked
// recording. Field is the canonical file the contract requires; File is what
// the result actually referenced; Present is an on-disk stat, so a reference
// to a missing file reads as missing instead of as evidence.
type DesktopEvidenceLink struct {
	Field   string `json:"field"`
	File    string `json:"file"`
	Present bool   `json:"present"`
}

// AssessmentReport / AssessmentSource are the persisted artifact of one
// Stage-1 assessment (per cell-lifecycle.md): one file per (agent, scenario)
// at replaydata/agents/<agent>/scenarios/<scenario>/assessment.json. The
// canonical definitions live in internal/matrix (the single matrix model,
// #508) so the gates, the matrix CLI, and the viewer share one disk/wire
// contract; these aliases keep the viewer's existing references working.
// DisplayState rolls the three axes + measured recording up — see
// matrix.DeriveDisplayState (mirrored by deriveDisplayState in catalog.go).
type (
	AssessmentReport = matrix.AssessmentReport
	AssessmentSource = matrix.AssessmentSource
)

// ToolCall is one Anthropic-style tool_use block lifted from the
// transcript. Today this is the only signal irrlicht has for
// "agent invoked a tool" — the daemon's events.jsonl carries
// transcript_activity / parent_linked / hook_received but NOT a
// first-class tool_use Kind. Promoting tool_use to a lifecycle Kind
// is future work (issue TBD); until then the viewer derives it
// client-side from the transcript content.
type ToolCall struct {
	Ts        string `json:"ts"`                   // RFC3339 (from the message line's timestamp)
	SessionID string `json:"session_id,omitempty"` // sessionId on the message line
	Name      string `json:"name"`                 // tool name (e.g. "Bash", "Agent", "Read")
	ID        string `json:"id,omitempty"`         // tool_use id (toolu_…)
}

// RecordingArchive is one row of the recordings-list response —
// names a historical recording's directory plus its manifest fields.
// ExecutionProfile and DesktopAppVersion come straight from manifest.json;
// a manifest without execution_profile is the legacy cli-local form, and the
// recordings list stamps that in so a row never renders profile-less.
type RecordingArchive struct {
	Name               string `json:"name"` // dir name under recordings/
	PromotedAt         string `json:"promoted_at,omitempty"`
	DaemonVersion      string `json:"daemon_version,omitempty"`
	AgentCLIVersion    string `json:"agent_cli_version,omitempty"`
	DesktopAppVersion  string `json:"desktop_app_version,omitempty"`
	ExecutionProfile   string `json:"execution_profile,omitempty"`
	RecipeHash         string `json:"recipe_hash,omitempty"`
	ExpectedPassRate   string `json:"expected_pass_rate,omitempty"`
	RecordingStartedAt string `json:"recording_started_at,omitempty"`
}

// ArchivedRecordingDetail is the payload for fetching one archived
// recording — events + transcript + the manifest + a fresh
// validation against the CURRENT top-level expected.jsonl. The
// re-validation is the drift signal: an archive that passed at
// promote-time (per manifest.expected_pass_rate) but fails the
// fresh evaluation means either the spec changed or the daemon
// drifted between then and now.
type ArchivedRecordingDetail struct {
	Name        string                   `json:"name"`
	Manifest    RecordingArchive         `json:"manifest"`
	Transitions []json.RawMessage        `json:"transitions"`
	Expected    *validate.ExpectedReport `json:"expected,omitempty"` // current spec vs this archive's events
	Tools       []ToolCall               `json:"tools,omitempty"`    // tool_use blocks extracted from archive's transcript.jsonl
}

// ScenarioSpec is the agent-AGNOSTIC spec for one scenario, served at
// /api/scenario-spec/<name> straight from the catalog shard. Process and
// AcceptanceCriteria are markdown.
type ScenarioSpec struct {
	ID                 string `json:"id"`   // "<section>.<index>" code
	Name               string `json:"name"` // kebab slug
	Description        string `json:"description"`
	Process            string `json:"process"`
	AcceptanceCriteria string `json:"acceptance_criteria"`
}
