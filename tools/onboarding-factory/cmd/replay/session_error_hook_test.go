package main

import (
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// TestHookRetiresSessionError_OnlyOnATurnBoundary pins which hook effects can
// retire a session error in the offline harness.
//
// Every row is read from the shared session.HookSignal table rather than
// hand-built, so a future hook whose row asserts SignalTurnDone is covered here
// the moment it is added — the harness and the daemon read the same table, and
// this is the assertion that they agree about what it means.
func TestHookRetiresSessionError_OnlyOnATurnBoundary(t *testing.T) {
	for _, tc := range []struct {
		hook string
		want bool
	}{
		{session.HookStop, true},
		{session.HookPermissionRequest, false},
		{session.HookPreToolUse, false},
		{session.HookPostToolUse, false},
		{session.HookPostToolUseFailure, false},
		{session.HookAfterTool, false},
	} {
		t.Run(tc.hook, func(t *testing.T) {
			effect, ok := session.HookSignal(tc.hook)
			if !ok {
				t.Fatalf("%s has no row in session.HookSignal — this case cannot "+
					"observe what it claims to", tc.hook)
			}
			if got := hookRetiresSessionError(effect); got != tc.want {
				t.Errorf("hookRetiresSessionError(%s) = %v, want %v", tc.hook, got, tc.want)
			}
		})
	}
}

// claudecode transcript lines carrying each error shape, trimmed to the fields
// the parser reads. Copied in shape (not verbatim) from the committed
// 2-14_turn-aborted-by-error and 2-9_token-quota-exhausted recordings; the
// adapter's own tests are what pin them against the real files.
const (
	terminalErrLine = `{"type":"assistant","timestamp":"2026-08-05T10:00:00Z",` +
		`"isApiErrorMessage":true,"message":{"role":"assistant","stop_reason":"stop_sequence",` +
		`"content":[{"type":"text","text":"API Error: the provider failed"}]}}`
	retryingErrLine = `{"type":"system","subtype":"api_error","timestamp":"2026-08-05T10:00:00Z",` +
		`"error":{"status":429,"type":"rate_limit_error",` +
		`"error":{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}},` +
		`"retryInMs":616.45,"retryAttempt":1,"maxRetries":10}`
)

// TestRetireSessionErrorOnHookBoundary_MatchesTheTailerRule is the harness's
// half of #1799's daemon mirror, exercised through the real method.
//
// It asserts BOTH copies the method touches agree afterwards: the tailer's
// sticky field and the cached lastMetrics a hook pass classifies off. Those
// disagreeing is worse than either answer — the viewer would show a failure as
// retired for exactly the pass it renders while the tailer still held it.
//
// A recording cannot cover this: no committed recording carries both a hook
// event and a session error (see hookRetiresSessionError), so this test is the
// only thing standing between the harness and a silent drift from the daemon.
func TestRetireSessionErrorOnHookBoundary_MatchesTheTailerRule(t *testing.T) {
	stopEffect, ok := session.HookSignal(session.HookStop)
	if !ok {
		t.Fatal("session.HookStop has no row in HookSignal")
	}
	if !hookRetiresSessionError(stopEffect) {
		t.Fatal("a Stop hook must assert a turn boundary")
	}

	for _, tc := range []struct {
		name    string
		line    string
		wantNil bool
		why     string
	}{
		{
			name:    "terminal",
			line:    terminalErrLine,
			wantNil: false,
			why:     "the Stop hook fires for the very turn that failed",
		},
		{
			name:    "retrying",
			line:    retryingErrLine,
			wantNil: true,
			why:     "another attempt was scheduled and the turn then completed",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "transcript.jsonl")
			if err := os.WriteFile(path, []byte(tc.line+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			tt := tailer.NewTranscriptTailer(path, &claudecode.Parser{}, claudecode.AdapterName)
			m, err := tt.TailAndProcess()
			if err != nil {
				t.Fatalf("TailAndProcess: %v", err)
			}
			if m.SessionError == nil {
				t.Fatal("precondition failed: the line produced no session error, so " +
					"this case cannot observe a retirement")
			}

			r := &sidecarReplayer{tailer: tt, lastMetrics: m}
			r.retireSessionErrorOnHookBoundary(stopEffect)

			// The cached copy — what this pass classifies off.
			if got := r.lastMetrics.SessionError == nil; got != tc.wantNil {
				t.Errorf("cached SessionError == nil is %v, want %v — %s",
					got, tc.wantNil, tc.why)
			}
			// The sticky copy — what every later pass reads. A pass that consumes
			// no new bytes still re-surfaces it, so this reads the tailer's own
			// state rather than the cache above.
			after, err := tt.TailAndProcess()
			if err != nil {
				t.Fatalf("TailAndProcess after the boundary: %v", err)
			}
			if got := after.SessionError == nil; got != tc.wantNil {
				t.Errorf("sticky SessionError == nil is %v, want %v — the two copies "+
					"must not disagree", got, tc.wantNil)
			}
		})
	}
}
