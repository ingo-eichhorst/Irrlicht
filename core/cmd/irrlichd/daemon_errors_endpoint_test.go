package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
)

// serveSessionsWithHookHealth boots the real /api/v1/sessions handler over a
// one-session repo and returns the decoded payload as a generic map.
//
// Decoding into map[string]any rather than into sessionsResponse is the same
// technique permission_unapplied_aggregate_test.go documents: it asserts what
// is ACTUALLY ON THE WIRE, so "the field is omitted" and "the field is present
// and empty" are distinguishable — which is the whole claim the `omitempty`
// tag makes. A typed decode cannot tell those apart.
func serveSessionsWithHookHealth(t *testing.T, hookHealth func() services.HookHealthSnapshot) map[string]any {
	t.Helper()
	repo := filesystem.NewWithDir(t.TempDir())
	now := time.Now().Unix()
	if err := repo.Save(&session.SessionState{
		SessionID:   "sess-1",
		State:       session.StateReady,
		ProjectName: "proj-a",
		FirstSeen:   now,
		UpdatedAt:   now,
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	push := services.NewPushService()
	orchMonitor := services.NewOrchestratorMonitor(nil, push, nil)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/sessions", handleGetSessions(repo, orchMonitor, nil, hookHealth))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/v1/sessions")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", resp.StatusCode)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// Guard the guard: if the payload lost its groups the assertions below
	// would be inspecting a response that never rendered, and an absent
	// daemon_errors key would read as "healthy" rather than "nothing ran".
	if _, ok := out["groups"]; !ok {
		t.Fatalf("payload has no groups — the handler did not render a real response: %+v", out)
	}
	return out
}

// TestHandleGetSessions_DaemonErrorsReachTheWire is the reachability half of
// #1801's daemon-wide banner: services.DaemonErrors is unit-tested next door,
// but a pure function nobody calls renders nothing. This drives the real
// handler and reads the real JSON.
func TestHandleGetSessions_DaemonErrorsReachTheWire(t *testing.T) {
	faulty := func() services.HookHealthSnapshot {
		return services.HookHealthSnapshot{
			Channels: []services.HookChannelHealth{
				{Adapter: "claude-code", Armed: true, Silent: true, TurnsSinceReceipt: 9},
			},
		}
	}
	got := serveSessionsWithHookHealth(t, faulty)

	raw, ok := got["daemon_errors"]
	if !ok {
		t.Fatalf("daemon_errors missing from the payload of a daemon with a silent hook channel: %+v", got)
	}
	list, ok := raw.([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("daemon_errors: want a 1-element list, got %#v", raw)
	}
	entry, ok := list[0].(map[string]any)
	if !ok {
		t.Fatalf("daemon_errors[0]: want an object, got %#v", list[0])
	}
	for _, key := range []string{"kind", "scope", "message"} {
		v, ok := entry[key]
		if !ok {
			t.Errorf("daemon_errors[0] has no %q key: %#v", key, entry)
			continue
		}
		if s, _ := v.(string); s == "" {
			t.Errorf("daemon_errors[0].%s is empty: %#v", key, entry)
		}
	}
	if entry["kind"] != services.DaemonErrorHookChannelSilent {
		t.Errorf("kind: got %v, want %q", entry["kind"], services.DaemonErrorHookChannelSilent)
	}
}

// TestHandleGetSessions_OmitsDaemonErrorsWhenHealthy pins the other half of the
// contract, in both of the ways it can be true: a daemon with nothing wrong,
// and a caller with no hook counters to report at all (nil — the seam
// liveHookHealth already documents, and what every non-daemon process passes).
// A healthy payload must be byte-identical to what it was before the field
// existed, or every client's parsing changes for no reason.
func TestHandleGetSessions_OmitsDaemonErrorsWhenHealthy(t *testing.T) {
	healthy := func() services.HookHealthSnapshot {
		return services.HookHealthSnapshot{
			Channels: []services.HookChannelHealth{
				{Adapter: "claude-code", Armed: true, Receipts: 5},
			},
			EntryReverification: services.HookEntryReverifySnapshot{
				Targets: []services.HookEntryHealth{
					{Adapter: "claude-code", Permission: "hooks", Watched: true},
				},
			},
		}
	}
	for name, provider := range map[string]func() services.HookHealthSnapshot{
		"healthy daemon":     healthy,
		"no health provider": nil,
	} {
		t.Run(name, func(t *testing.T) {
			got := serveSessionsWithHookHealth(t, provider)
			if v, ok := got["daemon_errors"]; ok {
				t.Errorf("daemon_errors must be omitted, got %#v", v)
			}
		})
	}
}
