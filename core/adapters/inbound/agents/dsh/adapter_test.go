package dsh

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/fswatcher"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

func TestAgentIdentity(t *testing.T) {
	a := Agent()
	if a.Identity.Name != AdapterName || a.Identity.DisplayName != "DeepSeek Harness" {
		t.Errorf("identity = %+v", a.Identity)
	}
	if a.Identity.IconSVGLight == "" || a.Identity.IconSVGDark == "" {
		t.Error("both icon variants must be present")
	}
}

func TestAgentProcessDeclaration(t *testing.T) {
	a := Agent()
	if _, ok := a.Process.Match.(agent.CommandPattern); !ok {
		t.Errorf("process match = %T, want CommandPattern", a.Process.Match)
	}
	if a.Process.PIDForSession == nil {
		t.Error("PIDForSession is nil")
	}
	if a.Process.SharedPIDOwner == nil {
		t.Error("SharedPIDOwner is nil")
	}
}

func TestSharedPIDOwnerRejectsMissingLock(t *testing.T) {
	transcript := filepath.Join(t.TempDir(), "session.v3.jsonl.zstd")
	if OwnsSharedPID("", transcript, os.Getpid()) {
		t.Fatal("missing session.lock confirmed PID ownership")
	}
	if OwnsSharedPID("", transcript, 0) {
		t.Fatal("missing session.lock confirmed a zero PID")
	}
}

func TestAgentSourceDeclaration(t *testing.T) {
	a := Agent()
	source, ok := a.Source.(agent.FilesUnderRoot)
	if !ok {
		t.Fatalf("source = %T, want FilesUnderRoot", a.Source)
	}
	if source.DirFunc == nil || source.SessionIDFromPath == nil || source.ParentSessionIDFromPath == nil {
		t.Error("source must resolve DSH_HOME lazily and derive linked directory-based session IDs")
	}
	if _, ok := source.Parser.(agent.JSONLineParser); !ok {
		t.Errorf("parser = %T, want JSONLineParser", source.Parser)
	}
}

func TestAgentPermissionDeclaration(t *testing.T) {
	a := Agent()
	if len(a.Permissions) != 1 {
		t.Fatalf("permission count = %d, want 1", len(a.Permissions))
	}
	p := a.Permissions[0]
	if p.Key != PermissionKeyTranscripts {
		t.Errorf("permission key = %q, want %q", p.Key, PermissionKeyTranscripts)
	}
	if p.Kind != permission.KindObserve {
		t.Errorf("permission kind = %q, want observe", p.Kind)
	}
	if p.Apply != nil {
		t.Error("observe permission Apply is not nil")
	}
	if p.Remove != nil {
		t.Error("observe permission Remove is not nil")
	}
	if p.Writes != nil {
		t.Error("observe permission Writes is not nil")
	}
}

func TestSessionsDirHonorsAbsoluteDSHHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv(dshHomeEnvVar, home)
	if got, want := sessionsDir(), filepath.Join(home, "sessions"); got != want {
		t.Errorf("sessionsDir() = %q, want %q", got, want)
	}

	t.Setenv(dshHomeEnvVar, "")
	if got := sessionsDir(); got != defaultRootDir {
		t.Errorf("default sessionsDir() = %q, want %q", got, defaultRootDir)
	}
}

func TestSessionIDFromPathSelectsHighestGeneration(t *testing.T) {
	const id = "session-600e7941-bf4f-4da4-9ef6-489168e13724"
	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"session.v2.jsonl.zstd",
		"session.v3.jsonl.zstd",
		"session.lock",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	if got := sessionIDFromPath(filepath.Join(dir, "session.v2.jsonl.zstd")); got != "" {
		t.Errorf("older generation minted session %q", got)
	}
	if got := sessionIDFromPath(filepath.Join(dir, "session.v3.jsonl.zstd")); got != id {
		t.Errorf("highest generation ID = %q, want %q", got, id)
	}
	if got := sessionIDFromPath(filepath.Join(dir, "session.lock")); got != "" {
		t.Errorf("lock file minted session %q", got)
	}
	if got := sessionIDFromPath(filepath.Join(filepath.Dir(dir), "not-a-session", "session.v3.jsonl.zstd")); got != "" {
		t.Errorf("invalid session directory minted session %q", got)
	}
}

// TestNativeSubagentHeaderIsDiscoverableAndLinked covers the durable shape
// documented for DSH's foreground, background, orphan-cleanup, and workflow
// children: a bare UUID directory and a first header marked origin:subagent.
// Before #1980's implementation this test fails because the bare child
// directory is rejected and Source does not expose a parent-header reader.
func TestNativeSubagentHeaderIsDiscoverableAndLinked(t *testing.T) {
	const parentID = "session-600e7941-bf4f-4da4-9ef6-489168e13724"
	const childID = "a0e1b2c3-d4e5-4f67-89a0-b1c2d3e4f5a6"
	dir := filepath.Join(t.TempDir(), childID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(dir, "session.v3.jsonl.zstd")
	writeZstdFrame(t, transcript, os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		`{"type":"session","version":3,"id":"`+childID+`","cwd":"/work","origin":"subagent","parentSession":"`+parentID+`"}`+"\n")

	if got := sessionIDFromPath(transcript); got != childID {
		t.Fatalf("child session ID = %q, want %q", got, childID)
	}
	source := Source().(agent.FilesUnderRoot)
	if source.ParentSessionIDFromPath == nil {
		t.Fatal("Source has no ParentSessionIDFromPath")
	}
	if got := source.ParentSessionIDFromPath(transcript); got != parentID {
		t.Errorf("child parent ID = %q, want %q", got, parentID)
	}
}

func TestParentSessionIDFromPathRejectsNonSubagentHeaders(t *testing.T) {
	const parentID = "session-600e7941-bf4f-4da4-9ef6-489168e13724"
	const childID = "a0e1b2c3-d4e5-4f67-89a0-b1c2d3e4f5a6"
	dir := filepath.Join(t.TempDir(), "session-"+childID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(dir, "session.v3.jsonl.zstd")
	writeZstdFrame(t, transcript, os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		`{"type":"session","version":3,"id":"session-`+childID+`","cwd":"/work","parentSession":"`+parentID+`","isSeeded":true}`+"\n")

	source := Source().(agent.FilesUnderRoot)
	if source.ParentSessionIDFromPath == nil {
		t.Fatal("Source has no ParentSessionIDFromPath")
	}
	if got := source.ParentSessionIDFromPath(transcript); got != "" {
		t.Errorf("non-subagent parent ID = %q, want empty", got)
	}
}

func TestNativeSubagentHeaderRequiresDirectoryIDMatch(t *testing.T) {
	const parentID = "session-600e7941-bf4f-4da4-9ef6-489168e13724"
	const directoryID = "a0e1b2c3-d4e5-4f67-89a0-b1c2d3e4f5a6"
	const headerID = "b0e1b2c3-d4e5-4f67-89a0-b1c2d3e4f5a6"
	dir := filepath.Join(t.TempDir(), directoryID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(dir, "session.v3.jsonl.zstd")
	writeZstdFrame(t, transcript, os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		`{"type":"session","version":3,"id":"`+headerID+`","cwd":"/work","origin":"subagent","parentSession":"`+parentID+`"}`+"\n")

	if got := sessionIDFromPath(transcript); got != "" {
		t.Errorf("mismatched child session ID = %q, want empty", got)
	}
	if got := parentSessionIDFromPath(transcript); got != "" {
		t.Errorf("mismatched child parent ID = %q, want empty", got)
	}
}

func TestNativeSubagentHeaderAcceptsBareParentID(t *testing.T) {
	const parentID = "b0e1b2c3-d4e5-4f67-89a0-b1c2d3e4f5a6"
	const childID = "a0e1b2c3-d4e5-4f67-89a0-b1c2d3e4f5a6"
	dir := filepath.Join(t.TempDir(), childID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(dir, "session.v3.jsonl.zstd")
	writeZstdFrame(t, transcript, os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		`{"type":"session","version":3,"id":"`+childID+`","cwd":"/work","origin":"subagent","parentSession":"`+parentID+`"}`+"\n")

	if got := parentSessionIDFromPath(transcript); got != parentID {
		t.Errorf("nested child parent ID = %q, want %q", got, parentID)
	}
}

func TestNativeSubagentHeaderRejectsOversizedHeader(t *testing.T) {
	const parentID = "session-600e7941-bf4f-4da4-9ef6-489168e13724"
	const childID = "a0e1b2c3-d4e5-4f67-89a0-b1c2d3e4f5a6"
	dir := filepath.Join(t.TempDir(), childID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(dir, "session.v3.jsonl.zstd")
	padding := strings.Repeat("x", maxNativeHeaderBytes)
	writeZstdFrame(t, transcript, os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		`{"type":"session","version":3,"id":"`+childID+`","cwd":"/work","origin":"subagent","parentSession":"`+parentID+`","padding":"`+padding+`"}`+"\n")

	if got := sessionIDFromPath(transcript); got != "" {
		t.Errorf("oversized child session ID = %q, want empty", got)
	}
}

// TestNativeSubagentRemovalKeepsTheKnownBareUUID is the removal-edge lock:
// fswatcher asks SessionIDFromPath after the file is gone, so live-header
// validation cannot run at that point. Keeping this identity produces the
// child's transcript_removed event without accepting any live unrelated UUID.
func TestNativeSubagentRemovalKeepsTheKnownBareUUID(t *testing.T) {
	const childID = "a0e1b2c3-d4e5-4f67-89a0-b1c2d3e4f5a6"
	dir := filepath.Join(t.TempDir(), childID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(dir, "session.v3.jsonl.zstd")
	if err := os.WriteFile(transcript, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(transcript); err != nil {
		t.Fatal(err)
	}
	if got := sessionIDFromPath(transcript); got != childID {
		t.Errorf("removed child session ID = %q, want %q", got, childID)
	}
}

// TestNativeSubagentRemovalEmitsTheChildLifecycleEvent verifies the actual
// fswatcher removal path. Removing the fallback in sessionIDFromDirectory
// made this test time out: fswatcher skipped the deleted bare child before it
// could emit EventRemoved.
func TestNativeSubagentRemovalEmitsTheChildLifecycleEvent(t *testing.T) {
	const parentID = "session-600e7941-bf4f-4da4-9ef6-489168e13724"
	const childID = "a0e1b2c3-d4e5-4f67-89a0-b1c2d3e4f5a6"
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	watcher := fswatcher.NewWithRoot(root, AdapterName, 0).
		WithSessionID(sessionIDFromPath).
		WithParentSessionID(parentSessionIDFromPath)
	events := watcher.Subscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- watcher.Watch(ctx) }()
	select {
	case <-watcher.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not become ready")
	}

	dir := filepath.Join(workspace, childID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	transcript := filepath.Join(dir, "session.v3.jsonl.zstd")
	writeZstdFrame(t, transcript, os.O_WRONLY|os.O_CREATE|os.O_TRUNC,
		`{"type":"session","version":3,"id":"`+childID+`","cwd":"/work","origin":"subagent","parentSession":"`+parentID+`"}`+"\n")
	waitForDSHEvent(t, events, agent.EventNewSession, childID)
	if err := os.Remove(transcript); err != nil {
		t.Fatal(err)
	}
	waitForDSHEvent(t, events, agent.EventRemoved, childID)

	cancel()
	if err := <-done; err != nil && err != context.Canceled {
		t.Errorf("watcher returned %v", err)
	}
}

func waitForDSHEvent(t *testing.T, events <-chan agent.Event, wantType agent.EventType, wantID string) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case event := <-events:
			if event.Type == wantType && event.SessionID == wantID {
				return
			}
		case <-deadline:
			t.Fatalf("timed out waiting for %s for %s", wantType, wantID)
		}
	}
}

func TestSessionIDFromPathIdentifiesRemovedHighestGeneration(t *testing.T) {
	const id = "session-600e7941-bf4f-4da4-9ef6-489168e13724"
	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	older := filepath.Join(dir, "session.v2.jsonl.zstd")
	removed := filepath.Join(dir, "session.v3.jsonl.zstd")
	for _, path := range []string{older, removed} {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}
	if got := sessionIDFromPath(removed); got != id {
		t.Errorf("removed highest generation ID = %q, want %q", got, id)
	}
}

func TestGenerationComparisonDoesNotOverflow(t *testing.T) {
	if compareGenerations("100000000000000000000000000000", "99999999999999999999999999999") <= 0 {
		t.Error("larger generation did not sort after smaller generation")
	}
	if got, ok := transcriptGeneration("session.v0003.jsonl.zstd"); !ok || got != "3" {
		t.Errorf("normalized generation = %q, %v; want 3, true", got, ok)
	}
}

func TestProcessCmdRegex(t *testing.T) {
	for _, cmd := range []string{
		"node /Users/ingo/.nvm/versions/node/v22.18.0/bin/dsh --profile web --no-open",
		"node /Users/ingo/.nvm/versions/node/v24.16.0/bin/dsh --profile tui",
		"dsh --profile headless prompt",
	} {
		if !processCmdRegex.MatchString(cmd) {
			t.Errorf("process pattern did not match %q", cmd)
		}
	}
	for _, cmd := range []string{
		"irrlichd --watch /Users/ingo/.dsh/sessions",
		"tail -f /Users/ingo/.dsh/sessions/x/session.v3.jsonl.zstd",
		"node /tmp/dsh-helper/index.js",
	} {
		if processCmdRegex.MatchString(cmd) {
			t.Errorf("process pattern matched unrelated command %q", cmd)
		}
	}
}

func TestSessionLockPath(t *testing.T) {
	path := "/Users/x/.dsh/sessions/--work--/session-600e7941-bf4f-4da4-9ef6-489168e13724/session.v3.jsonl.zstd"
	want := filepath.Join(filepath.Dir(path), sessionLockFilename)
	if got := sessionLockPath(path); got != want {
		t.Errorf("sessionLockPath() = %q, want %q", got, want)
	}
	if got := sessionLockPath("session.v3.jsonl.zstd"); got != "" {
		t.Errorf("parentless sessionLockPath() = %q, want empty", got)
	}
}
