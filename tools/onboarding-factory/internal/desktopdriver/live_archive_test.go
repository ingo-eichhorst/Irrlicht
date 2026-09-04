package desktopdriver

// Archiving the owned session, and the guards that keep the driver away from
// one it did not create.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func archiveFixtureElements(project, title string) []helperElement {
	elements := []helperElement{
		fixtureElement("environment", "AXPopUpButton", "Local", ""),
		fixtureElement("project", "AXPopUpButton", project, ""),
		fixtureElement("prompt", "AXTextArea", "", "Prompt"),
		fixtureElement("send", "AXButton", "", "Send"),
		fixtureElement("mode", "AXPopUpButton", "Auto", ""),
		fixtureElement("model", "AXPopUpButton", "", "Model: Opus 5"),
	}
	return append(elements, helperElement{
		Path: selectedSessionMenuPath, Role: "AXPopUpButton",
		Description: "More options for " + title,
		Hierarchy:   []string{"AXApplication", "AXWindow", "AXGroup", "AXPopUpButton"},
	})
}

func TestArchiveTargetRejectsDuplicateActiveTitle(t *testing.T) {
	owned := OwnedSession{Registry: RegistrySession{SessionID: "local_owned", CWD: "/repo/workspace"}}
	sessions := []RegistrySession{
		{SessionID: "local_owned", CWD: "/repo/workspace", Title: "Same title"},
		{SessionID: "local_user", CWD: "/repo/other", Title: "Same title"},
	}
	elements := archiveFixtureElements("workspace", "Same title")
	if _, err := validateArchiveTarget(owned, sessions, elements); err == nil {
		t.Fatal("validateArchiveTarget() accepted a duplicate active title")
	}
}

func TestArchiveTargetRejectsSelectedProjectDrift(t *testing.T) {
	owned := OwnedSession{Registry: RegistrySession{SessionID: "local_owned", CWD: "/repo/workspace"}}
	sessions := []RegistrySession{{SessionID: "local_owned", CWD: "/repo/workspace", Title: "Owned title"}}
	elements := archiveFixtureElements("other-project", "Owned title")
	if _, err := validateArchiveTarget(owned, sessions, elements); err == nil {
		t.Fatal("validateArchiveTarget() accepted selected-project drift")
	}
}

func TestArchiveOwnedInvokesDuplicateTitleGuard(t *testing.T) {
	root := t.TempDir()
	workspace := "/repo/workspace"
	registryRoot := filepath.Join(root, "claude-code-sessions", "account", "profile")
	if err := os.MkdirAll(registryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, session := range []RegistrySession{
		{SessionID: "local_owned", CLISessionID: "cli-owned", CWD: workspace, Title: "Same title"},
		{SessionID: "local_user", CLISessionID: "cli-user", CWD: "/repo/other", Title: "Same title"},
	} {
		data, err := json.Marshal(session)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(registryRoot, session.SessionID+".json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	response, err := json.Marshal(helperResponse{OK: true, Elements: archiveFixtureElements("workspace", "Same title")})
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "helper")
	script := "#!/bin/sh\ncat >/dev/null\nprintf '%s\\n' '" + string(response) + "'\n"
	if err := os.WriteFile(helper, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewLiveRuntime(LiveOptions{
		Home: root, HelperPath: helper, DaemonAddress: "127.0.0.1:1",
		RecordingDirectory: filepath.Join(root, "recordings"), DesktopSupportRoot: root,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	owned := OwnedSession{Registry: RegistrySession{
		SessionID: "local_owned", CLISessionID: "cli-owned", CWD: workspace,
	}}
	err = runtime.ArchiveOwned(context.Background(), owned)
	if err == nil || !strings.Contains(err.Error(), "active session title") || !strings.Contains(err.Error(), "not unique") {
		t.Fatalf("ArchiveOwned() duplicate-title error = %v", err)
	}
}
