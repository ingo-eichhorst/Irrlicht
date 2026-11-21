package filesystem

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

func finished(id, parent string, at time.Time, input int64) session.FinishedSubagent {
	return session.FinishedSubagent{
		SessionID: id, ParentSessionID: parent, Adapter: "claude-code", FinishedAt: at.Unix(),
		Usage: &session.SessionMetrics{CumInputTokens: input, PricingSource: "litellm@2026-11-20"},
	}
}

func ids(recs []session.FinishedSubagent) []string {
	out := make([]string, len(recs))
	for i, r := range recs {
		out[i] = r.SessionID
	}
	return out
}

func mustRetain(t *testing.T, s *FinishedSubagentStore, rec session.FinishedSubagent) {
	t.Helper()
	if err := s.Retain(rec); err != nil {
		t.Fatal(err)
	}
}

// TestFinishedSubagentStore_SurvivesARestart: a record written by one store is
// served by a fresh store over the same directory — the daemon-restart case —
// and a second record for the same session replaces the first.
func TestFinishedSubagentStore_SurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	a := NewFinishedSubagentStoreWithDir(dir)
	mustRetain(t, a, finished("child-1", "root", now, 10))
	mustRetain(t, a, finished("child-2", "root", now.Add(time.Second), 20))
	mustRetain(t, a, finished("child-1", "root", now.Add(2*time.Second), 11))

	got := NewFinishedSubagentStoreWithDir(dir).List()
	if !slices.Equal(ids(got), []string{"child-2", "child-1"}) {
		t.Fatalf("after restart: %v, want [child-2 child-1] (oldest removal first)", ids(got))
	}
	if !isReplacingUsage(got[1].Usage) {
		t.Errorf("child-1 usage after restart = %+v, want the replacing record's 11 tokens and its source", got[1].Usage)
	}
}

// isReplacingUsage reports the replacing child-1 record's 11 tokens and its
// pricing source.
func isReplacingUsage(u *session.SessionMetrics) bool {
	return u != nil && u.CumInputTokens == 11 && u.PricingSource == "litellm@2026-11-20"
}

// TestFinishedSubagentStore_OneFilePerRoot: a grandchild whose parent is
// itself a record lands in its root's file, so a removal rewrites only the
// file of the root it belongs to.
func TestFinishedSubagentStore_OneFilePerRoot(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	s := NewFinishedSubagentStoreWithDir(dir)
	mustRetain(t, s, finished("child", "root-a", now, 1))
	mustRetain(t, s, finished("grandchild", "child", now, 1))
	mustRetain(t, s, finished("other", "root-b", now, 1))

	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 2 {
		t.Fatalf("files = %v, want one per root (2)", files)
	}
	if got := s.roots["root-a"]; got == nil || len(got.Records) != 2 {
		t.Errorf("root-a holds %+v, want child and grandchild", got)
	}
}

// TestFinishedSubagentStore_PruneOrphansKeepsLiveRootsForever: a root that
// exists keeps its records however long it runs; a root that is gone loses
// them only after the retention window; a root that comes back stops its
// clock.
func TestFinishedSubagentStore_PruneOrphansKeepsLiveRootsForever(t *testing.T) {
	dir := t.TempDir()
	start := time.Now()
	s := NewFinishedSubagentStoreWithDir(dir)
	mustRetain(t, s, finished("live-child", "live-root", start, 1))
	mustRetain(t, s, finished("gone-child", "gone-root", start, 1))
	mustRetain(t, s, finished("back-child", "back-root", start, 1))
	present := map[string]bool{"live-root": true}
	exists := func(id string) bool { return present[id] }

	at := func(d time.Duration) {
		t.Helper()
		s.now = func() time.Time { return start.Add(d) }
		if err := s.PruneOrphans(exists); err != nil {
			t.Fatal(err)
		}
	}
	at(0) // gone-root and back-root start their clocks
	present["back-root"] = true
	at(time.Hour) // back-root is back: its clock stops
	delete(present, "back-root")
	at(FinishedSubagentRetention) // back-root restarts at the window: not yet expired
	at(FinishedSubagentRetention + time.Hour)

	got := ids(NewFinishedSubagentStoreWithDir(dir).List())
	want := map[string]bool{"live-child": true, "back-child": true}
	if len(got) != len(want) {
		t.Fatalf("after the window: %v, want live-child (its root exists) and back-child (absent less than the window)", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected record %s kept", id)
		}
	}
}

// TestFinishedSubagentStore_UnreadableFileIsSkipped: a corrupt file or one
// written in another version is skipped on load, not served and not deleted.
func TestFinishedSubagentStore_UnreadableFileIsSkipped(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"corrupt.json": "{not json",
		"future.json":  `{"version":99,"root":"r","records":[{"session_id":"x","parent_session_id":"r","finished_at":1}]}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := NewFinishedSubagentStoreWithDir(dir)
	if got := s.List(); len(got) != 0 {
		t.Errorf("loaded %v from unreadable files, want none", ids(got))
	}
	if _, err := os.Stat(filepath.Join(dir, "future.json")); err != nil {
		t.Errorf("a file in another version was removed: %v", err)
	}
	mustRetain(t, s, session.FinishedSubagent{}) // no id: ignored, no error
	if len(s.List()) != 0 {
		t.Error("a record with no session id was kept")
	}
}

// TestFinishedSubagentStore_WriteFailureIsReported: Retain returns the write
// error instead of dropping it, and the record is still served from memory.
func TestFinishedSubagentStore_WriteFailureIsReported(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	s := NewFinishedSubagentStoreWithDir(filepath.Join(blocker, "store"))
	if err := s.Retain(finished("child", "root", time.Now(), 1)); err == nil {
		t.Error("Retain into an unwritable directory returned no error")
	}
	if len(s.List()) != 1 {
		t.Error("the record was not kept in memory after the failed write")
	}
}
