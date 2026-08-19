package services

import (
	"context"
	"slices"
	"testing"
	"time"

	"irrlicht/core/domain/dora"
	"irrlicht/core/domain/session"
)

// This file is #1563's behavioural half: what an operation that stacks git
// calls does when the ONE aggregate deadline covering it runs out. The value
// half — that each budget is small enough to be worth having — is
// TestDoraGitBudgetCoversOneHistoryWalk (core/cmd/irrlichd, the only package
// that may depend on both this layer and the git adapter) and
// TestYieldSweepBudgetFitsItsOwnTick below.
//
// Every assertion here turns on the BUDGET rather than on a wall clock, which
// is what keeps these tests instant: the loops consult ctx.Err(), so a probe
// that cancels the shared context at a chosen call drives the exact state a
// slow repository would take minutes to reach. Nothing here sleeps.
//
// The probe is injected for the reason #1529's is: no arrangement of real
// repositories can be made to exhaust a shared budget at call 3 of 6 on
// purpose, and what the budget does when it has is the whole subject.

// budgetProbe is a doraGitProbe/yieldGitProbe that records every git call it is
// asked for and spends the whole remaining budget on the nth one. Cancelling
// the context is how "this call consumed the budget" is expressed without
// waiting for it: the loops read ctx.Err(), which cancellation and a fired
// deadline set alike.
//
// It refuses to answer under an expired context, which is not decoration — it
// is what the real adapter does (core/adapters/outbound/git's run hands the
// context to exec.CommandContext, which fails before Start, and
// shellout.Answered reports that as a NON-answer). A fake that answered anyway
// would make the loop-check tests below pass for a reason that does not exist
// in production, and would stop the two mutations in this file's header
// isolating: removing a loop's own check would then change the RESULT as well
// as the call count, so one mutation would redden both tests and neither would
// pin its own obligation.
type budgetProbe struct {
	root          string
	rootByCWD     map[string]string
	tags          []dora.TagInfo
	commits       map[string][]dora.CommitInfo
	tagContaining map[string]string
	reverted      map[string][]string

	// exhaustAt is the 1-based index of the call that spends the budget. 0
	// never exhausts it, which is how each test below gets its vacuity guard
	// from the same fixture it makes its claim with.
	exhaustAt int
	cancel    context.CancelFunc

	calls     []string
	deadlines []time.Time
	bounded   []bool
}

// begin records one call and reports whether the probe may answer it.
//
// The ATTEMPT is recorded before the refusal, and that order is load-bearing
// rather than tidy: calls is what the loop-check tests read, so a probe that
// recorded only the calls it ANSWERED would report an identical sequence
// whether or not the loop checked its own budget — the loop would ask, the
// probe would silently decline, and the assertion would be green against a
// deleted check. Measured: with calls recorded after the refusal, deleting the
// ranges loop's budget check left TestComputeDoraMetrics_ChecksTheBudgetAtEachLoopHead passing.
//
// It cancels AFTER recording, so the call that spends the budget still answers
// — the work happened, the budget ran out on the way back.
func (p *budgetProbe) begin(ctx context.Context, what string) bool {
	p.calls = append(p.calls, what)
	deadline, ok := ctx.Deadline()
	p.deadlines = append(p.deadlines, deadline)
	p.bounded = append(p.bounded, ok)
	if ctx.Err() != nil {
		// What the real adapter does: exec.CommandContext under an expired
		// context fails before Start, and shellout.Answered reports that as a
		// NON-answer (see that predicate's doc).
		return false
	}
	if p.exhaustAt > 0 && len(p.calls) == p.exhaustAt {
		p.cancel()
	}
	return true
}

func (p *budgetProbe) GetGitRoot(ctx context.Context, dir string) (string, bool) {
	if !p.begin(ctx, "root:"+dir) {
		return "", false
	}
	if p.rootByCWD != nil {
		return p.rootByCWD[dir], true
	}
	return p.root, true
}

func (p *budgetProbe) ListReleaseTags(ctx context.Context, _ string) ([]dora.TagInfo, bool) {
	if !p.begin(ctx, "tags") {
		return nil, false
	}
	return p.tags, true
}

func (p *budgetProbe) CommitsInRange(ctx context.Context, _, _, toRef string) ([]dora.CommitInfo, bool) {
	if !p.begin(ctx, "range:"+toRef) {
		return nil, false
	}
	return p.commits[toRef], true
}

func (p *budgetProbe) TagContaining(ctx context.Context, _, hash string) (string, bool) {
	if !p.begin(ctx, "contains:"+hash) {
		return "", false
	}
	return p.tagContaining[hash], true
}

func (p *budgetProbe) RevertedCommits(ctx context.Context, dir string) ([]string, bool) {
	if !p.begin(ctx, "reverts:"+dir) {
		return nil, false
	}
	return p.reverted[dir], true
}

// doraFixture is three in-window releases, the last of which reverts a commit
// from the first — so the full call sequence is root, tags, three ranges and
// one `tag --contains`, six calls, and every stage this budget can abandon is
// actually reached. A fixture without the revert would assert on a loop the
// code never enters, which is a green that proves nothing.
func doraFixture(cancel context.CancelFunc, exhaustAt int) *budgetProbe {
	return &budgetProbe{
		root:      "/repo",
		exhaustAt: exhaustAt,
		cancel:    cancel,
		tags: []dora.TagInfo{
			{Name: "v1.0.0", Epoch: 1000},
			{Name: "v1.1.0", Epoch: 2000},
			{Name: "v1.2.0", Epoch: 3000},
		},
		commits: map[string][]dora.CommitInfo{
			"v1.0.0": {{Hash: "aaaaaaa", AuthorEpoch: 900, Body: "feat: a"}},
			"v1.1.0": {{Hash: "bbbbbbb", AuthorEpoch: 1900, Body: "feat: b"}},
			"v1.2.0": {{
				Hash:        "ccccccc",
				AuthorEpoch: 2900,
				Body:        "Revert \"feat: a\"\n\nThis reverts commit aaaaaaa.\n",
			}},
		},
		tagContaining: map[string]string{"aaaaaaa": "v1.0.0"},
	}
}

// theWholeWindow spans every fixture epoch above.
const theWholeWindow = int64(1 << 40)

// TestComputeDoraMetrics_DerivesOneBudgetOverTheWholeSequence pins the two
// structural claims the behavioural tests below cannot make, because those
// supply their own context: that the computation derives a bounded one at all,
// and that the SAME one spans every stage.
//
// One context rather than one per stage is the decision, not an implementation
// detail: a deadline per call would read as a bound while summing to
// `(1 + tags + candidates) x DoraGitBudget`, which is the unbounded stack this
// issue is about wearing a bound's clothes. What a DORA request costs is one
// number.
func TestComputeDoraMetrics_DerivesOneBudgetOverTheWholeSequence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	probe := doraFixture(cancel, 0)

	before := time.Now()
	result, err := ComputeDoraMetrics(ctx, probe, doraSessions("proj", "/repo/sub"), "proj", 0, theWholeWindow)
	after := time.Now()
	if err != nil {
		t.Fatalf("ComputeDoraMetrics: %v", err)
	}
	if !result.Available {
		t.Fatalf("the fixture must compute: %+v", result)
	}
	if len(probe.calls) < 2 {
		t.Fatalf("only %d git call(s) were made (%v); this test asserts about a SEQUENCE", len(probe.calls), probe.calls)
	}

	for i, bounded := range probe.bounded {
		if !bounded {
			t.Fatalf("git call %d (%s) ran under a context with NO deadline: the DORA stack is unbounded again (#1563)",
				i+1, probe.calls[i])
		}
	}
	for i := 1; i < len(probe.deadlines); i++ {
		if !probe.deadlines[i].Equal(probe.deadlines[0]) {
			t.Errorf("git call %d (%s) ran under a different deadline from call 1 (%v apart): one deadline per call "+
				"sums to len(calls) x DoraGitBudget and is not the aggregate this fix states (#1563)",
				i+1, probe.calls[i], probe.deadlines[i].Sub(probe.deadlines[0]))
		}
	}
	if probe.deadlines[0].After(after.Add(DoraGitBudget)) || !probe.deadlines[0].After(before) {
		t.Errorf("the derived deadline is %v from the call, want a positive span no longer than DoraGitBudget (%v)",
			probe.deadlines[0].Sub(before), DoraGitBudget)
	}
}

// TestComputeDoraMetrics_ChecksTheBudgetAtEachLoopHead is #1529's point 2 one
// layer over, and the reason each loop tests ctx.Err() itself instead of
// letting the git calls inherit the deadline.
//
// Inheriting alone is not nothing — an expired context does kill every child —
// but it produces the WRONG FACT and it costs a process per remaining item.
// Each surviving tag would still fork a `git log` that dies before Start, and
// the computation would report "every range was unreadable" where the truth is
// "I stopped after one". Detecting exhaustion is also the only way the
// aggregate becomes a bound a caller can observe rather than an accident of the
// children's own ceilings.
func TestComputeDoraMetrics_ChecksTheBudgetAtEachLoopHead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Call 3 is the first commit range: root, tags, range:v1.0.0.
	probe := doraFixture(cancel, 3)

	if _, err := ComputeDoraMetrics(ctx, probe, doraSessions("proj", "/repo/sub"), "proj", 0, theWholeWindow); err != nil {
		t.Fatalf("ComputeDoraMetrics: %v", err)
	}

	want := []string{"root:/repo/sub", "tags", "range:v1.0.0"}
	if !slices.Equal(probe.calls, want) {
		t.Errorf("the budget ran out at the first commit range and the computation went on to make %v, want %v: "+
			"an aggregate deadline that is only INHERITED by the git calls is not detected by the loops, so every "+
			"remaining tag and revert candidate still forks a doomed child and is reported unreadable rather than "+
			"unread (#1563)", probe.calls, want)
	}

	// Vacuity guard: the same fixture with nothing exhausting the budget must
	// reach every stage, or the assertion above would pass for a computation
	// that simply stops early.
	live, cancelLive := context.WithCancel(context.Background())
	defer cancelLive()
	full := doraFixture(cancelLive, 0)
	if _, err := ComputeDoraMetrics(live, full, doraSessions("proj", "/repo/sub"), "proj", 0, theWholeWindow); err != nil {
		t.Fatalf("ComputeDoraMetrics (vacuity guard): %v", err)
	}
	wantAll := []string{"root:/repo/sub", "tags", "range:v1.0.0", "range:v1.1.0", "range:v1.2.0", "contains:aaaaaaa"}
	if !slices.Equal(full.calls, wantAll) {
		t.Errorf("with budget to spare the computation made %v, want %v — the test above proves nothing if the "+
			"sequence truncates regardless", full.calls, wantAll)
	}
}

// TestComputeDoraMetrics_AbandonedSequenceBlanksRatherThanPublishes is the
// load-bearing one, and the property the whole budget rests on.
//
// A git call abandoned because the budget ran out is a call we DECLINED TO
// MAKE, so it must arrive as a NON-answer and take the same all-or-nothing exit
// every other non-answer here takes (#1543). Getting it backwards is not a
// missing nicety: publishing the metrics computed so far means a median lead
// time over the tags that happened to fit inside the budget, and a Change
// Failure Rate missing the reverts nobody got to — a biased sample presented as
// complete, with Available:true, which is precisely the failure gitMaxOutput's
// doc and #1553's rejection of `--since` both exist to refuse. A latency fix
// that introduced it would be a bad trade: the panel would be WRONG instead of
// slow, and silently so.
func TestComputeDoraMetrics_AbandonedSequenceBlanksRatherThanPublishes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	probe := doraFixture(cancel, 3)

	result, err := ComputeDoraMetrics(ctx, probe, doraSessions("proj", "/repo/sub"), "proj", 0, theWholeWindow)
	if err != nil {
		t.Fatalf("ComputeDoraMetrics: %v", err)
	}

	if result.Available {
		t.Errorf("two of three release ranges and the revert candidate were never read, and the panel published "+
			"anyway: %+v. A budget that abandons mid-sequence and publishes what it has computes DORA over a "+
			"biased subset and reports it as complete (#1563)", result)
	}
	for _, m := range []struct {
		name string
		got  dora.Metric
	}{
		{"DeploymentFrequency", result.DeploymentFrequency},
		{"LeadTime", result.LeadTime},
		{"ChangeFailureRate", result.ChangeFailureRate},
		{"MTTR", result.MTTR},
	} {
		if m.got != (dora.Metric{}) {
			t.Errorf("%s carries %+v on an abandoned computation; every metric must be its zero value, because "+
				"the handler and the dashboard render whatever is non-empty", m.name, m.got)
		}
	}
	if result.Message != doraGitTooSlow {
		t.Errorf("Message = %q, want %q: an abandoned budget is a different fact from a git that could not be RUN, "+
			"and the user's lever for it (a narrower window) is different too", result.Message, doraGitTooSlow)
	}

	// Vacuity guard: the identical fixture read within budget MUST publish, and
	// publish something non-zero — otherwise the assertions above hold for a
	// computation that never produces metrics at all, and the blank above would
	// be evidence of nothing.
	live, cancelLive := context.WithCancel(context.Background())
	defer cancelLive()
	full, err := ComputeDoraMetrics(live, doraFixture(cancelLive, 0), doraSessions("proj", "/repo/sub"), "proj", 0, theWholeWindow)
	if err != nil {
		t.Fatalf("ComputeDoraMetrics (vacuity guard): %v", err)
	}
	if !full.Available || full.DeploymentFrequency.Value == 0 {
		t.Errorf("the same fixture read within budget produced %+v, want an available panel with a non-zero "+
			"deployment frequency — if this fails, the assertions above pass for a fixture that computes nothing",
			full)
	}
}

// TestResolveDoraProjectRoot_AbandonedCandidateIsANonAnswer covers the FIRST
// stage, which is the one where getting the polarity wrong produces a false
// claim rather than a blank.
//
// resolveDoraProjectRoot walks every session of the project until one CWD
// resolves to a repo. Its `unread == 0` rule already says that one candidate
// nobody could read makes "project not found or not a git repository"
// unsupportable (#1543). A candidate abandoned on the budget is the same thing
// for a different reason — we declined to look at it — so it must poison the
// answer identically. A budget that reported "not a git repository" for a
// project whose later sessions were never examined would be #1543's exact
// defect arriving through the latency fix.
func TestResolveDoraProjectRoot_AbandonedCandidateIsANonAnswer(t *testing.T) {
	sessions := oneSession{states: []*session.SessionState{
		{SessionID: "s1", ProjectName: "proj", CWD: "/not/a/repo", State: session.StateReady},
		{SessionID: "s2", ProjectName: "proj", CWD: "/repo/sub", State: session.StateReady},
	}}
	// s1 is a real directory that is not a repo (root "", answered) — so
	// nothing but the abandonment can move the verdict.
	roots := map[string]string{"/not/a/repo": "", "/repo/sub": "/repo"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	probe := &budgetProbe{rootByCWD: roots, exhaustAt: 1, cancel: cancel}

	result, err := ComputeDoraMetrics(ctx, probe, sessions, "proj", 0, theWholeWindow)
	if err != nil {
		t.Fatalf("ComputeDoraMetrics: %v", err)
	}
	// Two obligations, and they fail to different mutations: the loop must stop
	// asking (this one), and what it stopped short of must poison the verdict
	// (the message below).
	if want := []string{"root:/not/a/repo"}; !slices.Equal(probe.calls, want) {
		t.Errorf("the budget ran out at candidate 1 and the resolve went on to ask for %v, want %v: a loop that "+
			"leaves the aggregate to its children forks a doomed `rev-parse` per remaining session (#1563)",
			probe.calls, want)
	}
	if result.Message != doraGitTooSlow {
		t.Errorf("Message = %q, want %q: session s2's CWD was never looked at because the budget ran out, and "+
			"answering anything about whether this project is a git repository is a claim the daemon cannot "+
			"support (#1543/#1563)", result.Message, doraGitTooSlow)
	}

	// Vacuity guard: the same two candidates read within budget, where the
	// second resolves, must find the repo — otherwise the assertion above holds
	// for a resolver that never answers.
	live, cancelLive := context.WithCancel(context.Background())
	defer cancelLive()
	full := &budgetProbe{rootByCWD: roots, cancel: cancelLive,
		tags: []dora.TagInfo{{Name: "v1.0.0", Epoch: 1000}}, commits: map[string][]dora.CommitInfo{}}
	if got, err := ComputeDoraMetrics(live, full, sessions, "proj", 0, theWholeWindow); err != nil || !got.Available {
		t.Errorf("within budget the same candidates produced (%+v, %v), want an available panel — if this fails, "+
			"the assertion above passes for a resolver that never resolves anything", got, err)
	}
}

// TestYieldSweep_StopsAtTheBudgetAndReportsWhatItDidNotRead is #1563's other
// operation. The sweep stacks one `rev-parse` per session and one
// `log --all --grep` per distinct project root, both unbounded in the input,
// and #1553 raised the second to a 30s ceiling.
//
// Two obligations in one test because they are one behaviour: the loops stop
// (rather than forking a doomed child per remaining item), and what they did
// not read is COUNTED and logged. The second is the polarity that matters —
// a root the sweep declined to scan must never be folded in as a root that was
// scanned and had no reverts, which is how a sweep reports "nothing flipped"
// for history it never read (#1543).
func TestYieldSweep_StopsAtTheBudgetAndReportsWhatItDidNotRead(t *testing.T) {
	states := []*session.SessionState{
		{SessionID: "a", HeadCommit: "aaa", CWD: "/one"},
		{SessionID: "b", HeadCommit: "bbb", CWD: "/two"},
		{SessionID: "c", HeadCommit: "ccc", CWD: "/three"},
	}
	roots := map[string]string{"/one": "/one", "/two": "/two", "/three": "/three"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	probe := &budgetProbe{rootByCWD: roots, exhaustAt: 2, cancel: cancel}
	log := &gateLog{}
	NewYieldSweeper(&diagFakeRepo{sessions: states}, probe, log, 0).Sweep(ctx)

	for _, call := range probe.calls {
		if len(call) >= 8 && call[:8] == "reverts:" {
			t.Errorf("the budget ran out during root resolution and the sweep still scanned history: %v. "+
				"Each of those is a `git log --all --grep` forked only to die before Start (#1563)", probe.calls)
			break
		}
	}
	if len(probe.calls) != 2 {
		t.Errorf("the budget ran out at root 2 and the sweep made %d call(s) (%v), want 2: a loop that leaves the "+
			"aggregate to its children keeps going and reports every remaining item as unreadable (#1563)",
			len(probe.calls), probe.calls)
	}
	if !log.errorMentioning("could not resolve a repo root") {
		t.Error("the sessions whose CWD was never resolved are invisible: a sweep that stops early must say so, " +
			"or it looks exactly like a sweep that found nothing (#1543/#1563)")
	}
	if !log.errorMentioning("could not read git history") {
		t.Error("the project roots the sweep declined to scan were not counted as unscanned. An unscanned root " +
			"folded in as a scanned one with no findings is how a sweep reports 'nothing flipped' for history it " +
			"never read (#1543)")
	}

	// Vacuity guard: the same sweep with budget to spare must read everything
	// and complain about nothing, or the assertions above hold for a sweeper
	// that always stops early and always logs.
	live, cancelLive := context.WithCancel(context.Background())
	defer cancelLive()
	quiet := &gateLog{}
	full := &budgetProbe{rootByCWD: roots, cancel: cancelLive}
	NewYieldSweeper(&diagFakeRepo{sessions: states}, full, quiet, 0).Sweep(live)
	if len(full.calls) != 6 {
		t.Errorf("with budget to spare the sweep made %d call(s) (%v), want 6 (3 roots + 3 revert scans)",
			len(full.calls), full.calls)
	}
	if quiet.errorMentioning("could not resolve a repo root") || quiet.errorMentioning("could not read git history") {
		t.Errorf("a fully readable sweep reported unread work: %v", quiet.errors)
	}
}

// TestYieldSweepBudgetFitsItsOwnTick is what makes yieldSweepBudget's value
// evidence rather than a number someone liked. The sweep runs on a
// time.Ticker, which DROPS ticks it overruns, so a budget at or above the
// interval does not delay one sweep — it silently turns a 30-minute cadence
// into whatever the git calls cost.
//
// Both constants live in this package, so unlike DORA's relation (pinned in
// core/cmd/irrlichd, because application/services may not import an adapter)
// this one is checkable where it is stated.
func TestYieldSweepBudgetFitsItsOwnTick(t *testing.T) {
	if yieldSweepBudget >= DefaultYieldSweepInterval {
		t.Errorf("yieldSweepBudget (%v) is not smaller than DefaultYieldSweepInterval (%v): a sweep allowed to "+
			"run for its whole tick overruns it, and Go's Ticker drops the ticks it overruns rather than queuing "+
			"them (#1563)", yieldSweepBudget, DefaultYieldSweepInterval)
	}
	if yieldSweepBudget <= 0 {
		t.Fatalf("yieldSweepBudget is %v: a non-positive budget makes context.WithTimeout fire immediately, so "+
			"every sweep would abandon before its first git call", yieldSweepBudget)
	}
}
