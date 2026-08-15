//go:build darwin

package processlifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"irrlicht/core/domain/session"
)

// This file covers #1544's two memos on the platform that actually shells out:
// the process-global bundle-id cache (bundleIDMemo, osutil_darwin.go) and the
// per-resolve read memo where both ancestry walks share it.
//
// The two have OPPOSITE lifetimes and that is the whole design — see each
// type's doc comment. The tests that pin the difference are
// TestBundleIDMemoNeverStoresANonAnswer here (the admission rule the family
// keeps getting wrong) and TestAncestryReadsAreScopedToOneResolveNotTheProcess
// in ancestryreads_test.go (the lifetime).

// fakeAppBundle builds a real, plutil-readable `.app` under t.TempDir() and
// returns its path. Real rather than faked because the tests below that lock
// the PRODUCTION wiring have to run the production probe, and no top-level app
// that is guaranteed to exist on a macOS box is also guaranteed to be absent
// from termProgramByAppName.
func fakeAppBundle(t *testing.T, bundleID string) string {
	t.Helper()
	app := filepath.Join(t.TempDir(), "Fixture.app")
	contents := filepath.Join(app, "Contents")
	if err := os.MkdirAll(filepath.Join(contents, "MacOS"), 0o755); err != nil {
		t.Fatalf("mkdir bundle: %v", err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>` + bundleID + `</string>
</dict></plist>
`
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatalf("write Info.plist: %v", err)
	}
	return app
}

// TestBundleIDMemoNeverStoresANonAnswer is the single most important assertion
// in #1544. "plutil could not be asked" and "plutil answered that this bundle
// has no id we can name" are the #1524 split, and only the second may be
// stored. Storing the first would be strictly worse than every previous
// instance in this family: those were transient, and a cache entry is not.
func TestBundleIDMemoNeverStoresANonAnswer(t *testing.T) {
	const app = "/Applications/Fixture.app"
	memo := &bundleIDMemo{ids: map[string]string{}}
	ctx := context.Background()

	calls := 0
	killed := func(ctx context.Context, appPath string) (string, error) {
		calls++
		return "", errors.New("plutil " + appPath + ": signal: killed")
	}
	for i := 0; i < 3; i++ {
		id, err := memo.resolve(ctx, app, killed)
		if err == nil {
			t.Fatalf("resolve %d: a non-answer must stay a non-answer", i)
		}
		if id != "" {
			t.Fatalf("resolve %d returned %q with a non-nil error", i, id)
		}
	}
	if calls != 3 {
		t.Fatalf("the probe ran %d times, want 3 — a non-answer was memoized", calls)
	}
	if id, hit := memo.lookup(app); hit {
		t.Fatalf("the memo stored a non-answer as %q", id)
	}

	// And a later answer is admitted normally: the non-answers left nothing
	// behind to displace or to poison.
	answered := 0
	plutil := func(ctx context.Context, appPath string) (string, error) {
		answered++
		return "com.example.fixture", nil
	}
	for i := 0; i < 3; i++ {
		id, err := memo.resolve(ctx, app, plutil)
		if err != nil || id != "com.example.fixture" {
			t.Fatalf("resolve %d after recovery returned (%q, %v)", i, id, err)
		}
	}
	if answered != 1 {
		t.Fatalf("the recovered answer ran the probe %d times, want 1", answered)
	}
}

// TestBundleIDMemoAdmitsTheEmptyAnswer is the other half of the same rule.
// plutil exits non-zero both for a missing Info.plist and for one carrying no
// CFBundleIdentifier, and bundleIDVia reports both as ("", nil) — a real
// verdict the walk continues on. Re-paying an exec to be told it again is
// exactly what this memo exists to stop.
func TestBundleIDMemoAdmitsTheEmptyAnswer(t *testing.T) {
	const app = "/Applications/NoIdentifier.app"
	memo := &bundleIDMemo{ids: map[string]string{}}
	ctx := context.Background()

	calls := 0
	declined := func(ctx context.Context, appPath string) (string, error) {
		calls++
		return "", nil
	}
	for i := 0; i < 5; i++ {
		id, err := memo.resolve(ctx, app, declined)
		if err != nil || id != "" {
			t.Fatalf("resolve %d returned (%q, %v), want (\"\", nil)", i, id, err)
		}
	}
	if calls != 1 {
		t.Fatalf("five resolves of an empty ANSWER ran the probe %d times, want 1", calls)
	}
	if id, hit := memo.lookup(app); !hit || id != "" {
		t.Fatalf("the empty answer was not stored: lookup returned (%q, %v)", id, hit)
	}
}

// TestBundleIDMemoCollapsesRepeatCallsPerApp is the hit/miss claim: N candidate
// chains reaching the same host app pay one plutil between them. The second
// app is the vacuity guard — a memo that answered everything from one entry
// would satisfy the first assertion alone.
func TestBundleIDMemoCollapsesRepeatCallsPerApp(t *testing.T) {
	memo := &bundleIDMemo{ids: map[string]string{}}
	ctx := context.Background()

	perApp := map[string]int{}
	probe := func(ctx context.Context, appPath string) (string, error) {
		perApp[appPath]++
		return "id-for" + appPath, nil
	}
	for i := 0; i < 25; i++ {
		if _, err := memo.resolve(ctx, "/Applications/Obsidian.app", probe); err != nil {
			t.Fatalf("resolve %d: %v", i, err)
		}
	}
	if _, err := memo.resolve(ctx, "/Applications/Other.app", probe); err != nil {
		t.Fatalf("resolve of a second app: %v", err)
	}

	if got := perApp["/Applications/Obsidian.app"]; got != 1 {
		t.Fatalf("25 resolves of one app ran plutil %d times, want 1", got)
	}
	if got := perApp["/Applications/Other.app"]; got != 1 {
		t.Fatalf("a distinct app ran plutil %d times, want 1", got)
	}
}

// TestBundleIDMemoIsSafeForConcurrentResolves exists because this memo's
// lifetime is process-global, which means shared between goroutines: PID
// discovery and the liveness sweep both reach it. Runs red under -race without
// the RWMutex.
func TestBundleIDMemoIsSafeForConcurrentResolves(t *testing.T) {
	memo := &bundleIDMemo{ids: map[string]string{}}
	ctx := context.Background()
	probe := func(ctx context.Context, appPath string) (string, error) {
		return "com.example." + filepath.Base(appPath), nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 16; j++ {
				app := "/Applications/App" + string(rune('A'+(i+j)%8)) + ".app"
				if _, err := memo.resolve(ctx, app, probe); err != nil {
					t.Errorf("concurrent resolve: %v", err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	if len(memo.ids) != 8 {
		t.Fatalf("memo holds %d entries, want 8", len(memo.ids))
	}
}

// TestBundleIDForAppPathAnswersFromTheProcessMemo locks the PRODUCTION wiring
// rather than the memo's own semantics — a memo nothing consults is the #1390
// gap, and probe counting cannot see it because the production probe is a real
// exec.
//
// It proves the wiring behaviourally: the bundle is DELETED between the two
// calls, so a second call that re-ran plutil would come back with the empty
// answer (plutil exits non-zero on a missing Info.plist, which bundleIDVia
// reports as ("", nil)). Only a memo hit can still name the id.
func TestBundleIDForAppPathAnswersFromTheProcessMemo(t *testing.T) {
	app := fakeAppBundle(t, "com.example.memowiring")
	ctx := context.Background()

	id, err := bundleIDForAppPath(ctx, app)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if id != "com.example.memowiring" {
		t.Fatalf("first read returned %q, want com.example.memowiring", id)
	}
	if cached, hit := bundleIDs.lookup(app); !hit || cached != id {
		t.Fatalf("bundleIDForAppPath did not populate the process memo: lookup returned (%q, %v)", cached, hit)
	}

	if err := os.RemoveAll(app); err != nil {
		t.Fatalf("remove bundle: %v", err)
	}
	// Vacuity guard: the removal must actually make an UNMEMOIZED read come
	// back empty, or the assertion below proves nothing.
	if fresh, err := bundleIDUncached(ctx, app); err != nil || fresh != "" {
		t.Fatalf("after removal an uncached read returned (%q, %v), want (\"\", nil) — the fixture no longer distinguishes a hit from a miss", fresh, err)
	}

	again, err := bundleIDForAppPath(ctx, app)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if again != id {
		t.Fatalf("second read returned %q, want the memoized %q — bundleIDForAppPath re-ran plutil", again, id)
	}
}

// TestNewAncestryReadsIsFreshPerResolve pins the production constructor to the
// lifetime its type documents. The behavioural claim is in
// TestAncestryReadsAreScopedToOneResolveNotTheProcess; this is the half that
// notices if newAncestryReads is ever turned into a package-level singleton.
func TestNewAncestryReadsIsFreshPerResolve(t *testing.T) {
	ctx := context.Background()
	first := newAncestryReads()
	if _, _, err := first.probe(ctx, os.Getpid()); err != nil {
		t.Fatalf("probe of self: %v", err)
	}
	if len(first.seen) != 1 {
		t.Fatalf("the first memo recorded %d reads, want 1", len(first.seen))
	}
	second := newAncestryReads()
	if len(second.seen) != 0 {
		t.Fatalf("newAncestryReads handed out a memo carrying %d entries from an earlier resolve", len(second.seen))
	}
}

// countingChain is a scripted ppid chain plus per-PID read counters, for the
// two tests below that assert walk 2 re-reads nothing walk 1 already read.
type countingChain struct {
	rows  map[int]procInfoAnswer
	reads map[int]int
}

func (c *countingChain) read(ctx context.Context, pid int) (int, string, error) {
	c.reads[pid]++
	row, ok := c.rows[pid]
	if !ok {
		return 0, "", errors.New("no such process")
	}
	return row.ppid, row.cmd, nil
}

// duplicates reports how many reads beyond the first each PID cost.
func (c *countingChain) duplicates() int {
	dup := 0
	for _, n := range c.reads {
		if n > 1 {
			dup += n - 1
		}
	}
	return dup
}

// chainMissingTheCuratedMap builds a depth-5 ancestry ending in a top-level
// `.app` that termProgramByAppName does NOT know — the shape that makes walk 1
// run to the end of the chain and walk 2 run at all.
func chainMissingTheCuratedMap(appPath string) *countingChain {
	c := &countingChain{rows: map[int]procInfoAnswer{}, reads: map[int]int{}}
	for i := 0; i < 4; i++ {
		c.rows[1000+i] = procInfoAnswer{ppid: 1000 + i + 1, cmd: "/bin/zsh"}
	}
	c.rows[1004] = procInfoAnswer{ppid: 1, cmd: appPath + "/Contents/MacOS/Fixture"}
	return c
}

// TestIsKnownInteractiveHostReadsEachAncestorOnce is #1544's second half at the
// call site where the duplication was total: walk 2 runs only after walk 1
// reached a verdict AND found nothing, which for walk 1 means it walked the
// chain to its end — so before this change every PID in the chain was read
// exactly twice.
func TestIsKnownInteractiveHostReadsEachAncestorOnce(t *testing.T) {
	chain := chainMissingTheCuratedMap("/Applications/Fixture.app")
	bundleCalls := 0
	bundleID := func(ctx context.Context, appPath string) (string, error) {
		bundleCalls++
		return "md.obsidian", nil
	}

	admitted := isKnownInteractiveHostSharingReads(context.Background(), 1000,
		newAncestryReadsVia(chain.read), bundleID)

	// Vacuity guard: unless walk 2 actually ran, "no PID was read twice" is
	// satisfied by a chain only one walk ever touched.
	if bundleCalls == 0 {
		t.Fatal("walk 2 never ran: this fixture no longer exercises the shared reads")
	}
	if !admitted {
		t.Fatal("the fixture resolves to an allow-listed embedded host and must be admitted")
	}
	if len(chain.reads) != 5 {
		t.Fatalf("the walks reached %d distinct pids, want 5", len(chain.reads))
	}
	if dup := chain.duplicates(); dup != 0 {
		t.Fatalf("walk 2 re-read %d pid(s) walk 1 had already read; per-pid counts: %v", dup, chain.reads)
	}
}

// TestApplyAncestryFallbacksSharesItsReadsWithTheBundleIDWalk is the same claim
// at the other call site — the one on the liveness sweep's path, where a
// session whose TTY never resolves re-runs both walks every tick.
//
// It runs the real bundle-id walk over a real `.app`, so it also exercises the
// production bundleIDForAppPath (and therefore the process memo) end to end.
func TestApplyAncestryFallbacksSharesItsReadsWithTheBundleIDWalk(t *testing.T) {
	app := fakeAppBundle(t, "md.obsidian")
	chain := chainMissingTheCuratedMap(app)
	reads := newAncestryReadsVia(chain.read)
	l := &session.Launcher{}

	noKittyWindow := func(ctx context.Context, socket string, sessionPID int) (string, bool) {
		return "", true
	}
	complete := applyAncestryFallbacksVia(context.Background(), l, 1000,
		&ancestryProbe{pid: 1000, reads: reads}, noKittyWindow)

	if !complete {
		t.Fatal("both walks answered, so the run is complete")
	}
	// Vacuity guard: the bundle-id walk must have produced its answer, or the
	// duplicate count below describes a walk that never ran.
	if l.HostBundleID != "md.obsidian" {
		t.Fatalf("HostBundleID is %q, want md.obsidian — the bundle-id walk did not reach the fixture app", l.HostBundleID)
	}
	if len(chain.reads) != 5 {
		t.Fatalf("the walks reached %d distinct pids, want 5", len(chain.reads))
	}
	if dup := chain.duplicates(); dup != 0 {
		t.Fatalf("the bundle-id walk re-read %d pid(s) the memoized walk had already read; per-pid counts: %v", dup, chain.reads)
	}
	// The fixture path is unique per run, so this leaves one bounded entry in
	// the process memo — named here so the next reader knows it is deliberate.
	if _, hit := bundleIDs.lookup(app); !hit || !strings.HasSuffix(app, ".app") {
		t.Fatalf("the production bundle-id read did not go through the process memo for %s", app)
	}
}
