package viewer

import (
	"encoding/json"

	"irrlicht/tools/agent-onboarding/internal/validate"
)

// This file is the viewer's model layer: the response DTOs the HTTP
// handlers (in catalog.go / recipe.go / spec.go / scenarios.go /
// recordings.go) marshal. Keeping them here — rather than scattered next
// to whichever handler first needed them — gives the wire contract one
// place to read.

// ScenarioListEntry is one row in /api/scenarios.
type ScenarioListEntry struct {
	Agent   string `json:"agent"`
	Subtree string `json:"subtree"` // "scenarios" | "regression"
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
	Tools          []ToolCall               `json:"tools,omitempty"`           // tool_use blocks extracted from transcript.jsonl
	LatestManifest *RecordingArchive        `json:"latest_manifest,omitempty"` // synthesized manifest for the live top-level recording, mirroring archive manifest fields so the viewer can render a uniform metadata panel
	Assessment     *AssessmentReport        `json:"assessment,omitempty"`      // Stage 1 (Assessment) point-in-time record from assessment.json, if present
}

// AssessmentReport is the persisted artifact of one Stage-1 assessment
// (per cell-lifecycle.md). One file per (agent, scenario) at
// replaydata/agents/<agent>/scenarios/<scenario>/assessment.json,
// overwritten on re-assessment — git is the history. The matrix in
// .claude/skills/ir:onboard-agent/agent-scenarios-coverage.json is the current-state rollup;
// this struct preserves when and why the verdict was reached.
type AssessmentReport struct {
	SchemaVersion    int                `json:"schema_version"`
	ScenarioID       string             `json:"scenario_id"`
	Agent            string             `json:"agent"`
	AssessedAt       string             `json:"assessed_at"`
	AgentSupports    string             `json:"agent_supports"`    // yes / partial / no / unknown
	IrrlichtObserves string             `json:"irrlicht_observes"` // yes / partial / no / unknown / n/a
	Confidence       float64            `json:"confidence,omitempty"`
	Body             string             `json:"body"`
	Sources          []AssessmentSource `json:"sources,omitempty"`
	// Caveats documents known limitations / metric drifts that don't
	// invalidate the verdict but a maintainer should know about. E.g.
	// "feature is invisible to file-watching, but spec compliance is
	// unaffected" or "context utilization % overstates after a rewind".
	// One string per caveat, plain prose. Rendered as a bulleted
	// list in the viewer's Assessment panel.
	Caveats []string `json:"caveats,omitempty"`
}

// AssessmentSource is one citation backing an assessment verdict.
type AssessmentSource struct {
	Kind string `json:"kind"` // "url" | "file" | other
	Ref  string `json:"ref"`
	Note string `json:"note,omitempty"`
}

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
