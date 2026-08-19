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

// TestNoMetricReadsAnOutOfWindowTagsCommits is the lock #1553's work bound
// stands on. ComputeDoraMetrics now skips the git call for tags outside
// [from, to], which is safe only because nothing here reads their entry in
// commitsByTag — LeadTime and DetectReverts are its only two consumers and
// both index it through filterRange.
//
// If either grew a read of an out-of-window tag, the service would not get a
// slower answer: it would get a metric computed over commits it never
// fetched, which is a wrong number reported as Available. So this asserts the
// property directly, by computing each metric with the out-of-window entries
// present and absent and requiring the two to agree.
//
// MUTATION EVIDENCE: replacing `filterRange(tags, from, to)` with `tags` in
// DetectReverts's loop makes the DetectReverts arm fail on the unresolved
// count; the same replacement in LeadTime's loop makes the LeadTime arm fail
// on the sample size.
//
// The LeadTime half is why the fixture below looks the way it does, and the
// first draft of it did NOT redden: LeadTime filters COMMITS by author epoch
// as well as TAGS by window, so an out-of-window tag carrying out-of-window
// commits is excluded twice over and the mutation changes nothing. A fixture
// that cannot express the distinguishing state grades a filter that is not the
// one under test — the shape #1475 records. What distinguishes them is a
// release made AFTER the window that ships commits authored DURING it, which
// is an ordinary thing for a repository to contain.
func TestNoMetricReadsAnOutOfWindowTagsCommits(t *testing.T) {
	const day = int64(86400)
	tags := []TagInfo{
		{Name: "v1.0", Epoch: 10 * day}, // before the window
		{Name: "v2.0", Epoch: 40 * day}, // inside it
		{Name: "v3.0", Epoch: 90 * day}, // after it
	}
	from, to := 30*day, 50*day

	// Each out-of-window release carries the shape one consumer looks for:
	// v1.0 a revert (DetectReverts), v3.0 a commit authored inside the window
	// (LeadTime — see the note above about why an out-of-window author epoch
	// there would prove nothing).
	full := map[string][]CommitInfo{
		"v1.0": {
			{Hash: "aaa", AuthorEpoch: 9 * day, Body: "old work\n"},
			{Hash: "bbb", AuthorEpoch: 9 * day, Body: "Revert \"old work\"\n\nThis reverts commit aaa.\n"},
		},
		"v2.0": {
			{Hash: "ccc", AuthorEpoch: 39 * day, Body: "new work\n"},
		},
		"v3.0": {
			{Hash: "ddd", AuthorEpoch: 45 * day, Body: "work shipped after the window\n"},
		},
	}
	pruned := map[string][]CommitInfo{"v2.0": full["v2.0"]}

	// Vacuity guard: without an in-window commit both sides would agree at
	// "nothing", and a filterRange that dropped everything would pass.
	if got := LeadTime(tags, pruned, from, to); !got.Available || got.SampleSize != 1 {
		t.Fatalf("LeadTime over the pruned map = %+v, want one in-window commit; this test is "+
			"not exercising anything", got)
	}

	if withAll, withPruned := LeadTime(tags, full, from, to), LeadTime(tags, pruned, from, to); withAll != withPruned {
		t.Errorf("LeadTime read an out-of-window tag's commits: %+v with them, %+v without. "+
			"ComputeDoraMetrics no longer fetches those (#1553), so this metric would be "+
			"computed over commits that were never read", withAll, withPruned)
	}

	allCand, allUnres := DetectReverts(tags, full, from, to)
	prunedCand, prunedUnres := DetectReverts(tags, pruned, from, to)
	if len(allCand) != len(prunedCand) || allUnres != prunedUnres {
		t.Errorf("DetectReverts read an out-of-window tag's commits: %d candidate(s)/%d unresolved "+
			"with them, %d/%d without", len(allCand), allUnres, len(prunedCand), prunedUnres)
	}
}

// TestInWindowIsTheOneDefinitionFilterRangeUses pins the export #1553 added.
// The service's skip and filterRange must agree on every boundary, and the
// boundary cases are where a second spelling would drift: both ends are
// INCLUSIVE.
func TestInWindowIsTheOneDefinitionFilterRangeUses(t *testing.T) {
	tags := []TagInfo{
		{Name: "before", Epoch: 99},
		{Name: "lower-edge", Epoch: 100},
		{Name: "middle", Epoch: 150},
		{Name: "upper-edge", Epoch: 200},
		{Name: "after", Epoch: 201},
	}
	kept := filterRange(tags, 100, 200)
	if len(kept) != 3 {
		t.Fatalf("filterRange kept %d tags, want 3 (both ends inclusive)", len(kept))
	}
	for _, tag := range tags {
		want := false
		for _, k := range kept {
			if k.Name == tag.Name {
				want = true
			}
		}
		if got := InWindow(tag.Epoch, 100, 200); got != want {
			t.Errorf("InWindow(%d) = %v but filterRange %s it; the service skips git calls on "+
				"InWindow and the metrics read filterRange, so a disagreement is a metric "+
				"computed over commits nobody fetched",
				tag.Epoch, got, map[bool]string{true: "keeps", false: "drops"}[want])
		}
	}
}
