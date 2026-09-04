package matrix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"irrlicht/tools/onboarding-factory/internal/validate"
)

// ExecutionProfile identifies the product entry surface that produced a
// recording. The profile is independent of the adapter: Claude Code CLI and
// Claude Desktop both produce claudecode transcripts, but their evidence must
// not replace each other.
type ExecutionProfile string

const (
	ProfileCLILocal     ExecutionProfile = "cli-local"
	ProfileDesktopLocal ExecutionProfile = "desktop-local"
)

// ExecutionProfiles returns every accepted manifest value in stable order.
func ExecutionProfiles() []ExecutionProfile {
	return []ExecutionProfile{ProfileCLILocal, ProfileDesktopLocal}
}

// ParseExecutionProfile validates one profile value. An empty value is the
// legacy manifest form and means cli-local.
func ParseExecutionProfile(value string) (ExecutionProfile, error) {
	if value == "" {
		return ProfileCLILocal, nil
	}
	p := ExecutionProfile(value)
	for _, allowed := range ExecutionProfiles() {
		if p == allowed {
			return p, nil
		}
	}
	return "", fmt.Errorf("unknown execution profile %q (allowed: %q, %q)",
		value, ProfileCLILocal, ProfileDesktopLocal)
}

// RecordingManifest is the identity and provenance stored in manifest.json.
// AgentCLIVersion is the Claude Code version for claudecode recordings.
// DesktopAppVersion is populated for desktop-local recordings.
type RecordingManifest struct {
	ExecutionProfile  ExecutionProfile `json:"execution_profile,omitempty"`
	Entrypoint        string           `json:"entrypoint,omitempty"`
	DaemonVersion     string           `json:"daemon_version,omitempty"`
	AgentCLIVersion   string           `json:"agent_cli_version,omitempty"`
	DesktopAppVersion string           `json:"desktop_app_version,omitempty"`
}

// Recording identifies the selected recording and its manifest.
type Recording struct {
	Name     string
	Manifest RecordingManifest
}

// LoadRecordingManifest reads and validates one recording manifest. Missing
// execution_profile is accepted as the legacy cli-local form.
func LoadRecordingManifest(path string) (RecordingManifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return RecordingManifest{}, err
	}
	var raw struct {
		ExecutionProfile  string `json:"execution_profile"`
		Entrypoint        string `json:"entrypoint"`
		DaemonVersion     string `json:"daemon_version"`
		AgentCLIVersion   string `json:"agent_cli_version"`
		DesktopAppVersion string `json:"desktop_app_version"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return RecordingManifest{}, fmt.Errorf("invalid JSON: %w", err)
	}
	profile, err := ParseExecutionProfile(raw.ExecutionProfile)
	if err != nil {
		return RecordingManifest{}, err
	}
	return RecordingManifest{
		ExecutionProfile:  profile,
		Entrypoint:        raw.Entrypoint,
		DaemonVersion:     raw.DaemonVersion,
		AgentCLIVersion:   raw.AgentCLIVersion,
		DesktopAppVersion: raw.DesktopAppVersion,
	}, nil
}

// NewestRecording selects the newest recording within profile. The profile
// filter is applied before the lexicographic newest-recording rule. A missing
// manifest is treated as legacy cli-local for matrix compatibility; `of
// validate` reports the missing manifest as an incomplete recording.
func NewestRecording(scenarioDir string, profile ExecutionProfile) (Recording, bool, error) {
	profile, err := ParseExecutionProfile(string(profile))
	if err != nil {
		return Recording{}, false, err
	}
	for _, dir := range validate.RecordingDirs(scenarioDir) {
		manifest, err := LoadRecordingManifest(filepath.Join(dir, "manifest.json"))
		if err != nil {
			if os.IsNotExist(err) {
				manifest.ExecutionProfile = ProfileCLILocal
			} else {
				return Recording{}, false, fmt.Errorf("%s: %w", filepath.Join(dir, "manifest.json"), err)
			}
		}
		if manifest.ExecutionProfile == profile {
			return Recording{Name: filepath.Base(dir), Manifest: manifest}, true, nil
		}
	}
	return Recording{}, false, nil
}
