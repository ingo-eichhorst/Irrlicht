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

// The archive must never fire on a session this run does not own. The guard
// that carries that is the selected-session menu: it names a title, and the
// title is proven unique among active sessions.
//
// This used to be enforced by ALSO demanding a visible composer whose project
// popup named the owned workspace. That check could not survive a turn — the
// window shows the session afterwards, not a composer — and it refused live run
// 17's cleanup, leaving the session unarchived. Its replacement is below: a
// tree that offers only another session's menu is still refused.
func TestArchiveTargetRejectsAnotherSessionsMenu(t *testing.T) {
	owned := OwnedSession{Registry: RegistrySession{SessionID: "local_owned", CWD: "/repo/workspace"}}
	sessions := []RegistrySession{{SessionID: "local_owned", CWD: "/repo/workspace", Title: "Owned title"}}
	elements := []helperElement{{
		Path: selectedSessionMenuPath, Role: "AXPopUpButton",
		Description: "More options for Someone else's session",
		Hierarchy:   []string{"AXApplication", "AXWindow", "AXGroup", "AXPopUpButton"},
	}}
	if _, err := validateArchiveTarget(owned, sessions, elements); err == nil {
		t.Fatal("validateArchiveTarget() accepted another session's menu")
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

// After a turn, Claude Desktop shows the session, not a fresh composer, so the
// project popup the archive guard demanded is gone.
//
// Live run 17 (2026-09-05) failed cleanup with: `Desktop project control
// requires one AXPopUpButton titled "cwd"; found 0. Visible AXPopUpButton
// controls: … described "More options for Echo hi" …`. The owned session was
// left unarchived in the user's Desktop — cleanup failing is a worse outcome
// than the check was ever worth.
//
// The binding that matters survives: the selected-session menu names the owned
// session's title, and that title is proven unique among active sessions. The
// freshness re-probe now targets the menu the driver is about to click, rather
// than a neighbouring control it never touches.
func TestArchiveTargetResolvesOnAPostTurnTree(t *testing.T) {
	owned := OwnedSession{Registry: RegistrySession{SessionID: "local_owned", CWD: "/repo/workspace"}}
	sessions := []RegistrySession{{SessionID: "local_owned", CWD: "/repo/workspace", Title: "Echo hi"}}
	postTurn := []helperElement{{
		Path: selectedSessionMenuPath, Role: "AXPopUpButton",
		Description: "More options for Echo hi",
		Hierarchy:   []string{"AXApplication", "AXWindow", "AXGroup", "AXPopUpButton"},
	}}
	target, err := validateArchiveTarget(owned, sessions, postTurn)
	if err != nil {
		t.Fatalf("validateArchiveTarget() on a post-turn tree: %v", err)
	}
	if target.menu.Description != "More options for Echo hi" {
		t.Fatalf("archive target does not name the owned session: %+v", target.menu)
	}
}
