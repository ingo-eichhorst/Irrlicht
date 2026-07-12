package dora

import (
	"math"
	"testing"
)

func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func tagsFixture() []TagInfo {
	// One tag per week, plus a hotfix (v1.2) 2 hours after v1.1.
	return []TagInfo{
		{Name: "v1.0", Epoch: 0},
		{Name: "v1.1", Epoch: 7 * 86400},
		{Name: "v1.2", Epoch: 7*86400 + 2*3600}, // 2h after v1.1 — hotfix
		{Name: "v1.3", Epoch: 14 * 86400},
	}
}

func TestDeploymentFrequency(t *testing.T) {
	tags := tagsFixture()

	t.Run("zero releases in range", func(t *testing.T) {
		m := DeploymentFrequency(tags, 100*86400, 200*86400)
		if m.Available {
			t.Fatalf("expected unavailable, got %+v", m)
		}
	})

	t.Run("one release in range", func(t *testing.T) {
		m := DeploymentFrequency(tags, 0, 0)
		if m.Available || m.SampleSize != 1 {
			t.Fatalf("expected unavailable with SampleSize=1, got %+v", m)
		}
	})

	t.Run("computes a rate over the full range", func(t *testing.T) {
		m := DeploymentFrequency(tags, 0, 14*86400)
		if !m.Available {
			t.Fatalf("expected available, got %+v", m)
		}
		if m.SampleSize != 4 {
			t.Fatalf("SampleSize = %d, want 4", m.SampleSize)
		}
		// span = 14 days = 2 weeks; 4 releases / 2 weeks = 2/week.
		if !almostEqual(m.Value, 2.0) {
			t.Fatalf("Value = %v, want 2.0", m.Value)
		}
	})

	t.Run("zero time span is unavailable, not a divide-by-zero", func(t *testing.T) {
		same := []TagInfo{{Name: "a", Epoch: 100}, {Name: "b", Epoch: 100}}
		m := DeploymentFrequency(same, 0, 200)
		if m.Available {
			t.Fatalf("expected unavailable for zero-span releases, got %+v", m)
		}
	})
}

func TestLeadTime(t *testing.T) {
	tags := tagsFixture()

	t.Run("no releases in range", func(t *testing.T) {
		m := LeadTime(tags, nil, 100*86400, 200*86400)
		if m.Available {
			t.Fatalf("expected unavailable, got %+v", m)
		}
	})

	t.Run("no commits recorded for in-range releases", func(t *testing.T) {
		m := LeadTime(tags, map[string][]CommitInfo{}, 0, 14*86400)
		if m.Available {
			t.Fatalf("expected unavailable, got %+v", m)
		}
	})

	t.Run("median lead time across commits, filtered by author epoch", func(t *testing.T) {
		commitsByTag := map[string][]CommitInfo{
			"v1.1": {
				{Hash: "a", AuthorEpoch: 7*86400 - 3600},   // 1h lead
				{Hash: "b", AuthorEpoch: 7*86400 - 7*3600}, // 7h lead
			},
			"v1.3": {
				{Hash: "c", AuthorEpoch: 14*86400 - 1000*86400}, // outside [from,to] — excluded
			},
		}
		m := LeadTime(tags, commitsByTag, 0, 14*86400)
		if !m.Available {
			t.Fatalf("expected available, got %+v", m)
		}
		if m.SampleSize != 2 {
			t.Fatalf("SampleSize = %d, want 2 (the out-of-range commit must be excluded)", m.SampleSize)
		}
		if !almostEqual(m.Value, 4.0) {
			t.Fatalf("median Value = %v, want 4.0 (median of 1h,7h)", m.Value)
		}
	})
}

func TestDetectHotfixes(t *testing.T) {
	tags := tagsFixture()

	t.Run("flags a release within the window, not others", func(t *testing.T) {
		out := DetectHotfixes(tags, 24, 0, 14*86400)
		if len(out) != 1 {
			t.Fatalf("got %d hotfixes, want 1: %+v", len(out), out)
		}
		if out[0].FixTag != "v1.2" {
			t.Fatalf("FixTag = %q, want v1.2", out[0].FixTag)
		}
		if !almostEqual(out[0].RestoreHours, 2.0) {
			t.Fatalf("RestoreHours = %v, want 2.0", out[0].RestoreHours)
		}
	})

	t.Run("zero window flags nothing", func(t *testing.T) {
		out := DetectHotfixes(tags, 0, 0, 14*86400)
		if len(out) != 0 {
			t.Fatalf("got %d hotfixes with a zero window, want 0: %+v", len(out), out)
		}
	})

	t.Run("first tag in history is never a hotfix (no predecessor)", func(t *testing.T) {
		out := DetectHotfixes(tags, 999999, 0, 0)
		if len(out) != 0 {
			t.Fatalf("got %d hotfixes for the very first tag, want 0: %+v", len(out), out)
		}
	})
}

func TestDetectReverts(t *testing.T) {
	tags := tagsFixture()

	t.Run("resolves a standard revert trailer", func(t *testing.T) {
		commitsByTag := map[string][]CommitInfo{
			"v1.3": {
				{Hash: "r1", AuthorEpoch: 14 * 86400, Body: "Revert \"feat: risky\"\n\nThis reverts commit abc1234def.\n"},
			},
		}
		candidates, unresolved := DetectReverts(tags, commitsByTag, 0, 14*86400)
		if unresolved != 0 {
			t.Fatalf("unresolved = %d, want 0", unresolved)
		}
		if len(candidates) != 1 || candidates[0].OriginalHash != "abc1234def" || candidates[0].RevertTag != "v1.3" {
			t.Fatalf("candidates = %+v", candidates)
		}
	})

	t.Run("case-insensitive subject match", func(t *testing.T) {
		commitsByTag := map[string][]CommitInfo{
			"v1.3": {{Hash: "r1", Body: "revert: restore old flow\n\nThis reverts commit abc1234def.\n"}},
		}
		candidates, _ := DetectReverts(tags, commitsByTag, 0, 14*86400)
		if len(candidates) != 1 {
			t.Fatalf("expected a lowercase-subject revert to match, got %+v", candidates)
		}
	})

	t.Run("non-standard revert with no trailer counts as unresolved, not dropped silently", func(t *testing.T) {
		commitsByTag := map[string][]CommitInfo{
			"v1.3": {{Hash: "r1", Body: "revert: restore subtitle flow\n\nCo-authored-by: x\n"}},
		}
		candidates, unresolved := DetectReverts(tags, commitsByTag, 0, 14*86400)
		if len(candidates) != 0 || unresolved != 1 {
			t.Fatalf("candidates=%+v unresolved=%d, want 0 candidates and unresolved=1", candidates, unresolved)
		}
	})

	t.Run("non-revert commits are ignored", func(t *testing.T) {
		commitsByTag := map[string][]CommitInfo{
			"v1.3": {{Hash: "c1", Body: "feat: add widget\n"}},
		}
		candidates, unresolved := DetectReverts(tags, commitsByTag, 0, 14*86400)
		if len(candidates) != 0 || unresolved != 0 {
			t.Fatalf("candidates=%+v unresolved=%d, want none", candidates, unresolved)
		}
	})
}

func TestResolveRevert(t *testing.T) {
	tags := tagsFixture()

	t.Run("resolves across releases", func(t *testing.T) {
		f, ok := ResolveRevert(tags, RevertCandidate{RevertTag: "v1.3", OriginalHash: "x"}, "v1.1")
		if !ok {
			t.Fatal("expected ok=true")
		}
		if f.FixTag != "v1.3" {
			t.Fatalf("FixTag = %q, want v1.3", f.FixTag)
		}
		// v1.3 epoch - v1.1 epoch = 7 days = 168h.
		if !almostEqual(f.RestoreHours, 168.0) {
			t.Fatalf("RestoreHours = %v, want 168.0", f.RestoreHours)
		}
	})

	t.Run("unresolvable original (never released) is skipped", func(t *testing.T) {
		_, ok := ResolveRevert(tags, RevertCandidate{RevertTag: "v1.3", OriginalHash: "x"}, "")
		if ok {
			t.Fatal("expected ok=false for an empty originalTag")
		}
	})

	t.Run("original fixed within the same release is skipped", func(t *testing.T) {
		_, ok := ResolveRevert(tags, RevertCandidate{RevertTag: "v1.3", OriginalHash: "x"}, "v1.3")
		if ok {
			t.Fatal("expected ok=false when original and revert share a release")
		}
	})

	t.Run("unknown tag name is skipped, not a panic", func(t *testing.T) {
		_, ok := ResolveRevert(tags, RevertCandidate{RevertTag: "vX", OriginalHash: "x"}, "v1.1")
		if ok {
			t.Fatal("expected ok=false for an unknown RevertTag")
		}
	})
}

func TestChangeFailureRate(t *testing.T) {
	tags := tagsFixture()

	t.Run("no releases in range", func(t *testing.T) {
		m := ChangeFailureRate(tags, nil, 100*86400, 200*86400)
		if m.Available {
			t.Fatalf("expected unavailable, got %+v", m)
		}
	})

	t.Run("dedupes a release flagged by multiple signals", func(t *testing.T) {
		failures := []ResolvedFailure{
			{FixTag: "v1.2", RestoreHours: 2},
			{FixTag: "v1.2", RestoreHours: 5}, // same release, second signal
			{FixTag: "v1.3", RestoreHours: 10},
		}
		m := ChangeFailureRate(tags, failures, 0, 14*86400)
		if !m.Available {
			t.Fatalf("expected available, got %+v", m)
		}
		if m.SampleSize != 4 {
			t.Fatalf("SampleSize = %d, want 4 (releases in range)", m.SampleSize)
		}
		// 2 unique releases (v1.2, v1.3) out of 4 = 50%.
		if !almostEqual(m.Value, 50.0) {
			t.Fatalf("Value = %v, want 50.0", m.Value)
		}
	})
}

func TestMTTR(t *testing.T) {
	t.Run("no failures", func(t *testing.T) {
		m := MTTR(nil)
		if m.Available {
			t.Fatalf("expected unavailable, got %+v", m)
		}
	})

	t.Run("median across all flagged instances, not deduped by release", func(t *testing.T) {
		failures := []ResolvedFailure{
			{FixTag: "v1.2", RestoreHours: 2},
			{FixTag: "v1.2", RestoreHours: 8}, // same release, second instance — both count
			{FixTag: "v1.3", RestoreHours: 5},
		}
		m := MTTR(failures)
		if !m.Available || m.SampleSize != 3 {
			t.Fatalf("got %+v, want Available with SampleSize=3", m)
		}
		if !almostEqual(m.Value, 5.0) {
			t.Fatalf("median Value = %v, want 5.0", m.Value)
		}
	})
}
