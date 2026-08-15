package filesystem

import (
	"testing"
	"time"
)

// #1551 review findings 5 and 6: resolveProject's non-answer branch had no
// coverage at all — neutralising it (`if false && !answered`) left the package
// green, because the only double in it never reported a non-answer.
//
// What the branch must do is narrow: not poison the permanent cache with a
// guess (#1046/#1049), and not re-probe per recording EVENT either, which is
// what #1543's first draft did. The doubles are this package's existing
// countingResolver, which grew one `unread` knob rather than a second type.

// TestResolveProjectDoesNotCacheANonAnswerPermanently pins both halves.
//
// RED-FIRST, measured by reverting each half separately:
//
//   - Writing the guess into resolveCache (the pre-#1543 behaviour): the
//     "re-probed after the TTL" arm fails with 1 call, because the permanent
//     cache answers forever and a worktree cwd keeps #1046's wrong name.
//   - Dropping the negative cache entirely (#1543's first draft): the "not
//     re-probed within the TTL" arm fails with 3 calls — one per recording
//     event, each up to the git adapter's 5s ceiling, under a held mutex.
func TestResolveProjectDoesNotCacheANonAnswerPermanently(t *testing.T) {
	now := time.Now()
	git := &countingResolver{unread: true}
	tr := NewConcurrencyTrackerWithDir(t.TempDir(), git)
	tr.clock = func() time.Time { return now }

	const cwd = "/repo/.claude/worktrees/1543-slug"

	// Three events for the same cwd inside one request.
	for i := 0; i < 3; i++ {
		if got := tr.resolveProject(cwd); got != "1543-slug" {
			t.Fatalf("resolveProject = %q, want the basename fallback", got)
		}
	}
	if n := git.Calls(cwd); n != 1 {
		t.Errorf("probed %d times for 3 events on one cwd; a non-answer must be reused within "+
			"the TTL, or every recording line costs a bounded-but-real git shellout under a "+
			"held mutex (#1551 finding 5)", n)
	}

	// The guess must NOT have entered the permanent cache.
	tr.resolveMu.Lock()
	_, cached := tr.resolveCache[cwd]
	tr.resolveMu.Unlock()
	if cached {
		t.Error("a basename guess from a git that never ran was written to the lifetime cache; " +
			"for a worktree cwd that is exactly the name #1046 exists to prevent")
	}

	// Past the TTL the probe is retried, and a now-working git wins.
	now = now.Add(unresolvedTTL + time.Second)
	git.mu.Lock()
	git.unread = false
	git.mu.Unlock()

	if got := tr.resolveProject(cwd); got != "1543-slug" {
		t.Errorf("after the TTL, resolveProject = %q", got)
	}
	if n := git.Calls(cwd); n != 2 {
		t.Errorf("probed %d times, want 2 (once inside the TTL, once after) — the non-answer "+
			"was cached permanently", n)
	}
	// And now that it answered, it is permanent.
	tr.resolveMu.Lock()
	_, cached = tr.resolveCache[cwd]
	tr.resolveMu.Unlock()
	if !cached {
		t.Error("a resolution that finally answered was not written to the lifetime cache")
	}
}

// TestResolveProjectStillCachesAnAnswerForever is the vacuity guard: without
// it, a tracker that cached nothing at all would pass every arm above, and
// #1049's whole point — one shellout per distinct cwd, not per event — would
// be silently undone.
func TestResolveProjectStillCachesAnAnswerForever(t *testing.T) {
	now := time.Now()
	git := &countingResolver{}
	tr := NewConcurrencyTrackerWithDir(t.TempDir(), git)
	tr.clock = func() time.Time { return now }

	const cwd = "/repo/sub"
	for i := 0; i < 3; i++ {
		if got := tr.resolveProject(cwd); got != "sub" {
			t.Fatalf("resolveProject = %q", got)
		}
	}
	now = now.Add(10 * unresolvedTTL)
	if got := tr.resolveProject(cwd); got != "sub" {
		t.Fatalf("after a long wait, resolveProject = %q", got)
	}
	if n := git.Calls(cwd); n != 1 {
		t.Errorf("probed %d times for a cwd that resolved; a resolution is permanent (#1049)", n)
	}
}
