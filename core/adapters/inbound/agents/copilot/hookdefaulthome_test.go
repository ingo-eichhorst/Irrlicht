// hookdefaulthome_test.go covers the configuration every ordinary user is in:
// COPILOT_HOME unset.
//
// Every other hook test in this package relocates COPILOT_HOME to an absolute
// t.TempDir(), which is the one spelling that hides this defect. With the
// variable unset, sessionsDir() returns the $HOME-RELATIVE literal
// ".copilot/session-state", and PathConfiner.Confine refuses a non-absolute
// path outright (RejectRelativePath) before it ever looks at the roots — so a
// reconstructed transcript path is thrown away and the Notification branch
// dispatches nothing at all.
package copilot

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// TestNotification_DispatchesWithDefaultHome is the regression test for that
// defect: the permission-prompt acceleration must work for a user who has
// never heard of COPILOT_HOME.
func TestNotification_DispatchesWithDefaultHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(copilotHomeEnvVar, "") // the default configuration

	root := filepath.Join(home, ".copilot", "session-state")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	want := writeSessionTranscript(t, root, "sess-default")

	target := &mockTarget{}
	confiner := TranscriptConfiner()
	h := NewHookHandlerWithConfiner(target,
		keyedGate{PermissionKeyHooks: true, PermissionKeyTranscripts: true}, mockLogger{}, confiner)

	rec := post(t, h, notificationBody("sess-default", "permission_prompt"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	perms := target.perms()
	if len(perms) != 1 {
		t.Fatalf("got %d permission dispatches with COPILOT_HOME unset, want 1 "+
			"(confiner rejections: %v) — the reconstructed path must be absolute, or the "+
			"permission-prompt half of the channel is dead for every default install",
			len(perms), confiner.Rejections())
	}
	if perms[0].transcriptPath != want {
		t.Errorf("transcriptPath = %q, want %q", perms[0].transcriptPath, want)
	}
	if n := confiner.RejectionCount(); n != 0 {
		t.Errorf("confiner counted %d rejection(s) for a legitimate prompt (%v) — this "+
			"pollutes the counter whose purpose is to signal local probing",
			n, confiner.Rejections())
	}
}

// TestStop_DispatchesWithDefaultHome pins that the turn-end half, which
// receives an absolute transcript_path from Copilot, is unaffected — so the
// fix above is not masking a broader breakage.
func TestStop_DispatchesWithDefaultHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(copilotHomeEnvVar, "")

	root := filepath.Join(home, ".copilot", "session-state")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tp := writeSessionTranscript(t, root, "sess-default")

	target := &mockTarget{}
	h := NewHookHandlerWithConfiner(target,
		keyedGate{PermissionKeyHooks: true, PermissionKeyTranscripts: true}, mockLogger{}, TranscriptConfiner())

	post(t, h, contractPayload(tp, HookStop))
	if n := len(target.stops()); n != 1 {
		t.Fatalf("got %d Stop dispatches with COPILOT_HOME unset, want 1", n)
	}
}
