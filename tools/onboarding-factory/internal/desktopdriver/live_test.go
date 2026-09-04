package desktopdriver

import (
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
