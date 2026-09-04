// Package desktopresults validates the explicit per-cell evidence contract for
// Claude Code Desktop Local. It does not change the profile-neutral matrix: the
// viewer and status commands can consume this contract in later issues.
package desktopresults

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"irrlicht/tools/onboarding-factory/internal/matrix"
)

const (
	// FileName is the result artifact stored beside metadata.json in each cell.
	FileName = "execution-results.json"
	// SchemaVersion is the only accepted result artifact version.
	SchemaVersion = 1

	DesktopRegistryFile = matrix.DesktopRegistryFile
	EnvironmentFile     = matrix.DesktopEnvironmentFile
	HooksFile           = matrix.DesktopHooksFile
	ProcessFile         = matrix.DesktopProcessFile
	IrrlichtSessionFile = matrix.DesktopIrrlichtSessionFile
	TranscriptFile      = matrix.DesktopTranscriptFile
)

// RequiredRecordingFiles returns the raw evidence files every Desktop Local
// recording must preserve, in stable order.
func RequiredRecordingFiles() []string {
	return matrix.DesktopEvidenceFiles()
}

type Outcome string

const (
	OutcomeObservedPassing Outcome = "observed-passing"
	OutcomeObservedFailure Outcome = "observed-failure"
	OutcomeNotApplicable   Outcome = "not-applicable"
	OutcomeUnobservable    Outcome = "unobservable"
	OutcomeNotRunnable     Outcome = "not-runnable"
)

// Evidence names raw files within one exact recording directory. These are
// references, not copied identity claims: validation reads every named file.
type Evidence struct {
	DesktopRegistry string `json:"desktop_registry"`
	Transcript      string `json:"transcript"`
	Hooks           string `json:"hooks"`
	Process         string `json:"process"`
	IrrlichtSession string `json:"irrlicht_session"`
	Environment     string `json:"environment"`
}

// Result is one execution-profile answer for the cell named by ScenarioID.
type Result struct {
	ScenarioID       string    `json:"scenario_id"`
	ExecutionProfile string    `json:"execution_profile"`
	Outcome          Outcome   `json:"outcome"`
	Recording        string    `json:"recording,omitempty"`
	Reason           string    `json:"reason,omitempty"`
	MissingControl   string    `json:"missing_control,omitempty"`
	Evidence         *Evidence `json:"evidence,omitempty"`
	EvidenceRefs     []string  `json:"evidence_refs,omitempty"`
}

// Document is one versioned per-cell execution-results.json artifact.
type Document struct {
	SchemaVersion int      `json:"schema_version"`
	Results       []Result `json:"results"`
}

// Load reads one result artifact with its closed schema.
func Load(path string) (Document, error) {
	var doc Document
	if err := decodeStrict(path, &doc); err != nil {
		return Document{}, err
	}
	return doc, nil
}

func decodeStrict(path string, value any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	decoder := json.NewDecoder(f)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return fmt.Errorf("trailing data: %w", err)
	}
	return nil
}

func knownOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomeObservedPassing, OutcomeObservedFailure, OutcomeNotApplicable,
		OutcomeUnobservable, OutcomeNotRunnable:
		return true
	default:
		return false
	}
}

func observedOutcome(outcome Outcome) bool {
	return outcome == OutcomeObservedPassing || outcome == OutcomeObservedFailure
}
