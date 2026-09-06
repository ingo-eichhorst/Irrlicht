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
	"time"
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
		Path: []int{9, 9}, Role: "AXPopUpButton",
		Description: "More options for " + title,
		Hierarchy:   openConversationHierarchy(),
	})
}

// This used to assert that a duplicate active title is refused. It is not any
// more, and deliberately so — see TestArchiveTargetResolvesWhenOtherSessionsShareTheTitle
// for why that rule made a repeatable scenario unable to clean up after itself.
//
// What replaces it is the check below, which is what the title rule was only
// ever standing in for: the conversation on screen must be the owned one.
func TestArchiveTargetRequiresTheOwnedConversationToBeOpen(t *testing.T) {
	owned := OwnedSession{Registry: RegistrySession{SessionID: "local_owned", CWD: "/repo/workspace"}}
	sessions := []RegistrySession{
		{SessionID: "local_owned", CWD: "/repo/workspace", Title: "Same title"},
		{SessionID: "local_user", CWD: "/repo/other", Title: "Same title"},
	}
	// The owned session's own conversation is open: allowed, despite the duplicate.
	if _, err := validateArchiveTarget(owned, sessions, []helperElement{
		sessionMenuElement("Same title", 21),
		sessionMenuElement("Same title", 29),
	}); err != nil {
		t.Fatalf("validateArchiveTarget() refused the open owned conversation: %v", err)
	}
	// Somebody else's conversation is open: refused.
	if _, err := validateArchiveTarget(owned, sessions, []helperElement{
		sessionMenuElement("Same title", 21),
		sessionMenuElement("A different session", 29),
	}); err == nil {
		t.Fatal("validateArchiveTarget() accepted a foreign open conversation")
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
		Path: []int{9, 9}, Role: "AXPopUpButton",
		Description: "More options for Someone else's session",
		Hierarchy:   openConversationHierarchy(),
	}}
	if _, err := validateArchiveTarget(owned, sessions, elements); err == nil {
		t.Fatal("validateArchiveTarget() accepted another session's menu")
	}
}

// ArchiveOwned must surface the open-conversation guard, not merely have it
// available: the guard is what stops a cleanup from archiving a session this
// run does not own.
func TestArchiveOwnedRefusesAForeignOpenConversation(t *testing.T) {
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
	response, err := json.Marshal(helperResponse{OK: true, Elements: []helperElement{
		sessionMenuElement("Same title", 21),
		sessionMenuElement("A different session", 29),
	}})
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
	// ArchiveOwned brings Desktop forward before it clicks. Stub that seam: its
	// default runs `open -a Claude`, and no unit test may touch the real app.
	runtime.frontDesktop = func(context.Context) error { return nil }
	owned := OwnedSession{Registry: RegistrySession{
		SessionID: "local_owned", CLISessionID: "cli-owned", CWD: workspace,
	}}
	err = runtime.ArchiveOwned(context.Background(), owned)
	if err == nil || !strings.Contains(err.Error(), "the open Claude Desktop conversation is") {
		t.Fatalf("ArchiveOwned() open-conversation error = %v", err)
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
		Path: []int{9, 9}, Role: "AXPopUpButton",
		Description: "More options for Echo hi",
		Hierarchy:   openConversationHierarchy(),
	}}
	target, err := validateArchiveTarget(owned, sessions, postTurn)
	if err != nil {
		t.Fatalf("validateArchiveTarget() on a post-turn tree: %v", err)
	}
	if target.menu.Description != "More options for Echo hi" {
		t.Fatalf("archive target does not name the owned session: %+v", target.menu)
	}
}

// Desktop names a session after its content, so a scenario that always sends
// the same prompt always earns the same name. Three sessions on this machine
// were called "Confirmation response" by 2026-09-06, and the run that created
// each of them could not clean up after itself: the guard demanded that the
// owned title be unique among active sessions, and every repeat run broke it.
//
// Uniqueness of the TITLE was only ever a proxy for the thing that matters —
// that the control the driver clicks belongs to the session it owns. The open
// conversation's own menu establishes that directly, and it stays unambiguous
// however many sidebar rows share a name.
func TestArchiveTargetResolvesWhenOtherSessionsShareTheTitle(t *testing.T) {
	owned := OwnedSession{Registry: RegistrySession{SessionID: "local_owned", CWD: "/repo/workspace"}}
	sessions := []RegistrySession{
		{SessionID: "local_owned", CWD: "/repo/workspace", Title: "Confirmation response"},
		{SessionID: "local_stray_a", CWD: "/repo/older", Title: "Confirmation response"},
		{SessionID: "local_stray_b", CWD: "/repo/older-still", Title: "Confirmation response"},
	}
	elements := []helperElement{
		sessionMenuElement("Confirmation response", 21),
		sessionMenuElement("Confirmation response", 21),
		sessionMenuElement("Confirmation response", 29),
	}
	target, err := validateArchiveTarget(owned, sessions, elements)
	if err != nil {
		t.Fatalf("validateArchiveTarget() with duplicate titles: %v", err)
	}
	if len(target.menu.Hierarchy) != 29 {
		t.Fatalf("archive target is a sidebar row, not the open conversation: %+v", target.menu)
	}

	// The open conversation must still be the owned one. A run whose session is
	// no longer on screen must refuse rather than archive whatever is.
	foreign := []helperElement{sessionMenuElement("Someone else's session", 29)}
	if _, err := validateArchiveTarget(owned, sessions, foreign); err == nil {
		t.Fatal("validateArchiveTarget() accepted a foreign open conversation")
	}
}

// The postcondition of a click must name the ONE control that proves the click
// worked. Live run 19 archived nothing because it watched for a bare `AXMenu`:
// `helper control_ambiguous: The postcondition selector matched 16 visible
// controls`. Claude Desktop has a menu bar, so "some menu appeared" is never a
// unique observation.
//
// What the driver actually needs to see is the Archive item it is about to
// click. This test reads the request the driver sends the helper.
func TestArchiveWatchesForTheArchiveItemNotAnyMenu(t *testing.T) {
	root := t.TempDir()
	workspace := "/repo/workspace"
	registryRoot := filepath.Join(root, "claude-code-sessions", "account", "profile")
	if err := os.MkdirAll(registryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	session := RegistrySession{
		SessionID: "local_owned", CLISessionID: "cli-owned", CWD: workspace, Title: "Same title",
	}
	data, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registryRoot, "local_owned.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	response, err := json.Marshal(helperResponse{OK: true, Elements: []helperElement{
		sessionMenuElement("Same title", 29),
	}})
	if err != nil {
		t.Fatal(err)
	}
	requests := filepath.Join(root, "requests.jsonl")
	helper := filepath.Join(root, "helper")
	script := "#!/bin/sh\ncat >> '" + requests + "'\nprintf '\\n' >> '" + requests +
		"'\nprintf '%s\\n' '" + string(response) + "'\n"
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
	runtime.frontDesktop = func(context.Context) error { return nil }
	owned := OwnedSession{Registry: RegistrySession{
		SessionID: "local_owned", CLISessionID: "cli-owned", CWD: workspace,
	}}
	// The fake never reports the session archived, so this ends in a timeout.
	// The requests it recorded on the way are what this test is about. The
	// budget has to outlast several helper SUBPROCESS launches under -race,
	// which a few hundred milliseconds does not.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = runtime.ArchiveOwned(ctx, owned)

	raw, err := os.ReadFile(requests)
	if err != nil {
		t.Fatalf("the driver sent the helper nothing; this check cannot run, which is a failure: %v", err)
	}
	var clicks int
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var request helperRequest
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			continue
		}
		if request.Command != "physical_click" || request.Postcondition == nil {
			continue
		}
		clicks++
		if request.Postcondition.Selector.Role == "AXMenu" &&
			request.Postcondition.Selector.Title == "" &&
			request.Postcondition.Selector.Description == "" {
			t.Fatalf("click %d watches for any menu, which Desktop's menu bar makes ambiguous: %+v",
				clicks, request.Postcondition.Selector)
		}
	}
	if clicks == 0 {
		t.Fatal("the driver issued no click; this check cannot run, which is a failure")
	}
}

// The archive path used to resolve the owned-session menu ONCE, outside its
// retry loop, and then click that one selector five times.
//
// Cell 2-18 failed all five attempts on 2026-09-06 with `helper stale_control:
// The current click point does not hit the selected control`, and left the
// session unarchived in the operator's Desktop. stale_control is the helper
// hit-testing a click point the renderer has already moved; re-clicking a
// selector resolved BEFORE that move cannot ever succeed. The retry existed and
// could not help, because it re-ran the click without re-running the resolve.
//
// RED-FIRST: against the single-resolve version every recorded click carried
// the first reading's hierarchy, and this failed on `every click reused the
// first reading`.
func TestArchiveReResolvesTheOwnedMenuOnEveryAttempt(t *testing.T) {
	root := t.TempDir()
	workspace := "/repo/workspace"
	registryRoot := filepath.Join(root, "claude-code-sessions", "account", "profile")
	if err := os.MkdirAll(registryRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	session := RegistrySession{
		SessionID: "local_owned", CLISessionID: "cli-owned", CWD: workspace, Title: "Owned title",
	}
	data, err := json.Marshal(session)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(registryRoot, "local_owned.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// The same menu, at two different depths: the renderer moved it between the
	// first reading and the rest. Both are the open conversation, both name the
	// owned session, and they differ only in the selector they produce.
	first := mustEncode(t, helperResponse{OK: true, Elements: []helperElement{
		sessionMenuElement("Owned title", 29),
	}})
	moved := mustEncode(t, helperResponse{OK: true, Elements: []helperElement{
		sessionMenuElement("Owned title", 31),
	}})
	refusal := mustEncode(t, helperResponse{OK: false, Error: &struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{Code: "stale_control", Message: "The current click point does not hit the selected control."}})

	requests := filepath.Join(root, "requests.jsonl")
	counter := filepath.Join(root, "inspects")
	helper := filepath.Join(root, "helper")
	script := "#!/bin/sh\n" +
		"req=$(cat)\n" +
		"printf '%s\\n' \"$req\" >> '" + requests + "'\n" +
		"case \"$req\" in\n" +
		"  *'\"command\":\"inspect\"'*)\n" +
		"    n=$(cat '" + counter + "' 2>/dev/null || echo 0)\n" +
		"    n=$((n+1)); printf '%s' \"$n\" > '" + counter + "'\n" +
		"    if [ \"$n\" -eq 1 ]; then printf '%s\\n' '" + first + "'\n" +
		"    else printf '%s\\n' '" + moved + "'; fi ;;\n" +
		"  *'\"command\":\"physical_click\"'*)\n" +
		"    printf '%s\\n' '" + refusal + "'; exit 1 ;;\n" +
		"  *) printf '%s\\n' '{\"ok\":true}' ;;\n" +
		"esac\n"
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
	fronted := 0
	runtime.frontDesktop = func(context.Context) error {
		fronted++
		return nil
	}
	owned := OwnedSession{Registry: RegistrySession{
		SessionID: "local_owned", CLISessionID: "cli-owned", CWD: workspace,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	// Every click is refused, so this ends in failure. What it did on the way is
	// the subject.
	if err := runtime.ArchiveOwned(ctx, owned); err == nil {
		t.Fatal("ArchiveOwned() returned nil; every click was refused")
	}

	depths := clickedMenuDepths(t, requests)
	if len(depths) < 2 {
		t.Fatalf("the driver clicked %d time(s); this check needs at least two attempts to compare", len(depths))
	}
	if depths[0] == depths[len(depths)-1] {
		t.Fatalf("every click reused the first reading (hierarchy depth %d); the menu had moved to %d",
			depths[0], 31)
	}
	if last := depths[len(depths)-1]; last != 31 {
		t.Fatalf("last click used hierarchy depth %d, want the moved menu's %d", last, 31)
	}
	if fronted < len(depths) {
		t.Fatalf("Desktop was brought forward %d time(s) for %d click attempts; a click point "+
			"hit-tested against a backgrounded window does not land", fronted, len(depths))
	}
}

func mustEncode(t *testing.T, response helperResponse) string {
	t.Helper()
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// clickedMenuDepths reads back the hierarchy depth of every owned-session menu
// the driver actually clicked. It fails rather than returning nothing when the
// recording is missing: inability to look must not read as "no clicks".
func clickedMenuDepths(t *testing.T, requests string) []int {
	t.Helper()
	raw, err := os.ReadFile(requests)
	if err != nil {
		t.Fatalf("the driver sent the helper nothing; this check cannot run, which is a failure: %v", err)
	}
	var depths []int
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var request helperRequest
		if err := json.Unmarshal([]byte(line), &request); err != nil {
			continue
		}
		if request.Command != "physical_click" || request.Selector == nil {
			continue
		}
		depths = append(depths, len(request.Selector.Hierarchy))
	}
	return depths
}
