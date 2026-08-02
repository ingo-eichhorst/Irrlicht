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
	insertSession(t, db, "s1", "tui", "gpt-5.6-luna", "/work/proj", 1000, 0, 1)
	insertMessage(t, db, "s1", "user", "go", "", "", "", 1001)

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
	insertSession(t, db, "s1", "tui", "m", "/work/proj", 1000, 0, 1)
	insertMessage(t, db, "s1", "user", "go", "", "", "", 1001)

	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { return "" }
	ch := w.Subscribe()
	scanNow(w)
	drain(ch)

	// The agent replies and ends its turn.
	insertMessage(t, db, "s1", "assistant", "done", "", "", "stop", 1002)
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
	insertSession(t, db, "s1", "tui", "m", "/work/proj", 1000, 0, 1)
	insertMessage(t, db, "s1", "user", "go", "", "", "", 1001)

	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { return "" }
	ch := w.Subscribe()
	scanNow(w)
	drain(ch)

	insertMessage(t, db, "s1", "assistant", "", realToolCallsJSON, "", "tool_calls", 1002)
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
	insertSession(t, db, "s1", "cli", "m", "", 1000, 0, 2)
	insertMessage(t, db, "s1", "user", "go", "", "", "", 1001)

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
	insertSession(t, db, "s1", "cli", "m", "", 1000, 0, 1)
	insertMessage(t, db, "s1", "user", "go", "", "", "", 1001)

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
	insertSession(t, db, "s1", "cli", "m", "", 1000, 0, 1)
	insertMessage(t, db, "s1", "user", "go", "", "", "", 1001)

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
	insertSession(t, db, "wa", "whatsapp", "m", "/work/proj", 1000, 0, 1)
	insertMessage(t, db, "wa", "user", "hi", "", "", "", 1001)
	insertSession(t, db, "cli1", "cli", "m", "", 1000, 0, 1)
	insertMessage(t, db, "cli1", "user", "go", "", "", "", 1001)

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
	insertSession(t, db, "old", "tui", "m", "/work/proj", now-7200, 0, 1)
	insertMessage(t, db, "old", "user", "go", "", "", "", now-7200)
	insertSession(t, db, "new", "tui", "m", "/work/proj", now-10, 0, 1)
	insertMessage(t, db, "new", "user", "go", "", "", "", now-10)

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
	insertSession(t, db, "child", "cli", "m", "", 1000, 0, 1)
	insertMessage(t, db, "child", "user", "go", "", "", "", 1001)
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
	insertSession(t, db, "s1", "tui", "m", "/work/proj", 1000, 0, 1)
	insertMessage(t, db, "s1", "user", "go", "", "", "", 1001)

	w := NewWithStorePath(path, 0)
	w.liveCWD = func() string { return "" }
	ch := w.Subscribe()

	w.scan() // first scan runs
	if evs := drain(ch); len(evs) != 2 {
		t.Fatalf("first scan emitted %d events, want 2 (new_session + activity)", len(evs))
	}
	// Immediately after, the debounce should suppress the work entirely.
	insertSession(t, db, "s2", "tui", "m", "/work/proj", 1000, 0, 1)
	insertMessage(t, db, "s2", "user", "go", "", "", "", 1001)
	w.scan()
	if evs := drain(ch); len(evs) != 0 {
		t.Errorf("debounced scan emitted %+v, want nothing", evs)
	}
}
