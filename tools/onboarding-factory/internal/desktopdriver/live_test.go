package desktopdriver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVersionGatePinsTheVerifiedDesktopAndBundledCodePair(t *testing.T) {
	status := helperStatus{
		BundleIdentifier:         desktopBundleID,
		DesktopVersion:           supportedDesktopVersion,
		BundledClaudeCodeVersion: supportedClaudeCodeVersion,
	}
	if _, err := validateVersions(status, "0.7.0+test"); err != nil {
		t.Fatalf("validateVersions() error = %v", err)
	}
	status.BundledClaudeCodeVersion = "2.1.261"
	if _, err := validateVersions(status, "0.7.0+test"); err == nil || !strings.Contains(err.Error(), "not the verified version") {
		t.Fatalf("unverified bundled version error = %v", err)
	}
}

func TestReadTranscriptIdentityRejectsInconsistentJSONL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.jsonl")
	content := strings.Join([]string{
		`{"sessionId":"cli-1","cwd":"/exact","entrypoint":"claude-desktop"}`,
		`{"sessionId":"cli-1","cwd":"/other","entrypoint":"claude-desktop"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readTranscriptIdentity(path); err == nil || !strings.Contains(err.Error(), "inconsistent cwd") {
		t.Fatalf("readTranscriptIdentity() error = %v", err)
	}
}

func TestIrrlichtSessionSelectionRejectsDuplicateIdentity(t *testing.T) {
	sessions := []SessionObservation{{SessionID: "cli-1"}, {SessionID: "cli-1"}}
	if _, _, err := selectIrrlichtSession(sessions, "cli-1"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("selectIrrlichtSession() error = %v", err)
	}
}

func TestIrrlichtDecoderRejectsMalformedSessionFields(t *testing.T) {
	var sessions []SessionObservation
	value := map[string]any{"session_id": "cli-1", "pid": "not-a-number"}
	if err := collectSessionObjects(value, &sessions); err == nil || !strings.Contains(err.Error(), "decode Irrlicht session") {
		t.Fatalf("collectSessionObjects() error = %v", err)
	}
}

// Claude Desktop's renderer swaps views under the driver. Live run 20 drove a
// complete turn and lost it twice to that: `submit Desktop prompt: helper
// action_failed: Accessibility could not read AXRole (AX error -25202)`, and
// then at cleanup `archive owned session: helper stale_control: The current
// click point does not hit the selected control`.
//
// Every failure retried here is provably BEFORE the helper posts a mouse event,
// so re-running the action cannot double-click anything. A retry of a failure
// that might have landed would be a different and much worse bug.
func TestTransientAccessibilityFailuresAreRetriedNotSurfaced(t *testing.T) {
	for _, message := range []string{
		"helper stale_control: The current click point does not hit the selected control.",
		"helper action_failed: Accessibility could not read AXRole (AX error -25202).",
		"helper control_missing: no control matched",
	} {
		attempts := 0
		err := retryTransientAX(context.Background(), "drive a control", func() error {
			attempts++
			if attempts < 3 {
				return errors.New(message)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("%q was not retried: %v", message, err)
		}
		if attempts != 3 {
			t.Fatalf("%q: attempts = %d, want 3", message, attempts)
		}
	}

	// A failure that is not a known pre-click refusal must surface at once.
	attempts := 0
	err := retryTransientAX(context.Background(), "drive a control", func() error {
		attempts++
		return errors.New("helper permission_denied: accessibility is not trusted")
	})
	if err == nil || attempts != 1 {
		t.Fatalf("a non-transient failure was retried %d times: %v", attempts, err)
	}

	// A tree that never settles must fail loudly, naming the last reason.
	attempts = 0
	err = retryTransientAX(context.Background(), "drive a control", func() error {
		attempts++
		return errors.New("helper stale_control: still moving")
	})
	if err == nil || !strings.Contains(err.Error(), "kept moving") ||
		!strings.Contains(err.Error(), "stale_control") {
		t.Fatalf("an unsettled tree did not fail loudly: %v", err)
	}
	if attempts < 2 {
		t.Fatalf("an unsettled tree was tried only %d times", attempts)
	}
}
