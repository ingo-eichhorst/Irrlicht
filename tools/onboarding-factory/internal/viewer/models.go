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
type ScenarioDetail struct {
	Agent          string                   `json:"agent"`
	Subtree        string                   `json:"subtree"`
	ID             string                   `json:"id"`
	Meta           json.RawMessage          `json:"meta,omitempty"`            // recording-meta.json or null
	Degraded       bool                     `json:"degraded"`                  // true when there is no events.jsonl sidecar — the timeline is synthesized from the transcript via the shared classifier engine, not daemon-recorded
	Expected       *validate.ExpectedReport `json:"expected,omitempty"`        // expected.jsonl validated against events.jsonl (if file present)
	Transitions    []json.RawMessage        `json:"transitions"`               // state_transition rows from events.jsonl
	Tools           []ToolCall               `json:"tools,omitempty"`            // tool_use blocks extracted from the newest recording's transcript.jsonl
	LatestManifest  *RecordingArchive        `json:"latest_manifest,omitempty"`  // manifest of the newest recording, mirroring archive manifest fields so the viewer renders a uniform metadata panel
	LatestRecording string                   `json:"latest_recording,omitempty"` // name (under recordings/) of the newest recording these fields describe; "" when none captured
	Assessment      *AssessmentReport        `json:"assessment,omitempty"`       // Stage 1 (Assessment) point-in-time record from assessment.json, if present
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
type RecordingArchive struct {
	Name               string `json:"name"` // dir name under recordings/
	PromotedAt         string `json:"promoted_at,omitempty"`
	DaemonVersion      string `json:"daemon_version,omitempty"`
	AgentCLIVersion    string `json:"agent_cli_version,omitempty"`
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

// ScenarioSpec is the parsed shape of one Feature: heading from the
// catalog markdown.
type ScenarioSpec struct {
	ID        string             `json:"id"`
	Section   string             `json:"section"`
	Feature   string             `json:"feature"`
	Scenarios []ScenarioSpecCase `json:"scenarios"`
}

// ScenarioSpecCase is one Scenario:/Expected: pair under a Feature
// heading. Multi-paragraph descriptions are joined with newlines.
type ScenarioSpecCase struct {
	Text     string   `json:"text"`
	Expected []string `json:"expected"`
}
