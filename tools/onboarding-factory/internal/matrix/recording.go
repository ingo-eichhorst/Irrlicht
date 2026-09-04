package matrix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// ParseExecutionProfile validates one explicit profile value. Legacy manifests
// are defaulted by LoadRecordingManifest only when the field is absent.
func ParseExecutionProfile(value string) (ExecutionProfile, error) {
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
	Dir      string
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
		ExecutionProfile  json.RawMessage `json:"execution_profile"`
		Entrypoint        string          `json:"entrypoint"`
		DaemonVersion     string          `json:"daemon_version"`
		AgentCLIVersion   string          `json:"agent_cli_version"`
		DesktopAppVersion string          `json:"desktop_app_version"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return RecordingManifest{}, fmt.Errorf("invalid JSON: %w", err)
	}
	profile := ProfileCLILocal
	if len(raw.ExecutionProfile) > 0 {
		var value string
		if err := json.Unmarshal(raw.ExecutionProfile, &value); err != nil {
			return RecordingManifest{}, fmt.Errorf("invalid execution_profile: %w", err)
		}
		profile, err = ParseExecutionProfile(value)
		if err != nil {
			return RecordingManifest{}, err
		}
	}
	return RecordingManifest{
		ExecutionProfile:  profile,
		Entrypoint:        raw.Entrypoint,
		DaemonVersion:     raw.DaemonVersion,
		AgentCLIVersion:   raw.AgentCLIVersion,
		DesktopAppVersion: raw.DesktopAppVersion,
	}, nil
}

// RecordingDirs returns recording directories newest-first. It is the single
// enumerator shared by matrix selection and verification.
func RecordingDirs(scenarioDir string) []string {
	if strings.Contains(scenarioDir, "..") {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(scenarioDir, "recordings"))
	if err != nil {
		return nil
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	dirs := make([]string, len(names))
	for i, name := range names {
		dirs[i] = filepath.Join(scenarioDir, "recordings", name)
	}
	return dirs
}

// RecordingsForProfile returns all recordings for profile, newest-first.
// Missing manifests remain legacy cli-local for selection compatibility.
func RecordingsForProfile(scenarioDir string, profile ExecutionProfile) ([]Recording, error) {
	profile, err := ParseExecutionProfile(string(profile))
	if err != nil {
		return nil, err
	}
	var recordings []Recording
	for _, dir := range RecordingDirs(scenarioDir) {
		manifestPath := filepath.Join(dir, "manifest.json")
		manifest, err := LoadRecordingManifest(manifestPath)
		if err != nil {
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("%s: %w", manifestPath, err)
			}
			manifest.ExecutionProfile = ProfileCLILocal
		}
		if manifest.ExecutionProfile == profile {
			recordings = append(recordings, Recording{
				Name: filepath.Base(dir), Dir: dir, Manifest: manifest,
			})
		}
	}
	return recordings, nil
}

// NewestRecording selects the newest recording within profile. The profile
// filter is applied before the lexicographic newest-recording rule. A missing
// manifest is treated as legacy cli-local for matrix compatibility; `of
// validate` reports the missing manifest as an incomplete recording.
func NewestRecording(scenarioDir string, profile ExecutionProfile) (Recording, bool, error) {
	recordings, err := RecordingsForProfile(scenarioDir, profile)
	if err != nil {
		return Recording{}, false, err
	}
	if len(recordings) == 0 {
		return Recording{}, false, nil
	}
	return recordings[0], true, nil
}
