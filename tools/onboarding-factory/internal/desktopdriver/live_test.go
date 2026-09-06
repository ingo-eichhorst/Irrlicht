package desktopdriver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// The composer leaves the accessibility tree when Claude Desktop is not
// frontmost. waitForComposerControls fronts Desktop before every observation
// for that reason, and the steps that follow it must do the same: focus can
// move at any moment, and a run of 25 live turns lost one to exactly that.
//
// Live run 25 failed with `Desktop send control requires one AXButton described
// "Send"; found 0. Visible AXButton controls: unlabelled, unlabelled,
// unlabelled, described "Hide sidebar", described "Back" …` — a sidebar with no
// composer in it at all, after five retries that each looked again without ever
// bringing the window back.
func TestTypingAndSubmittingBringDesktopToTheFront(t *testing.T) {
	for _, step := range []string{"set prompt", "submit"} {
		fronted := 0
		runtime := &LiveRuntime{
			controls:     map[string]helperSelector{"prompt": {Role: "AXTextArea", Description: "Prompt"}},
			frontDesktop: func(context.Context) error { fronted++; return nil },
			helper:       helperClient{path: filepath.Join(t.TempDir(), "absent-helper")},
		}
		switch step {
		case "set prompt":
			_ = runtime.SetPrompt(context.Background(), "hello")
		case "submit":
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			_ = runtime.Submit(ctx)
			cancel()
		}
		if fronted == 0 {
			t.Fatalf("%s never brought Claude Desktop to the front", step)
		}
	}
}

// Submit's postcondition watches for the Stop button, which only exists while
// a turn is in flight. A turn that finishes fast may never render it, and the
// helper then reports `postcondition_failed: The required exists postcondition`
// for a prompt that was sent perfectly well. Runs 46, 48 and 50 of 2026-09-06
// failed that way.
//
// The authoritative evidence that a submit landed is the Desktop registry row,
// which the very next step already waits for. So a failed postcondition here
// must not fail the run: it is an optimisation, not the proof.
//
// Every OTHER helper failure still surfaces. A click the helper refused
// outright is not a click.
func TestSubmitTreatsAMissedStopButtonAsSent(t *testing.T) {
	if !isMissedPostcondition(errors.New(
		"helper postcondition_failed: The required exists postcondition was not met")) {
		t.Fatal("a missed postcondition was not recognised")
	}
	for _, other := range []string{
		"helper stale_control: The current click point does not hit the selected control.",
		"helper permission_denied: accessibility is not trusted",
		"helper control_ambiguous: The selector matched 2 visible controls.",
	} {
		if isMissedPostcondition(errors.New(other)) {
			t.Fatalf("%q was wrongly treated as a landed click", other)
		}
	}
}
