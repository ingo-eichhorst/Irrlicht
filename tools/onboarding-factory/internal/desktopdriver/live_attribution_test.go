package desktopdriver

// Which launcher a session belongs to is attributed by the daemon AFTER the
// session first appears. A driver that reads "not yet attributed" as "attributed
// to somebody else" fails runs that were going perfectly.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// attributionServer serves the daemon's session list, handing out an
// unattributed session first and the attributed one afterwards.
func attributionServer(t *testing.T, sessionID, cwd string, hostAfter string) (*httptest.Server, *int) {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		host := ""
		if calls > 1 {
			host = hostAfter
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"sessions": []map[string]any{{
			"session_id": sessionID,
			"cwd":        cwd,
			"pid":        4321,
			"state":      "working",
			"launcher":   map[string]any{"host_bundle_id": host},
		}}})
	}))
	t.Cleanup(server.Close)
	return server, &calls
}

func attributionRuntime(t *testing.T, address string) *LiveRuntime {
	t.Helper()
	root := t.TempDir()
	helper := filepath.Join(root, "helper")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewLiveRuntime(LiveOptions{
		Home: root, HelperPath: helper, DaemonAddress: address,
		RecordingDirectory: filepath.Join(root, "recordings"), DesktopSupportRoot: root,
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

// RED-FIRST: cell 2-3 died at its very first step on 2026-09-07 with
// `Irrlicht host bundle ID is "", want "com.anthropic.claudefordesktop"`,
// against a session Claude Desktop had just created.
func TestAnUnattributedSessionIsWaitedFor(t *testing.T) {
	const sessionID = "cli-new"
	workspace := t.TempDir()
	server, calls := attributionServer(t, sessionID, workspace, desktopBundleID)
	runtime := attributionRuntime(t, strings.TrimPrefix(server.URL, "http://"))
	owned := OwnedSession{
		Registry:   RegistrySession{SessionID: "local_new", CLISessionID: sessionID, CWD: workspace},
		Transcript: TranscriptIdentity{SessionID: sessionID},
	}
	deadline := time.Now().Add(5 * time.Second)
	var seen SessionObservation
	for time.Now().Before(deadline) {
		observation, found, err := runtime.findOwnedIrrlichtSession(context.Background(), owned)
		if err != nil {
			t.Fatalf("observeOwnedSession() error = %v; an unattributed session is not a foreign one", err)
		}
		if found {
			seen = observation
			break
		}
	}
	if seen.SessionID != sessionID {
		t.Fatalf("the attributed session was never observed after %d call(s)", *calls)
	}
	if *calls < 2 {
		t.Fatalf("only %d call(s) were made; the first must have been waited through", *calls)
	}
}

// LOCK: a session attributed to a DIFFERENT host is still refused. Absence and
// contradiction are different findings.
func TestASessionAttributedElsewhereIsRefused(t *testing.T) {
	const sessionID = "cli-new"
	workspace := t.TempDir()
	server, _ := attributionServer(t, sessionID, workspace, "com.example.someone-else")
	runtime := attributionRuntime(t, strings.TrimPrefix(server.URL, "http://"))
	owned := OwnedSession{
		Registry:   RegistrySession{SessionID: "local_new", CLISessionID: sessionID, CWD: workspace},
		Transcript: TranscriptIdentity{SessionID: sessionID},
	}
	var err error
	for attempt := 0; attempt < 5 && err == nil; attempt++ {
		_, _, err = runtime.findOwnedIrrlichtSession(context.Background(), owned)
	}
	if err == nil {
		t.Fatal("a session belonging to another host was accepted")
	}
	if !strings.Contains(err.Error(), "host bundle ID") {
		t.Fatalf("error = %v, want it to name the wrong host", err)
	}
}
