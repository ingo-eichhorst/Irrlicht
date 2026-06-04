package services

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// nopActivationLogger satisfies outbound.Logger for these internal tests
// (the shared mockLogger lives in the external services_test package).
type nopActivationLogger struct{}

func (nopActivationLogger) LogInfo(_, _, _ string)                                  {}
func (nopActivationLogger) LogError(_, _, _ string)                                 {}
func (nopActivationLogger) LogProcessingTime(_, _ string, _ int64, _ int, _ string) {}
func (nopActivationLogger) Close() error                                            { return nil }

// fakeInstaller records calls and returns canned results.
type fakeInstaller struct {
	installCalls   int
	uninstallCalls int
	installErr     error
	uninstallErr   error
}

func (f *fakeInstaller) installer() ActivationInstaller {
	return ActivationInstaller{
		Install: func() (bool, error) {
			f.installCalls++
			return true, f.installErr
		},
		Uninstall: func() (bool, error) {
			f.uninstallCalls++
			return true, f.uninstallErr
		},
	}
}

func newTestActivationService(t *testing.T, f *fakeInstaller) *ActivationService {
	t.Helper()
	return NewActivationService(t.TempDir(), f.installer(), nopActivationLogger{})
}

func TestActivationService_StatusDefaultsFalse(t *testing.T) {
	s := newTestActivationService(t, &fakeInstaller{})
	if s.Status().TaskEtaEnabled {
		t.Error("fresh service should not be consented")
	}
}

func TestActivationService_EnablePersistsAndInstalls(t *testing.T) {
	f := &fakeInstaller{}
	s := newTestActivationService(t, f)
	state, err := s.Enable()
	if err != nil {
		t.Fatal(err)
	}
	if !state.TaskEtaEnabled || !s.Status().TaskEtaEnabled {
		t.Error("enable should persist consent")
	}
	if f.installCalls != 1 {
		t.Errorf("installCalls = %d, want 1", f.installCalls)
	}
	// Consent survives a "restart" (fresh service over the same dir).
	s2 := NewActivationService(s.dir, f.installer(), nopActivationLogger{})
	if !s2.Status().TaskEtaEnabled {
		t.Error("consent should persist across restarts")
	}
}

func TestActivationService_DisablePersistsAndUninstalls(t *testing.T) {
	f := &fakeInstaller{}
	s := newTestActivationService(t, f)
	if _, err := s.Enable(); err != nil {
		t.Fatal(err)
	}
	state, err := s.Disable()
	if err != nil {
		t.Fatal(err)
	}
	if state.TaskEtaEnabled || s.Status().TaskEtaEnabled {
		t.Error("disable should clear consent")
	}
	if f.uninstallCalls != 1 {
		t.Errorf("uninstallCalls = %d, want 1", f.uninstallCalls)
	}
}

func TestActivationService_EnableInstallErrorDoesNotPersistConsent(t *testing.T) {
	f := &fakeInstaller{installErr: errors.New("disk full")}
	s := newTestActivationService(t, f)
	if _, err := s.Enable(); err == nil {
		t.Fatal("expected install error to propagate")
	}
	if s.Status().TaskEtaEnabled {
		t.Error("failed install must not leave a consented-but-no-block record")
	}
	if _, err := os.Stat(filepath.Join(s.dir, activationFilename)); !os.IsNotExist(err) {
		t.Error("no consent file should be written on install failure")
	}
}

func TestActivationService_ReassertOnStartup(t *testing.T) {
	f := &fakeInstaller{}
	s := newTestActivationService(t, f)

	// Not consented → no install.
	s.ReassertOnStartup()
	if f.installCalls != 0 {
		t.Errorf("not consented: installCalls = %d, want 0", f.installCalls)
	}

	// Consented → idempotent re-install on startup.
	if _, err := s.Enable(); err != nil {
		t.Fatal(err)
	}
	s.ReassertOnStartup()
	if f.installCalls != 2 { // 1 from Enable + 1 from re-assert
		t.Errorf("consented: installCalls = %d, want 2", f.installCalls)
	}
	// Never uninstalls at startup.
	if f.uninstallCalls != 0 {
		t.Errorf("startup must never uninstall, got %d calls", f.uninstallCalls)
	}
}
