package services_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/permission"
)

const (
	migrationAgent = "claude-code"
	migrationPerm  = "instructions"
)

// errPermStore wraps mockPermStore with injectable Load/Save failures.
type errPermStore struct {
	mockPermStore
	loadErr error
	saveErr error
}

func (s *errPermStore) Load() (permission.Set, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.mockPermStore.Load()
}

func (s *errPermStore) Save(set permission.Set) error {
	if s.saveErr != nil {
		return s.saveErr
	}
	return s.mockPermStore.Save(set)
}

func writeLegacyActivation(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "activation.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func fileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

func TestMigrateLegacyTaskEtaConsent_NoFileIsNoop(t *testing.T) {
	store := &mockPermStore{}
	services.MigrateLegacyTaskEtaConsent(t.TempDir(), store, migrationAgent, migrationPerm, &mockLogger{})
	if store.saveCount() != 0 {
		t.Errorf("saves = %d, want 0", store.saveCount())
	}
}

func TestMigrateLegacyTaskEtaConsent_EnabledSeedsGranted(t *testing.T) {
	dir := t.TempDir()
	path := writeLegacyActivation(t, dir, `{"task_eta_enabled": true}`)
	store := &mockPermStore{}
	services.MigrateLegacyTaskEtaConsent(dir, store, migrationAgent, migrationPerm, &mockLogger{})
	if got := store.set.Get(migrationAgent, migrationPerm); got != permission.StateGranted {
		t.Errorf("state = %s, want granted", got)
	}
	if fileExists(t, path) {
		t.Error("legacy activation.json should be retired after migration")
	}
}

func TestMigrateLegacyTaskEtaConsent_DisabledSeedsDenied(t *testing.T) {
	dir := t.TempDir()
	path := writeLegacyActivation(t, dir, `{"task_eta_enabled": false}`)
	store := &mockPermStore{}
	services.MigrateLegacyTaskEtaConsent(dir, store, migrationAgent, migrationPerm, &mockLogger{})
	if got := store.set.Get(migrationAgent, migrationPerm); got != permission.StateDenied {
		t.Errorf("state = %s, want denied", got)
	}
	if fileExists(t, path) {
		t.Error("legacy activation.json should be retired after migration")
	}
}

func TestMigrateLegacyTaskEtaConsent_WizardAnswerWins(t *testing.T) {
	dir := t.TempDir()
	path := writeLegacyActivation(t, dir, `{"task_eta_enabled": true}`)
	store := &mockPermStore{set: permission.Set{
		migrationAgent: {migrationPerm: permission.StateDenied},
	}}
	services.MigrateLegacyTaskEtaConsent(dir, store, migrationAgent, migrationPerm, &mockLogger{})
	if got := store.set.Get(migrationAgent, migrationPerm); got != permission.StateDenied {
		t.Errorf("state = %s, want denied (wizard answer must win over legacy file)", got)
	}
	if store.saveCount() != 0 {
		t.Errorf("saves = %d, want 0 (already-answered permission must not be rewritten)", store.saveCount())
	}
	if fileExists(t, path) {
		t.Error("legacy activation.json should be retired even when the wizard answer wins")
	}
}

func TestMigrateLegacyTaskEtaConsent_UnparseableDiscarded(t *testing.T) {
	dir := t.TempDir()
	path := writeLegacyActivation(t, dir, `{not json`)
	store := &mockPermStore{}
	services.MigrateLegacyTaskEtaConsent(dir, store, migrationAgent, migrationPerm, &mockLogger{})
	if store.saveCount() != 0 {
		t.Errorf("saves = %d, want 0 (garbage must not seed an answer)", store.saveCount())
	}
	if fileExists(t, path) {
		t.Error("unparseable legacy file should be discarded")
	}
}

func TestMigrateLegacyTaskEtaConsent_LoadErrorKeepsFile(t *testing.T) {
	dir := t.TempDir()
	path := writeLegacyActivation(t, dir, `{"task_eta_enabled": true}`)
	store := &errPermStore{loadErr: errors.New("disk gone")}
	services.MigrateLegacyTaskEtaConsent(dir, store, migrationAgent, migrationPerm, &mockLogger{})
	if !fileExists(t, path) {
		t.Error("legacy file must survive a store load failure so a later start can retry")
	}
	if store.saveCount() != 0 {
		t.Errorf("saves = %d, want 0", store.saveCount())
	}
}

func TestMigrateLegacyTaskEtaConsent_SaveErrorKeepsFile(t *testing.T) {
	dir := t.TempDir()
	path := writeLegacyActivation(t, dir, `{"task_eta_enabled": true}`)
	store := &errPermStore{saveErr: errors.New("disk full")}
	services.MigrateLegacyTaskEtaConsent(dir, store, migrationAgent, migrationPerm, &mockLogger{})
	if !fileExists(t, path) {
		t.Error("legacy file must survive a store save failure so a later start can retry")
	}
}
