package hermes

import (
	"database/sql"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
)

// drain collects every event currently queued on ch.
func drain(ch <-chan agent.Event) []agent.Event {
	var out []agent.Event
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

// scanNow runs one scan with the debounce defeated, so a test can drive
// several scans back to back.
func scanNow(w *Watcher) {
	w.scanMu.Lock()
	w.lastScan = time.Time{}
	w.scanMu.Unlock()
	w.scan()
}

// agentEventFor runs one scan and returns the first emitted event.
func agentEventFor(t *testing.T, w *Watcher, _ *sql.DB) agent.Event {
	t.Helper()
	ch := w.Subscribe()
	scanNow(w)
	evs := drain(ch)
	if len(evs) == 0 {
		t.Fatal("expected at least one event")
	}
	return evs[0]
}

func TestWatcher_EmitsNewSessionOnce(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "s1", source: "tui", model: "gpt-5.6-luna", cwd: "/work/proj", started: 1000, ended: 0, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})

	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { return "" }
	ch := w.Subscribe()

	scanNow(w)
	evs := drain(ch)
	// Discovery emits new_session (announce) plus one activity (go read the
	// store) — see reconcile's comment on why the pairing is deliberate.
	if len(evs) != 2 || evs[0].Type != agent.EventNewSession || evs[1].Type != agent.EventActivity {
		t.Fatalf("first scan: got %+v, want new_session then activity", evs)
	}
	if evs[0].SessionID != "s1" {
		t.Errorf("SessionID = %q", evs[0].SessionID)
	}
	if evs[0].CWD != "/work/proj" {
		t.Errorf("CWD = %q, want /work/proj from the stored cwd", evs[0].CWD)
	}
	if evs[0].ProjectDir != "proj" {
		t.Errorf("ProjectDir = %q, want proj", evs[0].ProjectDir)
	}

	// A second scan with nothing changed must be silent — otherwise the
	// daemon sees a new session on every poll tick.
	scanNow(w)
	if evs := drain(ch); len(evs) != 0 {
		t.Errorf("unchanged rescan emitted %+v, want nothing", evs)
	}
}

func TestWatcher_EmitsActivityWhenConversationGrows(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "s1", source: "tui", model: "m", cwd: "/work/proj", started: 1000, ended: 0, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})

	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { return "" }
	ch := w.Subscribe()
	scanNow(w)
	drain(ch)

	// The agent replies and ends its turn.
	insertMessage(t, db, messageRow{sessionID: "s1", role: "assistant", content: "done", toolCalls: "", toolCallID: "", finish: "stop", ts: 1002})
	if _, err := db.Exec(`UPDATE sessions SET message_count = 2 WHERE id = 's1'`); err != nil {
		t.Fatal(err)
	}

	scanNow(w)
	evs := drain(ch)
	if len(evs) != 1 || evs[0].Type != agent.EventActivity {
		t.Fatalf("got %+v, want one activity event", evs)
	}
	if !evs[0].Terminal {
		t.Error("finish_reason=stop on the newest message must mark the activity Terminal")
	}
}

// A turn that is still running must NOT be marked terminal, or the session
// settles to ready while the agent is mid-tool-call.
func TestWatcher_ToolCallTurnIsNotTerminal(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "s1", source: "tui", model: "m", cwd: "/work/proj", started: 1000, ended: 0, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})

	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { return "" }
	ch := w.Subscribe()
	scanNow(w)
	drain(ch)

	insertMessage(t, db, messageRow{sessionID: "s1", role: "assistant", content: "", toolCalls: realToolCallsJSON, toolCallID: "", finish: "tool_calls", ts: 1002})
	if _, err := db.Exec(`UPDATE sessions SET message_count = 2 WHERE id = 's1'`); err != nil {
		t.Fatal(err)
	}
	scanNow(w)
	evs := drain(ch)
	if len(evs) != 1 {
		t.Fatalf("got %+v, want one activity event", evs)
	}
	if evs[0].Terminal {
		t.Error("finish_reason=tool_calls means the agent is still working — must not be Terminal")
	}
}

// ended_at flipping to non-NULL closes the session even when the closing
// write adds no message.
func TestWatcher_SessionEndEmitsTerminalActivity(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "s1", source: "cli", model: "m", cwd: "", started: 1000, ended: 0, msgs: 2})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})

	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { return "" }
	ch := w.Subscribe()
	scanNow(w)
	drain(ch)

	if _, err := db.Exec(`UPDATE sessions SET ended_at = 1042, end_reason='agent_close' WHERE id='s1'`); err != nil {
		t.Fatal(err)
	}
	scanNow(w)
	evs := drain(ch)
	if len(evs) != 1 || evs[0].Type != agent.EventActivity || !evs[0].Terminal {
		t.Fatalf("got %+v, want one terminal activity event", evs)
	}
}

// Verified on a live store: sessions.cwd is populated only by Hermes' TUI
// gateway, so a source='cli' row has none and the binding must come from
// the live process.
func TestWatcher_CLISessionBorrowsCWDFromProcess(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "s1", source: "cli", model: "m", cwd: "", started: 1000, ended: 0, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})

	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { return "/live/cwd" }
	ev := agentEventFor(t, w, db)

	if ev.CWD != "/live/cwd" {
		t.Errorf("CWD = %q, want the live process CWD", ev.CWD)
	}
	if ev.ProjectDir != "cwd" {
		t.Errorf("ProjectDir = %q, want cwd", ev.ProjectDir)
	}
}

// An ambiguous process set must produce NO binding rather than a guess:
// attributing a session to the wrong project is a silent error the user
// cannot see.
func TestWatcher_AmbiguousProcessYieldsNoCWD(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "s1", source: "cli", model: "m", cwd: "", started: 1000, ended: 0, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})

	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { return "" } // defaultLiveCWD returns "" when len != 1
	ev := agentEventFor(t, w, db)

	if ev.CWD != "" {
		t.Errorf("CWD = %q, want empty", ev.CWD)
	}
	if ev.ProjectDir != "" {
		t.Errorf("ProjectDir = %q, want empty when CWD is unknown", ev.ProjectDir)
	}
}

// The gateway filter applies to discovery too, not just metrics.
func TestWatcher_SkipsGatewaySessions(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "wa", source: "whatsapp", model: "m", cwd: "/work/proj", started: 1000, ended: 0, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "wa", role: "user", content: "hi", toolCalls: "", toolCallID: "", finish: "", ts: 1001})
	insertSession(t, db, sessionRow{id: "cli1", source: "cli", model: "m", cwd: "", started: 1000, ended: 0, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "cli1", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})

	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { return "" }
	ch := w.Subscribe()
	scanNow(w)

	for _, ev := range drain(ch) {
		if ev.SessionID == "wa" {
			t.Error("a WhatsApp session must never surface as a coding session")
		}
	}
}

func TestWatcher_MaxAgeExcludesOldSessions(t *testing.T) {
	path, db := newTestStore(t)
	now := float64(time.Now().Unix())
	insertSession(t, db, sessionRow{id: "old", source: "tui", model: "m", cwd: "/work/proj", started: now - 7200, ended: 0, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "old", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: now - 7200})
	insertSession(t, db, sessionRow{id: "new", source: "tui", model: "m", cwd: "/work/proj", started: now - 10, ended: 0, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "new", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: now - 10})

	w := NewWithStorePath(path, time.Hour)
	w.liveCWD = func() string { return "" }
	ch := w.Subscribe()
	scanNow(w)

	evs := drain(ch)
	if len(evs) == 0 {
		t.Fatal("expected the recent session to be emitted")
	}
	for _, ev := range evs {
		if ev.SessionID != "new" {
			t.Errorf("session %q emitted, want only the recent one", ev.SessionID)
		}
	}
}

func TestWatcher_ParentSessionIDIsCarried(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "child", source: "cli", model: "m", cwd: "", started: 1000, ended: 0, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "child", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})
	if _, err := db.Exec(`UPDATE sessions SET parent_session_id='parent' WHERE id='child'`); err != nil {
		t.Fatal(err)
	}

	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { return "" }
	ev := agentEventFor(t, w, db)
	if ev.ParentSessionID != "parent" {
		t.Errorf("ParentSessionID = %q, want parent", ev.ParentSessionID)
	}
}

func TestWatcher_IdentityAndRoot(t *testing.T) {
	w := NewWithStorePath("/h/state.db", 0).WithIdentity(Agent().Identity)
	if w.Root() != "/h/state.db" {
		t.Errorf("Root() = %q", w.Root())
	}
	if w.Adapter() != AdapterName {
		t.Errorf("Adapter() = %q", w.Adapter())
	}
	if w.Identity().Name != AdapterName {
		t.Errorf("Identity().Name = %q", w.Identity().Name)
	}
}

func TestWatcher_UnsubscribeClosesChannel(t *testing.T) {
	w := NewWithStorePath("/h/state.db", 0)
	ch := w.Subscribe()
	w.Unsubscribe(ch)
	if _, open := <-ch; open {
		t.Error("Unsubscribe must close the channel")
	}
}

// Debouncing exists because opening the store read-only touches it, which
// can trigger a further fsnotify Write and loop.
func TestWatcher_ScanIsDebounced(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "s1", source: "tui", model: "m", cwd: "/work/proj", started: 1000, ended: 0, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})

	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { return "" }
	ch := w.Subscribe()

	w.scan() // first scan runs
	if evs := drain(ch); len(evs) != 2 {
		t.Fatalf("first scan emitted %d events, want 2 (new_session + activity)", len(evs))
	}
	// Immediately after, the debounce should suppress the work entirely.
	insertSession(t, db, sessionRow{id: "s2", source: "tui", model: "m", cwd: "/work/proj", started: 1000, ended: 0, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "s2", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})
	w.scan()
	if evs := drain(ch); len(evs) != 0 {
		t.Errorf("debounced scan emitted %+v, want nothing", evs)
	}
}

// Cursors for sessions that have aged out of the scan window must be
// dropped, or a long-lived daemon retains one entry per Hermes session ever
// observed. Safe because the filter is `started_at > cutoff` and started_at
// is immutable: an aged-out session can never re-enter and be re-emitted.
func TestWatcher_PrunesCursorsForVanishedSessions(t *testing.T) {
	path, db := newTestStore(t)
	now := float64(time.Now().Unix())
	insertSession(t, db, sessionRow{id: "s1", source: "tui", model: "m", cwd: "/work/proj", started: now - 10, ended: 0, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: now - 10})

	w := NewWithStorePath(path, time.Hour)
	w.liveCWD = func() string { return "" }
	ch := w.Subscribe()
	scanNow(w)
	drain(ch)

	w.mu.Lock()
	n := len(w.sessions)
	w.mu.Unlock()
	if n != 1 {
		t.Fatalf("tracked %d sessions after first scan, want 1", n)
	}

	// Age the session out of the window; the next scan no longer returns it.
	if _, err := db.Exec(`UPDATE sessions SET started_at = ? WHERE id='s1'`, now-7200); err != nil {
		t.Fatal(err)
	}
	scanNow(w)

	w.mu.Lock()
	n = len(w.sessions)
	w.mu.Unlock()
	if n != 0 {
		t.Errorf("tracked %d sessions after the row aged out, want 0 — cursors leak", n)
	}
}

// The live-process probe shells out to pgrep + lsof, so it must run at most
// once per scan rather than once per row. Regression guard for the version
// that called it from reconcile: with N unbound CLI rows it cost N probes on
// every 2s tick, even with nothing changed.
func TestWatcher_ResolvesLiveCWDAtMostOncePerScan(t *testing.T) {
	path, db := newTestStore(t)
	for _, id := range []string{"a", "b", "c", "d"} {
		insertSession(t, db, sessionRow{id: id, source: "cli", model: "m", started: 1000, msgs: 1})
		insertMessage(t, db, messageRow{sessionID: id, role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})
	}

	calls := 0
	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { calls++; return "/live/cwd" }

	ch := w.Subscribe()
	scanNow(w)
	drain(ch)

	if calls != 1 {
		t.Errorf("liveCWD called %d times for 4 rows in one scan, want 1", calls)
	}
}

// When every row carries a stored cwd there is nothing to fall back to, so
// the probe must not run at all.
func TestWatcher_SkipsLiveCWDProbeWhenAllRowsHaveCWD(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "s1", source: "tui", model: "m", cwd: "/work/proj", started: 1000, ended: 0, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "go", toolCalls: "", toolCallID: "", finish: "", ts: 1001})

	calls := 0
	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { calls++; return "/should/not/be/used" }

	ch := w.Subscribe()
	scanNow(w)
	evs := drain(ch)

	if calls != 0 {
		t.Errorf("liveCWD probed %d times though every row had a stored cwd, want 0", calls)
	}
	if evs[0].CWD != "/work/proj" {
		t.Errorf("CWD = %q, want the stored cwd", evs[0].CWD)
	}
}

// A session that was ALREADY finished when the watcher first saw it is
// history: new_session carries it and its state is settled. Pairing it with
// an activity event doubled the cold-start burst — one scan over a store
// with N in-window sessions emitted 2N events into a 64-slot channel that
// drops silently when full, which is the most likely cause of a concurrent
// session intermittently never surfacing.
func TestWatcher_ColdStartHistoryEmitsNewSessionOnly(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "done", source: "cli", model: "m", started: 1000, ended: 1042, msgs: 2})
	insertMessage(t, db, messageRow{sessionID: "done", role: "user", content: "go", ts: 1001})
	insertMessage(t, db, messageRow{sessionID: "done", role: "assistant", content: "ok", finish: "stop", ts: 1002})

	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { return "" }
	ch := w.Subscribe()
	scanNow(w)

	evs := drain(ch)
	if len(evs) != 1 {
		t.Fatalf("got %d events %+v, want exactly 1 (new_session only)", len(evs), evs)
	}
	if evs[0].Type != agent.EventNewSession {
		t.Errorf("Type = %v, want new_session", evs[0].Type)
	}
}

// The burst matters at scale: a cold start over a store of finished sessions
// must stay at one event each, not two.
func TestWatcher_ColdStartBurstIsOnePerFinishedSession(t *testing.T) {
	path, db := newTestStore(t)
	const n = 20
	for i := 0; i < n; i++ {
		id := "s" + string(rune('a'+i))
		insertSession(t, db, sessionRow{id: id, source: "cli", model: "m", started: 1000, ended: 1042, msgs: 2})
		insertMessage(t, db, messageRow{sessionID: id, role: "user", content: "go", ts: 1001})
		insertMessage(t, db, messageRow{sessionID: id, role: "assistant", content: "ok", finish: "stop", ts: 1002})
	}

	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { return "" }
	ch := w.Subscribe()
	scanNow(w)

	if got := len(drain(ch)); got != n {
		t.Errorf("cold start emitted %d events for %d finished sessions, want %d", got, n, n)
	}
}

// A session still LIVE at first sight keeps the pairing — that is what lets
// a mid-flight discovery derive state immediately.
func TestWatcher_ColdStartLiveSessionKeepsActivityPairing(t *testing.T) {
	path, db := newTestStore(t)
	insertSession(t, db, sessionRow{id: "live", source: "cli", model: "m", started: 1000, msgs: 1})
	insertMessage(t, db, messageRow{sessionID: "live", role: "user", content: "go", ts: 1001})

	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { return "" }
	ch := w.Subscribe()
	scanNow(w)

	evs := drain(ch)
	if len(evs) != 2 || evs[0].Type != agent.EventNewSession || evs[1].Type != agent.EventActivity {
		t.Fatalf("got %+v, want new_session then activity for a live session", evs)
	}
}

// The watcher's own copy of the turn-boundary rule must agree with the
// parser's. It is the one that decides Terminal on the emitted event, so a
// disagreement would settle metrics while leaving the session state working.
func TestTurnIsDone_AbnormalFinishReasons(t *testing.T) {
	cases := []struct {
		finish string
		want   bool
	}{
		{"stop", true},
		{"length", true},         // model hit max output tokens
		{"content_filter", true}, // model declined to respond
		{"incomplete", true},     // provider-mapped truncation
		{"tool_calls", false},    // still working
		{"", false},              // row still in flight
	}
	for _, tc := range cases {
		path, db := newTestStore(t)
		insertSession(t, db, sessionRow{id: "s1", source: "cli", model: "m", started: 1000, msgs: 2})
		insertMessage(t, db, messageRow{sessionID: "s1", role: "user", content: "go", ts: 1001})
		insertMessage(t, db, messageRow{sessionID: "s1", role: "assistant", content: "x", finish: tc.finish, ts: 1002})
		_ = path

		w := &Watcher{}
		if got := w.turnIsDone(db, "s1"); got != tc.want {
			t.Errorf("turnIsDone(finish=%q) = %v, want %v", tc.finish, got, tc.want)
		}
	}
}
