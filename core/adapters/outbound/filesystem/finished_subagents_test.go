package filesystem

import (
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

// TestFinishedSubagentStore_SurvivesARestart: a record written by one store is
// served by a fresh store over the same directory — the daemon-restart case.
func TestFinishedSubagentStore_SurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	a := NewFinishedSubagentStoreWithDir(dir)
	a.Retain(finished("child-1", "root", now, 10))
	a.Retain(finished("child-2", "root", now.Add(time.Second), 20))
	a.Retain(finished("child-1", "root", now.Add(2*time.Second), 11)) // replaces

	got := NewFinishedSubagentStoreWithDir(dir).List()
	if len(got) != 2 || got[0].SessionID != "child-2" || got[1].SessionID != "child-1" {
		t.Fatalf("after restart: %v, want [child-2 child-1] (oldest removal first)", ids(got))
	}
	if got[1].Usage == nil || got[1].Usage.CumInputTokens != 11 || got[1].Usage.PricingSource != "litellm@2026-11-20" {
		t.Errorf("child-1 usage after restart = %+v, want the replacing record's 11 tokens and its source", got[1].Usage)
	}
}

// TestFinishedSubagentStore_ForgetTreeDropsEveryDescendant: removing a root
// drops its children's records and theirs, and nothing else.
func TestFinishedSubagentStore_ForgetTreeDropsEveryDescendant(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	s := NewFinishedSubagentStoreWithDir(dir)
	s.Retain(finished("grandchild", "child", now, 1))
	s.Retain(finished("child", "root", now, 1))
	s.Retain(finished("other-child", "other-root", now, 1))
	s.ForgetTree("root")

	if got := ids(s.List()); len(got) != 1 || got[0] != "other-child" {
		t.Errorf("after ForgetTree(root): %v, want [other-child]", got)
	}
	if got := ids(NewFinishedSubagentStoreWithDir(dir).List()); len(got) != 1 {
		t.Errorf("ForgetTree was not persisted: %v", got)
	}
}

// TestFinishedSubagentStore_RetentionBoundsGrowth: a record past the window is
// dropped — at load, and on the next Retain — so records whose root was
// removed without ForgetTree cannot accumulate.
func TestFinishedSubagentStore_RetentionBoundsGrowth(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	s := NewFinishedSubagentStoreWithDir(dir)
	s.Retain(finished("old", "gone-root", now.Add(-FinishedSubagentRetention-time.Hour), 1))
	s.Retain(finished("fresh", "root", now, 1))
	if got := ids(s.List()); len(got) != 1 || got[0] != "fresh" {
		t.Errorf("after Retain: %v, want [fresh]", got)
	}

	s.now = func() time.Time { return now.Add(FinishedSubagentRetention + time.Hour) }
	s.Retain(finished("later", "root", s.now(), 1))
	if got := ids(s.List()); len(got) != 1 || got[0] != "later" {
		t.Errorf("after the window passed: %v, want [later]", got)
	}
}

// TestFinishedSubagentStore_UnreadableFileStartsEmpty: a corrupt file is not
// an error — the store starts empty and the next Retain rewrites it.
func TestFinishedSubagentStore_UnreadableFileStartsEmpty(t *testing.T) {
	dir := t.TempDir()
	s := NewFinishedSubagentStoreWithDir(dir)
	if err := writeFileAtomic(s.path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s = NewFinishedSubagentStoreWithDir(dir)
	if len(s.List()) != 0 {
		t.Fatalf("corrupt file: %v, want empty", ids(s.List()))
	}
	s.Retain(finished("child", "root", time.Now(), 1))
	if got := ids(NewFinishedSubagentStoreWithDir(dir).List()); len(got) != 1 {
		t.Errorf("rewrite after corruption: %v, want [child]", got)
	}
	s.Retain(session.FinishedSubagent{}) // no id: ignored
	if len(s.List()) != 1 {
		t.Errorf("a record with no session id was kept")
	}
}
