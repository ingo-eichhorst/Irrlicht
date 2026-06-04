// activation_service.go owns the consent state for irrlicht-managed
// instruction-file blocks (issue #558, "global activation"). Unlike the
// hook/statusline installers — which run unconditionally at startup — the
// task-eta emission rule is written to ~/.claude/CLAUDE.md only after the
// user opts in once; consent persists across restarts in the daemon's data
// dir (honors IRRLICHT_HOME isolation).
package services

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"irrlicht/core/ports/outbound"
)

// activationFilename is the basename of the persisted consent record,
// written beside relay-identity.json under the daemon's data dir.
const activationFilename = "activation.json"

// ActivationState is the persisted consent record.
type ActivationState struct {
	TaskEtaEnabled bool `json:"task_eta_enabled"`
}

// ActivationInstaller carries the instruction-file mutators as function
// values so this package doesn't import the claudecode adapter. Both return
// (modified, err) — modified=false means the file already matched.
type ActivationInstaller struct {
	Install   func() (bool, error)
	Uninstall func() (bool, error)
}

// ActivationService persists consent and orchestrates the installer.
type ActivationService struct {
	dir       string
	installer ActivationInstaller
	log       outbound.Logger
	mu        sync.Mutex
}

// NewActivationService creates the service. dir is the daemon data dir
// (dataDir(home) — IRRLICHT_HOME-aware).
func NewActivationService(dir string, installer ActivationInstaller, log outbound.Logger) *ActivationService {
	return &ActivationService{dir: dir, installer: installer, log: log}
}

// Status returns the persisted consent state (zero value when never set).
func (s *ActivationService) Status() ActivationState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load()
}

// Enable installs the managed block and then persists consent — in that
// order, so a failed install never leaves a "consented but no block" record.
func (s *ActivationService) Enable() (ActivationState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.installer.Install(); err != nil {
		s.log.LogError("activation", "", "task-eta install failed: "+err.Error())
		return s.load(), err
	}
	state := ActivationState{TaskEtaEnabled: true}
	if err := s.save(state); err != nil {
		s.log.LogError("activation", "", "task-eta consent persist failed: "+err.Error())
		return state, err
	}
	return state, nil
}

// Disable removes the managed block and persists the revocation. The consent
// flag is cleared even if the block removal fails (the user said no — honor
// it; a stray block is re-removable later).
func (s *ActivationService) Disable() (ActivationState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := ActivationState{TaskEtaEnabled: false}
	if err := s.save(state); err != nil {
		s.log.LogError("activation", "", "task-eta consent persist failed: "+err.Error())
		return state, err
	}
	if _, err := s.installer.Uninstall(); err != nil {
		s.log.LogError("activation", "", "task-eta uninstall failed: "+err.Error())
		return state, err
	}
	return state, nil
}

// ReassertOnStartup re-installs the managed block iff consented — idempotent
// (a byte-identical block is a no-op; a stale block is upgraded). When not
// consented it does nothing: uninstall is only ever an explicit user action,
// never a silent startup side effect.
func (s *ActivationService) ReassertOnStartup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.load().TaskEtaEnabled {
		return
	}
	modified, err := s.installer.Install()
	if err != nil {
		s.log.LogError("startup", "", "task-eta block re-assert failed: "+err.Error())
		return
	}
	if modified {
		s.log.LogInfo("startup", "", "re-asserted task-eta managed block in ~/.claude/CLAUDE.md")
	}
}

// --- persistence (mirrors relay/identity.go: 0o700 dir, 0o600 file) ---

func (s *ActivationService) path() string {
	return filepath.Join(s.dir, activationFilename)
}

func (s *ActivationService) load() ActivationState {
	var state ActivationState
	data, err := os.ReadFile(s.path())
	if err != nil {
		return state
	}
	_ = json.Unmarshal(data, &state) // unreadable record reads as not-consented
	return state
}

func (s *ActivationService) save(state ActivationState) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path(), data, 0o600)
}
