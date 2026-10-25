package opencode

import (
	"testing"

	"irrlicht/core/domain/agent"
)

// These are #2106's watcher tests: an opencode 2.x store is read from
// session_v2 / session_message, and a session is admitted on its activity
// while a live client owns its directory.

// TestV2Reader_AdmitsLiveSessionsOnTheirTurn is the red-first test for
// admission. Every fixture session exists before its turn lands, as an
// opencode 2.x session row does; the turn then arrives. Each session in the
// client's directory must be admitted — the subagent child with its parent's
// id — and its activity must end the turn, because each turn ends in an idle
// row. On main the 2.x store was read by nothing.
func TestV2Reader_AdmitsLiveSessionsOnTheirTurn(t *testing.T) {
	w, db := newV2Store(t)
	loadFixtureRows(t, db, v2StoreRows, insertSessions)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	w.scanSessions()
	if evs := drainNow(ch); len(evs) != 0 {
		t.Fatalf("sessions with no activity yet produced %d event(s), want 0: %+v", len(evs), evs)
	}

	loadFixtureRows(t, db, v2StoreRows, insertMessages)
	w.scanSessions()
	evs := drainNow(ch)

	admitted := eventsOfType(evs, agent.EventNewSession)
	activity := eventsOfType(evs, agent.EventActivity)
	for _, id := range []string{sesToolTurn, sesInterrupted, sesParent, sesChild, sesFailed, sesPermission} {
		if n := len(admitted[id]); n != 1 {
			t.Errorf("%s: %d EventNewSession, want 1", id, n)
			continue
		}
		ev := admitted[id][0]
		if ev.CWD != fixtureDir || ev.TranscriptPath != w.transcriptPathFor(id) {
			t.Errorf("%s: admitted with CWD %q, TranscriptPath %q; want %q, %q",
				id, ev.CWD, ev.TranscriptPath, fixtureDir, w.transcriptPathFor(id))
		}
		if n := len(activity[id]); n != 1 || !activity[id][0].Terminal {
			t.Errorf("%s: activity %+v, want exactly one Terminal EventActivity (the turn ends in an idle row)", id, activity[id])
		}
	}
	if got := admitted[sesChild]; len(got) == 1 && got[0].ParentSessionID != sesParent {
		t.Errorf("subagent child admitted with ParentSessionID %q, want %q", got[0].ParentSessionID, sesParent)
	}
	if got := admitted[sesParent]; len(got) == 1 && got[0].ParentSessionID != "" {
		t.Errorf("parent admitted with ParentSessionID %q, want none", got[0].ParentSessionID)
	}
}

// TestV2Reader_AnAssistantRowUpdatedInPlaceIsActivity is the red-first test
// for the cursor. opencode 2.x streams a whole step into ONE assistant row
// and rewrites it in place — same id, same seq, a later time_updated. Measured
// on the permission fixture session: its seq-5 row moved 1792951697767 →
// 1792951699890 (shell call running, permission held) → 1792951703489
// (completed). A cursor on seq would see that row once and miss the tool
// finishing; the (time_updated, id) cursor must report the rewrite.
func TestV2Reader_AnAssistantRowUpdatedInPlaceIsActivity(t *testing.T) {
	w, db := newV2Store(t)
	loadV2Fixture(t, db, v2StoreRows)
	holdBackAfter(t, db, sesPermission, 4) // the store just after the prompt
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	w.scanSessions() // first sight of every session: nothing it already held is activity
	if evs := drainNow(ch); len(evs) != 0 {
		t.Fatalf("first sight produced %d event(s), want 0: %+v", len(evs), evs)
	}

	setToRunningTool(t, db, sesPermission)
	w.scanSessions()
	evs := drainNow(ch)
	if n := len(eventsOfType(evs, agent.EventNewSession)[sesPermission]); n != 1 {
		t.Fatalf("the step starting produced %d EventNewSession, want 1: %+v", n, evs)
	}
	if act := eventsOfType(evs, agent.EventActivity)[sesPermission]; len(act) != 1 || act[0].Terminal {
		t.Fatalf("the step starting produced activity %+v, want one non-Terminal EventActivity", act)
	}

	execAll(t, db, `UPDATE session_message
		SET data = (SELECT data FROM held WHERE session_id = '`+sesPermission+`' AND seq = 5),
		    time_updated = (SELECT time_updated FROM held WHERE session_id = '`+sesPermission+`' AND seq = 5)
		WHERE session_id = '`+sesPermission+`' AND seq = 5`)
	w.scanSessions()
	evs = drainNow(ch)
	if act := eventsOfType(evs, agent.EventActivity)[sesPermission]; len(act) != 1 || act[0].Terminal {
		t.Errorf("the in-place rewrite of seq 5 produced %d activity event(s) %+v, want one non-Terminal EventActivity",
			len(act), act)
	}
	if n := len(eventsOfType(evs, agent.EventNewSession)); n != 0 {
		t.Errorf("an admitted session was admitted again (%d EventNewSession)", n)
	}

	putBack(t, db, sesPermission, "seq > 5")
	w.scanSessions()
	if act := eventsOfType(drainNow(ch), agent.EventActivity)[sesPermission]; len(act) != 1 || !act[0].Terminal {
		t.Errorf("the turn's last step and idle row produced activity %+v, want one Terminal EventActivity", act)
	}
}

// TestV2Reader_ImportedV1RowWaitsForNewActivity is the red-first test for the
// rows opencode 2.x's in-place migration imports: version 1.19.4, no idle row,
// time_idle NULL. Neither that row nor a 2.x session already in the store is
// admitted on the state it held when the watcher first saw it — with a live
// client in its directory — and the imported row IS admitted, once, when 2.x
// writes new activity to it.
func TestV2Reader_ImportedV1RowWaitsForNewActivity(t *testing.T) {
	w, db := newV2Store(t)
	loadV2Fixture(t, db, v2MigratedRows)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	w.scanSessions()
	w.scanSessions()
	if evs := drainNow(ch); len(evs) != 0 {
		t.Fatalf("rows already in the migrated store produced %d event(s), want 0: %+v", len(evs), evs)
	}

	execAll(t, db, `INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data)
		VALUES ('msg_fixture_resumed', '`+sesImported+`', 'user', 2, 1792931700000, 1792931700000,
		        '{"time":{"created":1792931700000},"text":"Continue."}')`)
	w.scanSessions()
	w.scanSessions()
	evs := drainNow(ch)
	admitted := eventsOfType(evs, agent.EventNewSession)
	if n := len(admitted[sesImported]); n != 1 {
		t.Errorf("the imported row got %d EventNewSession after new activity, want exactly 1: %+v", n, evs)
	}
	if n := len(admitted[sesMigrated]); n != 0 {
		t.Errorf("the untouched 2.x session got %d EventNewSession, want 0", n)
	}
}

// TestV2Reader_RemovesArchivedAndDeletedSessions: a tracked 2.x session whose
// row is archived or deleted is removed — the 2.x counterpart of
// emitRemovedForArchivedSessions, which reads the 1.x table. A suspended one
// is not: see TestV2Reader_ASuspendedSessionIsStillRunning.
func TestV2Reader_RemovesArchivedAndDeletedSessions(t *testing.T) {
	w, db := newV2Store(t)
	loadFixtureRows(t, db, v2StoreRows, insertSessions)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)
	w.scanSessions()
	loadFixtureRows(t, db, v2StoreRows, insertMessages)
	w.scanSessions()
	if n := len(eventsOfType(drainNow(ch), agent.EventNewSession)); n != 6 {
		t.Fatalf("admitted %d sessions, want all 6 fixture sessions", n)
	}

	execAll(t, db,
		`UPDATE session_v2 SET time_archived = 1792960000000 WHERE id = '`+sesToolTurn+`'`,
		`UPDATE session_v2 SET time_suspended = 1792960000000 WHERE id = '`+sesInterrupted+`'`,
		`DELETE FROM session_message WHERE session_id = '`+sesFailed+`'`,
		`DELETE FROM session_v2 WHERE id = '`+sesFailed+`'`,
	)
	w.scanSessions()
	removed := eventsOfType(drainNow(ch), agent.EventRemoved)
	for _, id := range []string{sesToolTurn, sesFailed} {
		if n := len(removed[id]); n != 1 {
			t.Errorf("%s: %d EventRemoved, want 1", id, n)
		}
	}
	if n := len(removed[sesInterrupted]); n != 0 {
		t.Errorf("the suspended session got %d EventRemoved, want 0 — suspended is not closed", n)
	}
	if len(removed) != 2 {
		t.Errorf("EventRemoved for %d session(s), want 2: %v", len(removed), removed)
	}
}

// TestV2Reader_NoClientNoAdmission: activity in a directory no live client
// owns admits nothing — the 2.x service runs every session it hosts, so its
// own store writes must not stand in for a client (#2106; client-less
// sessions are P3's).
func TestV2Reader_NoClientNoAdmission(t *testing.T) {
	w, db := newV2Store(t)
	w.liveCWDs = func() map[string]struct{} { return map[string]struct{}{"/Users/x/elsewhere": {}} }
	loadFixtureRows(t, db, v2StoreRows, insertSessions)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)
	w.scanSessions()
	loadFixtureRows(t, db, v2StoreRows, insertMessages)
	w.scanSessions()
	if evs := drainNow(ch); len(evs) != 0 {
		t.Errorf("activity with no client in its directory produced %d event(s), want 0: %+v", len(evs), evs)
	}
}

// TestV2Reader_ASuspendedSessionIsStillRunning is the red-first test for what
// the live TUI run found (#2106). opencode 2.x sets session_v2.time_suspended
// on a TUI's session within milliseconds of creating it, holds it through the
// turn, and clears it at idle — sampled at 30 ms against the sandbox store
// (scratchpad fleet-2106-dbsampler2.py): created 1792954501603, suspended
// 1792954501611 from the next sample on, NULL again at the idle
// (1792954508949). A reader that drops suspended rows loses every TUI session
// for exactly the span it is working, then first-sights it whole after idle
// and never admits it — which is what the dev daemon did.
func TestV2Reader_ASuspendedSessionIsStillRunning(t *testing.T) {
	w, db := newV2Store(t)
	loadV2Fixture(t, db, v2StoreRows)
	holdBackAfter(t, db, sesPermission, 4)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)
	w.scanSessions()
	drainNow(ch)

	execAll(t, db, `UPDATE session_v2 SET time_suspended = 1792951696600 WHERE id = '`+sesPermission+`'`)
	setToRunningTool(t, db, sesPermission)
	w.scanSessions()
	evs := drainNow(ch)
	if n := len(eventsOfType(evs, agent.EventNewSession)[sesPermission]); n != 1 {
		t.Errorf("a suspended session's step produced %d EventNewSession, want 1: %+v", n, evs)
	}
	if n := len(eventsOfType(evs, agent.EventRemoved)); n != 0 {
		t.Errorf("a suspended session was removed (%d EventRemoved)", n)
	}

	execAll(t, db, `UPDATE session_v2 SET time_suspended = NULL WHERE id = '`+sesPermission+`'`)
	putBack(t, db, sesPermission, "seq > 5")
	w.scanSessions()
	if act := eventsOfType(drainNow(ch), agent.EventActivity)[sesPermission]; len(act) != 1 || !act[0].Terminal {
		t.Errorf("the turn's end after the suspension lifted produced activity %+v, want one Terminal EventActivity", act)
	}
}

// TestV2Reader_AFailedFirstSightDoesNotSeedHistoryAsActivity pins the cursor
// cleanup scanV2Session does when a session's messages cannot be read on its
// first sighting (#2106 review). A cursor left registered but unseeded would
// make the next scan treat the session as known and read its whole stored
// history as activity — admitting it on exactly the state first sight exists
// to ignore. The read is made to fail with a row whose time_updated is text,
// which SQLite stores as given and rows.Scan cannot put in an int64.
func TestV2Reader_AFailedFirstSightDoesNotSeedHistoryAsActivity(t *testing.T) {
	w, db := newV2Store(t)
	loadV2Fixture(t, db, v2StoreRows)
	execAll(t, db, `INSERT INTO session_message (id, session_id, type, seq, time_created, time_updated, data)
		VALUES ('msg_unreadable', '`+sesToolTurn+`', 'user', 99, 1, 'x', '{}')`)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	w.scanSessions() // sesToolTurn's first sight fails to read
	execAll(t, db, `DELETE FROM session_message WHERE id = 'msg_unreadable'`)
	w.scanSessions() // and is first-sighted again here

	if evs := drainNow(ch); len(evs) != 0 {
		t.Errorf("stored history produced %d event(s) after a failed first sight, want 0: %+v", len(evs), evs)
	}
}
