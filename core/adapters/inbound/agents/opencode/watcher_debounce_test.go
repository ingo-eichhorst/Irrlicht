package opencode

import (
	"context"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
)

// TestWatch_AWriteInsideTheDebounceGapIsStillScanned is the red-first test
// for what #2106's final live run measured: an opencode2 TUI's turn ended
// (idle row at 1792958760804) while the dev daemon showed the session working
// until 1792958769.5, the next unrelated write. Watch drops a store write that
// arrives within minScanGap of the last scan, and nothing scanned again
// afterwards, so a turn end written inside that gap waited for some later
// write — for a quiet session, forever. A write inside the gap must still be
// scanned once the gap has passed.
//
// Runs the real Watch loop on a store in a temp dir: the writes reach it
// through fsnotify exactly as opencode's do.
func TestWatch_AWriteInsideTheDebounceGapIsStillScanned(t *testing.T) {
	const gap = time.Second
	w, db := newV2Store(t)
	w.minScanGap = gap
	// opencode's own journal mode: writes land in opencode.db-wal, and the
	// watcher's read connection never blocks this writer. One connection, so
	// the busy timeout holds for every statement.
	db.SetMaxOpenConns(1)
	execAll(t, db, `PRAGMA journal_mode=WAL`, `PRAGMA busy_timeout=5000`)
	loadV2Fixture(t, db, v2StoreRows)
	holdBackAfter(t, db, sesPermission, 4)
	ch := w.Subscribe()
	defer w.Unsubscribe(ch)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = w.Watch(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	// Watch's initial scan first-sights the session; only a write after that
	// is activity.
	start := time.Now()
	for !w.tracks(sesPermission) {
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Fatalf("Watch's initial scan never saw %s (%s)", sesPermission, elapsed)
		}
		time.Sleep(10 * time.Millisecond)
	}

	// The step starts. Until it is admitted, poke the store every 200 ms
	// with a write that changes a row — SQLite wrote no WAL frame, so
	// fsnotify saw no event, for `SET title = title` when this test was
	// built — because Watch arms fsnotify only after its initial scan, and
	// on the base a scan only followed a write landing outside the gap.
	setToRunningTool(t, db, sesPermission)
	start = time.Now()
	for admitted := false; !admitted; {
		for _, ev := range drainNow(ch) {
			admitted = admitted || (ev.Type == agent.EventNewSession && ev.SessionID == sesPermission)
		}
		if admitted {
			break
		}
		if elapsed := time.Since(start); elapsed > 10*time.Second {
			t.Fatalf("the step never admitted the session (%s); the watch loop is not scanning", elapsed)
		}
		execAll(t, db, `UPDATE session_v2 SET resume_attempts = resume_attempts + 1 WHERE id = '`+sesPermission+`'`)
		time.Sleep(200 * time.Millisecond)
	}

	// The scan that admitted it has just run, so this write — the step's
	// end and the idle row, one write and no other after it — lands inside
	// the gap.
	putBack(t, db, sesPermission, "seq > 5")
	ended := time.Now()
	for {
		for _, ev := range drainNow(ch) {
			if ev.Type == agent.EventActivity && ev.SessionID == sesPermission && ev.Terminal {
				return
			}
		}
		if elapsed := time.Since(ended); elapsed > 3*gap {
			t.Fatalf("the turn end written inside the %s debounce gap was never scanned (waited %s)", gap, elapsed)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// tracks reports whether the watcher holds a cursor for sessionID.
func (w *Watcher) tracks(sessionID string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, ok := w.cursors[sessionID]
	return ok
}
