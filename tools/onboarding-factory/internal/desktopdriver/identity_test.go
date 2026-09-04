package desktopdriver

import (
	"strings"
	"testing"
)

func validOwnedSession() (RegistrySession, TranscriptIdentity) {
	registry := RegistrySession{
		SessionID:    "local_new",
		CLISessionID: "cli-new",
		CWD:          "/repo/workspace",
	}
	transcript := TranscriptIdentity{
		SessionID:  "cli-new",
		CWD:        "/repo/workspace",
		Entrypoint: "claude-desktop",
	}
	return registry, transcript
}

func TestSelectOwnedSessionRejectsNestedWorkspaceOpenedAsNoFolder(t *testing.T) {
	requested := "/repo/.build/refresh/claudecode/basic/cwd"
	scratch := "/Users/test/Library/Application Support/Claude/scratch-workspaces/account/profile/scratch-2026-09-04"
	registry := RegistrySession{
		SessionID:    "local_new",
		CLISessionID: "cli-new",
		CWD:          scratch,
	}
	transcript := TranscriptIdentity{
		SessionID:  "cli-new",
		CWD:        scratch,
		Entrypoint: "claude-desktop",
	}

	_, err := SelectOwnedSession(
		map[string]struct{}{},
		[]RegistrySession{registry},
		map[string]TranscriptIdentity{"cli-new": transcript},
		requested,
	)
	if err == nil {
		t.Fatal("expected an exact workspace mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "workspace mismatch") ||
		!strings.Contains(err.Error(), requested) ||
		!strings.Contains(err.Error(), scratch) {
		t.Fatalf("error = %q; want workspace mismatch with requested and observed paths", err)
	}
}

func TestSelectOwnedSessionJoinsExactDesktopIdentity(t *testing.T) {
	registry, transcript := validOwnedSession()
	got, err := SelectOwnedSession(
		map[string]struct{}{"local_old": {}},
		[]RegistrySession{{SessionID: "local_old"}, registry},
		map[string]TranscriptIdentity{"cli-new": transcript},
		"/repo/workspace",
	)
	if err != nil {
		t.Fatalf("SelectOwnedSession() error = %v", err)
	}
	if got.Registry.SessionID != "local_new" || got.Transcript.SessionID != "cli-new" {
		t.Fatalf("SelectOwnedSession() = %+v", got)
	}
}

func TestSelectOwnedSessionRejectsPreBaselineSelectionMutation(t *testing.T) {
	registry, transcript := validOwnedSession()
	_, err := SelectOwnedSession(
		map[string]struct{}{"local_old": {}},
		[]RegistrySession{
			{SessionID: "local_old", CLISessionID: "cli-old", CWD: "/repo/workspace"},
			registry,
		},
		map[string]TranscriptIdentity{
			"cli-old": {SessionID: "cli-old", CWD: "/repo/workspace", Entrypoint: "claude-desktop"},
			"cli-new": transcript,
		},
		"/repo/workspace",
	)
	if err != nil {
		t.Fatalf("baseline filter did not select the owned session: %v", err)
	}
}

func TestSelectOwnedSessionRejectsConcurrentNewSession(t *testing.T) {
	registry, transcript := validOwnedSession()
	second := RegistrySession{SessionID: "local_other", CLISessionID: "cli-other", CWD: "/repo/workspace"}
	_, err := SelectOwnedSession(
		map[string]struct{}{},
		[]RegistrySession{registry, second},
		map[string]TranscriptIdentity{"cli-new": transcript},
		"/repo/workspace",
	)
	if err == nil || !strings.Contains(err.Error(), "provisional ownership is ambiguous") {
		t.Fatalf("error = %v; want concurrent session refusal", err)
	}
}

func TestSelectProvisionalSessionCanOwnBeforeTranscriptAppears(t *testing.T) {
	registry, _ := validOwnedSession()
	registry.CLISessionID = ""
	got, err := SelectProvisionalSession(nil, []RegistrySession{registry}, "/repo/workspace")
	if err != nil {
		t.Fatalf("SelectProvisionalSession() error = %v", err)
	}
	if got.Registry.SessionID != "local_new" || got.Transcript.SessionID != "" {
		t.Fatalf("SelectProvisionalSession() = %+v", got)
	}
}

func TestSelectProvisionalSessionAcceptsOmittedLocalScope(t *testing.T) {
	registry, _ := validOwnedSession()
	registry.EnvScopeID = nil
	registry.EnvScopePresent = false
	if _, err := SelectProvisionalSession(nil, []RegistrySession{registry}, "/repo/workspace"); err != nil {
		t.Fatalf("omitted envScopeId must identify a Local project: %v", err)
	}
}

func TestSelectOwnedSessionRejectsUnverifiedIdentityFields(t *testing.T) {
	localScope := "remote"
	tests := []struct {
		name       string
		mutate     func(*RegistrySession, *TranscriptIdentity)
		wantDetail string
	}{
		{"non-local id", func(r *RegistrySession, _ *TranscriptIdentity) { r.SessionID = "remote_new" }, "not a local session"},
		{"missing cli id", func(r *RegistrySession, _ *TranscriptIdentity) { r.CLISessionID = "" }, "no Claude Code transcript ID"},
		{"non-local environment", func(r *RegistrySession, _ *TranscriptIdentity) { r.EnvScopeID = &localScope }, "did not use the Local environment"},
		{"transcript id", func(_ *RegistrySession, tr *TranscriptIdentity) { tr.SessionID = "wrong" }, "transcript identity mismatch"},
		{"transcript cwd", func(_ *RegistrySession, tr *TranscriptIdentity) { tr.CWD = "/other" }, "transcript workspace mismatch"},
		{"entrypoint", func(_ *RegistrySession, tr *TranscriptIdentity) { tr.Entrypoint = "sdk-cli" }, "entrypoint"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry, transcript := validOwnedSession()
			test.mutate(&registry, &transcript)
			transcripts := map[string]TranscriptIdentity{registry.CLISessionID: transcript}
			_, err := SelectOwnedSession(nil, []RegistrySession{registry}, transcripts, "/repo/workspace")
			if err == nil || !strings.Contains(err.Error(), test.wantDetail) {
				t.Fatalf("error = %v; want %q", err, test.wantDetail)
			}
		})
	}
}
