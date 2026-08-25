package main

import (
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/domain/session"
)

// terminalAPIErrorTranscript is a claudecode transcript whose last line is the
// give-up shape: an `assistant` message flagged `isApiErrorMessage:true`.
//
// Modelled on the committed recording
// replaydata/agents/claudecode/scenarios/2-23_provider-overloaded-terminal/,
// which is the same failure #1815's reporter drove live (a 529 that never
// recovers). Trimmed to the fields claudecode's parser actually reads —
// terminalAPIError gates on the isApiErrorMessage VALUE and takes Message from
// the assistant text — so the fixture cannot drift into asserting on fields
// nothing consumes.
//
// The gate is on the value rather than the field's presence, so the `false`
// line here is not filler: it is the shape that must NOT turn a session red,
// sitting in the same file as the one that must.
const terminalAPIErrorTranscript = `{"type":"user","uuid":"11111111-1111-1111-1111-111111111111","timestamp":"2026-08-25T10:33:00.000Z","message":{"role":"user","content":"summarise the repo"},"cwd":"/tmp/irrlicht-1815","session_id":"SESSION_ID"}
{"type":"assistant","uuid":"22222222-2222-2222-2222-222222222222","timestamp":"2026-08-25T10:33:10.000Z","message":{"role":"assistant","model":"claude-opus-4-6","content":[{"type":"text","text":"Working on it."}],"usage":{"input_tokens":10,"output_tokens":5}},"isApiErrorMessage":false,"session_id":"SESSION_ID"}
{"type":"assistant","uuid":"33333333-3333-3333-3333-333333333333","timestamp":"2026-08-25T10:33:23.706Z","message":{"role":"assistant","model":"<synthetic>","stop_reason":"stop_sequence","content":[{"type":"text","text":"API Error: Repeated 529 Overloaded errors. The API is at capacity — this is usually temporary."}],"usage":{"input_tokens":0,"output_tokens":0}},"error":"server_error","isApiErrorMessage":true,"apiErrorStatus":529,"session_id":"SESSION_ID"}
`

// midTurnTranscript ends with an OPEN tool call — an assistant `tool_use` with
// no matching `tool_result`. That is what an agent's transcript looks like when
// its process dies mid-turn, and it is the shape the dead-process case needs:
// the transcript alone reads as work still in flight, so restoring only the
// persisted SessionError would not be enough to keep the session red. The
// classifier's process_death rule outranks the transcript-tier rules precisely
// so a frozen transcript like this one cannot repaint a dead session, and that
// rule reads a flag only a re-placed hold can set.
const midTurnTranscript = `{"type":"user","uuid":"44444444-4444-4444-4444-444444444444","timestamp":"2026-08-25T10:40:00.000Z","message":{"role":"user","content":"refactor the parser"},"cwd":"/tmp/irrlicht-1815","session_id":"SESSION_ID"}
{"type":"assistant","uuid":"55555555-5555-5555-5555-555555555555","timestamp":"2026-08-25T10:40:05.000Z","message":{"role":"assistant","model":"claude-opus-4-6","content":[{"type":"tool_use","id":"toolu_1815","name":"Edit","input":{"file_path":"/tmp/irrlicht-1815/parser.go"}}],"usage":{"input_tokens":12,"output_tokens":8}},"session_id":"SESSION_ID"}
`

// TestErrorStateSurvivesDaemonRestart is #1815's acceptance shape, run against a
// real irrlichd on an ephemeral port under temp dirs: drive a session into
// `error`, restart the daemon on the SAME state dir, assert it STILL reads
// `error`.
//
// TWO CASES, AND THE DIFFERENCE BETWEEN THEM IS THE WHOLE POINT. #1815 names two
// independent mechanisms with the same user-visible outcome, and exactly one
// fact separates them — whether the agent process is alive across the restart:
//
//   - alive: the row survives every startup sweep (isStartupZombie's dead-PID
//     predicate is false), and what is lost is the VERDICT. The tailer's sticky
//     sessionError was in-memory only, so the second daemon re-classified off a
//     frozen transcript and landed on ready. This is the mechanism the reporter
//     reproduced live at 6e646c7c.
//   - dead: the row itself is DELETED, by either of the two startup dead-PID
//     deleters, before anything can look at it. The reporter's run deliberately
//     did not cover this one and asked for it to be run rather than assumed.
//
// Both drive the FIRST daemon into `error` off a real transcript rather than
// seeding the verdict, so the ledger under test is written by the daemon itself.
// The lifetime-1 assertion is therefore also the vacuity guard: if the
// transcript stopped producing an error, this test fails there and says so,
// instead of passing lifetime 2 against a session that was never red.
func TestErrorStateSurvivesDaemonRestart(t *testing.T) {
	bin := buildIrrlichd(t)

	// MECHANISM 1 — the in-memory sticky error. Two daemon lifetimes: the first
	// produces the verdict off the transcript (and writes the ledger the fix
	// turns on), the second must still report it.
	t.Run("agent process alive across the restart", func(t *testing.T) {
		// Each subtest owns its HOME, its IRRLICHT_HOME (and so its unix socket
		// path), and an ephemeral TCP port, so the three are independent. Run
		// concurrently: each spends 8s doing nothing but holding an invariant,
		// and serialising that is ~15s of pure wall clock on every CI run.
		t.Parallel()
		homeDir := t.TempDir()
		stateDir := shortTempDir(t)
		seedGrantedTranscriptsConsent(t, stateDir)

		sessionID := "e5f1a2b3-1815-4c5d-8e9f-a0b1c2d3e4f5"
		transcript := writeTranscript(t, t.TempDir(), sessionID, terminalAPIErrorTranscript)

		// State `working` on purpose: the FIRST daemon has to produce the verdict
		// itself off the transcript, or the ledger this issue is about is never
		// written and lifetime 2 would be asserting against a hand-seeded value.
		seedPersistedSession(t, stateDir, &session.SessionState{
			SessionID:      sessionID,
			Adapter:        claudecode.AdapterName,
			State:          session.StateWorking,
			PID:            liveProcessPIDForRestart(t),
			TranscriptPath: transcript,
			CWD:            filepath.Dir(transcript),
			FirstSeen:      time.Now().Unix(),
			UpdatedAt:      time.Now().Unix(),
		})

		d := bootSmokeDaemonIn(t, bin, homeDir, stateDir)

		// Vacuity guard AND precondition: the first daemon must actually reach
		// `error`. Polled rather than slept — the seed runs inside
		// SessionDetector.Run, which starts AFTER the addr file is published, so
		// waitForAddr returning says nothing about whether the seed has run.
		if !pollUntil(t, 15*time.Second, 25*time.Millisecond, func() bool {
			return liveSessionState(t, d.addr, sessionID) == session.StateError
		}) {
			t.Fatalf("first daemon never classified %s as %s (got %q) — the transcript stopped "+
				"producing a session error, so nothing below would test a restart",
				sessionID, session.StateError, liveSessionState(t, d.addr, sessionID))
		}

		d.shutdown(t)

		d2 := bootSmokeDaemonIn(t, bin, homeDir, stateDir)
		defer d2.shutdown(t)
		requireSessionState(t, d2.addr, sessionID, session.StateError)
		holdSessionState(t, d2.addr, sessionID, session.StateError, 8*time.Second)
	})

	// MECHANISM 2 — the startup sweeps deleting exactly these rows. ONE daemon
	// lifetime, because the seeded row IS the previous daemon's output: this is
	// byte-for-byte what retainAsProcessDeath persists when an agent dies
	// mid-turn (StateError, the dead PID still recorded, a process_death
	// SessionError on the metrics). Booting a daemon against it IS the restart.
	//
	// The reporter's live run deliberately did not cover this case and asked for
	// it to be run rather than assumed.
	// BOTH ERROR CLASSES, because they are rescued by different halves of the fix
	// and only one of them was covered at first. A process_death row is retained
	// by its own re-registered verdict; a transcript-derived one (the reporter's
	// actual 529) has no verdict of its own in the new process, and used to fall
	// through the periodic sweep's diedMidTurn check and be deleted five seconds
	// after the restart — invisible to a test that stopped at the first poll.
	for _, tc := range []struct {
		name       string
		errClass   string
		errMessage string
	}{
		{"process death", session.ErrorClassProcessDeath, "agent process exited mid-turn — process watcher"},
		{"provider error", "rate_limit_error", "API Error: Repeated 529 Overloaded errors."},
	} {
		t.Run("agent process dead across the restart ("+tc.name+")", func(t *testing.T) {
			t.Parallel()
			homeDir := t.TempDir()
			stateDir := shortTempDir(t)
			seedGrantedTranscriptsConsent(t, stateDir)

			sessionID := "c4d5e6f7-1815-4a1b-9c2d-3e4f5a6b7c8d"
			transcript := writeTranscript(t, t.TempDir(), sessionID, midTurnTranscript)
			deadPID := deadPIDForRestart(t)

			seedPersistedSession(t, stateDir, &session.SessionState{
				SessionID:      sessionID,
				Adapter:        claudecode.AdapterName,
				State:          session.StateError,
				PID:            deadPID,
				TranscriptPath: transcript,
				CWD:            filepath.Dir(transcript),
				FirstSeen:      time.Now().Unix(),
				UpdatedAt:      time.Now().Unix(),
				Metrics: &session.SessionMetrics{
					SessionError: &session.SessionError{
						Phase:   session.ErrorPhaseTerminal,
						Class:   tc.errClass,
						Message: tc.errMessage + " (pid " + strconv.Itoa(deadPID) + ")",
					},
				},
			})

			d := bootSmokeDaemonIn(t, bin, homeDir, stateDir)
			defer d.shutdown(t)
			requireSessionState(t, d.addr, sessionID, session.StateError)
			// Past at least one liveness-sweep tick — see holdSessionState.
			holdSessionState(t, d.addr, sessionID, session.StateError, 8*time.Second)
		})
	}
}

// requireSessionState polls the running daemon until sessionID reports want, and
// fails with what it actually saw and how long it waited.
//
// Polls the SUBJECT — the verdict the daemon reports for this session — rather
// than a side effect on the way in, and never sleeps: a wrong answer here is
// stable (the session settles green, or the row is gone and stays gone), so the
// deadline is the whole cost on the failing path.
func requireSessionState(t *testing.T, addr, sessionID, want string) {
	t.Helper()
	started := time.Now()
	var got string
	ok := pollUntil(t, 10*time.Second, 25*time.Millisecond, func() bool {
		got = liveSessionState(t, addr, sessionID)
		return got == want
	})
	if !ok {
		t.Fatalf("after a daemon restart session %s reads %q, want %q (polled %v) — "+
			"#1815: the error verdict did not survive the restart",
			sessionID, got, want, time.Since(started).Round(time.Millisecond))
	}
}

// holdSessionState requires that sessionID keeps reading want for the whole of
// d, rather than merely reaching it once.
//
// SURVIVING STARTUP IS NOT SURVIVING. requireSessionState returns on the first
// matching poll, which for the dead-process case happened in ~30ms — before the
// periodic liveness sweep had ticked even once. That sweep is a SEPARATE deleter
// from the two startup ones, and it took the rescued row about five seconds
// later; the test was green throughout, because the failure lives entirely in
// the window after the first successful poll. The hold has to outlast at least
// one sweep interval for "it survived the restart" to mean anything.
func holdSessionState(t *testing.T, addr, sessionID, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	started := time.Now()
	for time.Now().Before(deadline) {
		if got := liveSessionState(t, addr, sessionID); got != want {
			t.Fatalf("session %s held %q for only %v before flipping to %q (wanted %q for %v) — "+
				"#1815: the row survived startup and was taken by a later sweep",
				sessionID, want, time.Since(started).Round(time.Millisecond), got, want, d)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// liveSessionState asks the RUNNING daemon what state it currently reports for
// one session, or "<absent>" when the session is not in the list at all.
//
// Absent and green are DIFFERENT failures — #1815's two mechanisms produce one
// each — so they must not collapse into the same string. A caller that saw
// "<absent>" learns the row was deleted (the startup sweep); one that saw
// "ready" learns the row survived and the verdict did not.
func liveSessionState(t *testing.T, addr, sessionID string) string {
	t.Helper()
	// Its own client rather than http.DefaultClient: smokeDaemon.shutdown calls
	// http.DefaultClient.CloseIdleConnections(), which is process-global, so
	// under t.Parallel one subtest's teardown would drop the others' idle
	// keep-alives. Harmless (they redial) but a coupling worth not having.
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get("http://" + addr + "/api/v1/sessions")
	if err != nil {
		t.Fatalf("GET /api/v1/sessions: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/v1/sessions: status %d", resp.StatusCode)
	}
	// Decoded generically and walked recursively rather than through the typed
	// response: the payload is a TREE (groups nest groups, agents nest
	// children) and session.Agent embeds *SessionState inline, so a typed walk
	// would have to re-encode that nesting and would go quietly blind to a
	// session parked one level deeper than it expected.
	var payload any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("decode /api/v1/sessions: %v", err)
	}
	if st, found := findSessionState(payload, sessionID); found {
		return st
	}
	return "<absent>"
}

// findSessionState walks an arbitrarily nested decoded JSON payload for the
// object carrying session_id == want, and returns its `state`.
func findSessionState(node any, want string) (string, bool) {
	switch n := node.(type) {
	case map[string]any:
		if id, _ := n["session_id"].(string); id == want {
			st, _ := n["state"].(string)
			return st, true
		}
		for _, v := range n {
			if st, ok := findSessionState(v, want); ok {
				return st, true
			}
		}
	case []any:
		for _, v := range n {
			if st, ok := findSessionState(v, want); ok {
				return st, true
			}
		}
	}
	return "", false
}

// seedGrantedTranscriptsConsent grants Claude Code's observe-kind permission, so
// the daemon under test actually reads the seeded transcript.
//
// Without it every assertion here would be vacuous in the quietest possible
// way: a fresh IRRLICHT_HOME leaves every permission PENDING, seedReevaluateStates
// is consent-gated per adapter, and an un-consented session is simply left
// persisted as-is — so the test would pass while the daemon monitored nothing.
func seedGrantedTranscriptsConsent(t *testing.T, stateDir string) {
	t.Helper()
	seedGrantedConsent(t, stateDir, claudecode.PermissionKeyTranscripts)
}

// seedPersistedSession writes one session row into the state dir the daemon will
// boot against, standing in for what a previous daemon run left behind.
//
// Through filesystem.SessionRepository rather than a hand-written
// <id>.json, on seedGrantedHooksConsent's argument: the filename convention and
// the record's shape belong to that type.
func seedPersistedSession(t *testing.T, stateDir string, state *session.SessionState) {
	t.Helper()
	repo := filesystem.NewWithDir(filepath.Join(stateDir, "instances"))
	if err := repo.Save(state); err != nil {
		t.Fatalf("seed session %s: %v", state.SessionID, err)
	}
}

// writeTranscript writes one of this file's fixture transcripts into dir under
// sessionID's name, substituting the session id, and returns its path.
func writeTranscript(t *testing.T, dir, sessionID, fixture string) string {
	t.Helper()
	path := filepath.Join(dir, sessionID+".jsonl")
	body := strings.ReplaceAll(fixture, "SESSION_ID", sessionID)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript %s: %v", path, err)
	}
	return path
}

// liveProcessPIDForRestart returns the PID of a child that stays alive for the
// whole test — the "agent still running" half of #1815's split.
func liveProcessPIDForRestart(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("sleep", "120")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	return cmd.Process.Pid
}

// deadPIDForRestart spawns and reaps a process, returning a PID known to be
// dead — the "agent gone" half of #1815's split, and the case its reporter
// explicitly left unrun.
//
// Skips rather than guesses if the kernel recycles the PID before we can
// confirm it: a recycled PID is alive, which would silently convert this into a
// second copy of the live case.
func deadPIDForRestart(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start true: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	if err := syscall.Kill(pid, 0); err == nil {
		t.Skipf("dead PID %d was recycled before the test could observe it", pid)
	}
	return pid
}
