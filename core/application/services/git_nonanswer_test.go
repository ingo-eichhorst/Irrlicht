package services

import (
	"testing"

	"irrlicht/core/domain/dora"
	"irrlicht/core/domain/session"
)

// #1543's collapse did not stop at the adapter — the whole harm was what the
// zero value MEANT once it arrived. These pin the four consumer decisions, and
// each is a claim the daemon made to a user on evidence it did not have.
//
// Red-first: each was run against the consumer arm reverted (the `!answered`
// branch removed, so a non-answer takes the old empty-answer path). What each
// reported is recorded on the test. That is the same evidence shape as the
// adapter's defect test — the consumer code is new, but the BEHAVIOUR each one
// asserts is the pre-existing defect, so reverting the arm reproduces it.

// unreadGit reports a non-answer for whichever probes it is told to fail, and a
// normal answer for the rest. The two are separable so a test can pin which
// probe's non-answer produced which message.
type unreadGit struct {
	root      string
	tags      []dora.TagInfo
	commits   map[string][]dora.CommitInfo
	unreadFor map[string]bool // "root" | "tags" | "commits" | "tagcontaining"
}

func (g *unreadGit) answered(probe string) bool { return !g.unreadFor[probe] }

func (g *unreadGit) GetGitRoot(string) (string, bool) {
	if !g.answered("root") {
		return "", false
	}
	return g.root, true
}

func (g *unreadGit) ListReleaseTags(string) ([]dora.TagInfo, bool) {
	if !g.answered("tags") {
		return nil, false
	}
	return g.tags, true
}

func (g *unreadGit) CommitsInRange(_, _, toRef string) ([]dora.CommitInfo, bool) {
	if !g.answered("commits") {
		return nil, false
	}
	return g.commits[toRef], true
}

func (g *unreadGit) TagContaining(string, string) (string, bool) {
	if !g.answered("tagcontaining") {
		return "", false
	}
	return "", true
}

type oneSession struct{ states []*session.SessionState }

func (s oneSession) ListAll() ([]*session.SessionState, error) { return s.states, nil }

func doraSessions(project, cwd string) oneSession {
	return oneSession{states: []*session.SessionState{
		{SessionID: "s1", ProjectName: project, CWD: cwd, State: session.StateReady},
	}}
}

// TestDoraReportsUnreadableGitRatherThanAFalseVerdict is the user-visible half
// of #1543. Three different non-answers each used to reach the user as a
// confident statement about their repository.
//
// RED-FIRST (each arm, with its `!answered` branch reverted to the old path):
//
//	unreadable root    -> Message = "project not found or not a git repository"
//	unreadable tags    -> Message = "no releases found for this project"
//	unreadable commits -> Available = true, with a lead time computed over the
//	                      tags that happened to succeed
func TestDoraReportsUnreadableGitRatherThanAFalseVerdict(t *testing.T) {
	tags := []dora.TagInfo{{Name: "v0.1.0", Epoch: 1000}, {Name: "v0.2.0", Epoch: 2000}}
	// v0.2.0 carries a REVERT commit, so DetectReverts produces a candidate and
	// TagContaining is actually reached. Without it that arm asserts on a probe
	// the code never calls, which is a green that proves nothing.
	commits := map[string][]dora.CommitInfo{
		"v0.1.0": {{Hash: "a", AuthorEpoch: 900}},
		"v0.2.0": {{
			Hash:        "b",
			AuthorEpoch: 1900,
			Body:        "Revert \"add a\"\n\nThis reverts commit aaaaaaa.\n",
		}},
	}

	cases := []struct {
		name   string
		unread string
		wasSay string // what the collapse used to claim
	}{
		{"repo root unread", "root", "project not found or not a git repository"},
		{"release tags unread", "tags", "no releases found for this project"},
		{"a release's commits unread", "commits", "a lead time over the tags that happened to succeed"},
		{"the tag containing a revert unread", "tagcontaining", "a change failure rate missing that revert"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			git := &unreadGit{
				root:      "/repo",
				tags:      tags,
				commits:   commits,
				unreadFor: map[string]bool{tc.unread: true},
			}
			got, err := ComputeDoraMetrics(git, doraSessions("proj", "/repo/sub"), "proj", 0, 9999)
			if err != nil {
				t.Fatalf("ComputeDoraMetrics: %v", err)
			}
			if got.Available {
				t.Errorf("reported Available:true on a git it could not read; it used to publish %s", tc.wasSay)
			}
			if got.Message != doraGitUnreadable {
				t.Errorf("Message = %q, want %q — the old message (%q) is a claim about the "+
					"repository made by a probe that never reached it", got.Message, doraGitUnreadable, tc.wasSay)
			}
		})
	}
}

// TestDoraStillReportsItsGenuineVerdicts is the vacuity guard for the test
// above. Without it, a ComputeDoraMetrics that answered "could not read git"
// unconditionally would pass every arm — and the two genuine verdicts below are
// the ones the collapse was hiding behind, so losing them would be a regression
// wearing the fix's clothes.
func TestDoraStillReportsItsGenuineVerdicts(t *testing.T) {
	t.Run("no session resolves to a repo", func(t *testing.T) {
		git := &unreadGit{root: ""}
		got, err := ComputeDoraMetrics(git, doraSessions("proj", "/nowhere"), "proj", 0, 9999)
		if err != nil {
			t.Fatalf("ComputeDoraMetrics: %v", err)
		}
		if got.Available || got.Message != "project not found or not a git repository" {
			t.Errorf("got (%v, %q), want a genuine not-a-repo verdict", got.Available, got.Message)
		}
	})

	t.Run("a repo with no release tags", func(t *testing.T) {
		git := &unreadGit{root: "/repo", tags: nil}
		got, err := ComputeDoraMetrics(git, doraSessions("proj", "/repo/sub"), "proj", 0, 9999)
		if err != nil {
			t.Fatalf("ComputeDoraMetrics: %v", err)
		}
		if got.Available || got.Message != "no releases found for this project" {
			t.Errorf("got (%v, %q), want a genuine no-releases verdict", got.Available, got.Message)
		}
	})

	t.Run("a healthy repo still computes", func(t *testing.T) {
		git := &unreadGit{
			root: "/repo",
			tags: []dora.TagInfo{{Name: "v0.1.0", Epoch: 1000}, {Name: "v0.2.0", Epoch: 2000}},
			commits: map[string][]dora.CommitInfo{
				"v0.1.0": {{Hash: "a", AuthorEpoch: 900}},
				"v0.2.0": {{Hash: "b", AuthorEpoch: 1900}},
			},
		}
		got, err := ComputeDoraMetrics(git, doraSessions("proj", "/repo/sub"), "proj", 0, 9999)
		if err != nil {
			t.Fatalf("ComputeDoraMetrics: %v", err)
		}
		if !got.Available {
			t.Errorf("a fully readable repo reported Available:false (%q)", got.Message)
		}
	})
}

// TestResolveDoraProjectRootAnswersWhenAnyCandidateDid pins the one place the
// non-answer must NOT propagate. The question is "where is this project's repo
// root", and one candidate cwd resolving answers it — so a second candidate
// that went unread is not a failure. Reporting unreadable there would blank a
// chart the daemon has everything it needs to draw.
func TestResolveDoraProjectRootAnswersWhenAnyCandidateDid(t *testing.T) {
	// A git that fails the FIRST cwd and resolves the second.
	seen := 0
	git := &countingRootGit{fn: func(cwd string) (string, bool) {
		seen++
		if seen == 1 {
			return "", false
		}
		return "/repo", true
	}}
	sessions := oneSession{states: []*session.SessionState{
		{SessionID: "a", ProjectName: "proj", CWD: "/one", State: session.StateReady},
		{SessionID: "b", ProjectName: "proj", CWD: "/two", State: session.StateReady},
	}}

	root, answered, err := resolveDoraProjectRoot(git, sessions, "proj")
	if err != nil {
		t.Fatalf("resolveDoraProjectRoot: %v", err)
	}
	if !answered {
		t.Error("reported unreadable although a later candidate resolved the root")
	}
	if root != "/repo" {
		t.Errorf("root = %q, want /repo", root)
	}

	t.Run("every candidate unread is unreadable", func(t *testing.T) {
		all := &countingRootGit{fn: func(string) (string, bool) { return "", false }}
		_, answered, err := resolveDoraProjectRoot(all, sessions, "proj")
		if err != nil {
			t.Fatalf("resolveDoraProjectRoot: %v", err)
		}
		if answered {
			t.Error("every candidate went unread, but the result was reported as an answer")
		}
	})

	// #1551 review finding 7. The old expression was `asked == 0 || unread <
	// asked`, which let ONE negative candidate outvote N-1 unread ones: with
	// asked=3, unread=2, the user got the confident "project not found or not
	// a git repository" on the strength of a single cwd.
	//
	// RED-FIRST: against `unread < asked` this reports
	//	2 of 3 candidates went unread, but the result was reported as an answer
	t.Run("a mix of unread and negative candidates is not an answer", func(t *testing.T) {
		seen := 0
		mixed := &countingRootGit{fn: func(string) (string, bool) {
			seen++
			if seen == 3 {
				return "", true // genuinely not a repo
			}
			return "", false // unread
		}}
		three := oneSession{states: []*session.SessionState{
			{SessionID: "a", ProjectName: "proj", CWD: "/one", State: session.StateReady},
			{SessionID: "b", ProjectName: "proj", CWD: "/two", State: session.StateReady},
			{SessionID: "c", ProjectName: "proj", CWD: "/three", State: session.StateReady},
		}}
		_, answered, err := resolveDoraProjectRoot(mixed, three, "proj")
		if err != nil {
			t.Fatalf("resolveDoraProjectRoot: %v", err)
		}
		if answered {
			t.Error("2 of 3 candidates went unread, but the result was reported as an answer; " +
				"one negative cwd cannot support \"not a git repository\" for the project")
		}
	})

	t.Run("no candidate at all is an answer", func(t *testing.T) {
		none := &countingRootGit{fn: func(string) (string, bool) { return "", true }}
		_, answered, err := resolveDoraProjectRoot(none, oneSession{}, "proj")
		if err != nil {
			t.Fatalf("resolveDoraProjectRoot: %v", err)
		}
		if !answered {
			t.Error("a project with no sessions is a normal nothing-to-compute outcome, not an unreadable git")
		}
	})
}

type countingRootGit struct {
	fn func(string) (string, bool)
	unreadGit
}

func (g *countingRootGit) GetGitRoot(cwd string) (string, bool) { return g.fn(cwd) }

// enricherGit is a GitResolver whose branch/name probes report a non-answer.
type enricherGit struct {
	branchAnswered bool
	nameAnswered   bool
	headAnswered   bool
	rootAnswered   bool
	branch         string
	name           string
	head           string
	root           string
}

func (g *enricherGit) GetBranch(string) (string, bool)       { return g.branch, g.branchAnswered }
func (g *enricherGit) GetProjectName(string) (string, bool)  { return g.name, g.nameAnswered }
func (g *enricherGit) GetGitRoot(string) (string, bool)      { return g.root, g.rootAnswered }
func (g *enricherGit) GetHeadCommit(string) (string, bool)   { return g.head, g.headAnswered }
func (g *enricherGit) GetBranchFromTranscript(string) string { return "" }

// GetCWDFromTranscript reports a MOVE: RefreshOnActivity only reaches the
// branch/project block when the transcript's cwd differs from the state's.
func (g *enricherGit) GetCWDFromTranscript(string) string { return "/new/repo" }

// TestBackfillDoesNotMarkASessionDoneOnANonAnswer is the permanence bug, and it
// is the sharpest of the four: backfillOne returns early when ProjectName is
// already set, so a session that stored a value from a git which never ran was
// never revisited. One transient failure at first enrichment left a session
// with no project and no branch for the rest of its life.
//
// RED-FIRST (with the adoptIfAnswered calls reverted to unconditional
// assignment): "a non-answer marked the session updated; it will never be
// retried". Measured by reverting the arm.
func TestBackfillDoesNotMarkASessionDoneOnANonAnswer(t *testing.T) {
	e := newMetadataEnricher(&enricherGit{name: "guess", branch: "guess", rootAnswered: true}, nil)
	st := &session.SessionState{SessionID: "s", CWD: "/repo/sub"}

	if e.backfillOne(st) {
		t.Error("a non-answer marked the session updated; because backfillOne returns early once " +
			"ProjectName is set, that verdict would never be revisited")
	}
	if st.ProjectName != "" {
		t.Errorf("ProjectName = %q — a guess from a git that never ran was stored as resolved", st.ProjectName)
	}
	if st.GitBranch != "" {
		t.Errorf("GitBranch = %q — same", st.GitBranch)
	}

	// Vacuity guard: an answer must still backfill, or a backfillOne that did
	// nothing at all would pass the arm above.
	answering := newMetadataEnricher(&enricherGit{
		nameAnswered: true, name: "proj",
		branchAnswered: true, branch: "main", rootAnswered: true,
	}, nil)
	fresh := &session.SessionState{SessionID: "s", CWD: "/repo/sub"}
	if !answering.backfillOne(fresh) {
		t.Fatal("a git that answered did not backfill")
	}
	if fresh.ProjectName != "proj" || fresh.GitBranch != "main" {
		t.Errorf("got (%q, %q), want (proj, main)", fresh.ProjectName, fresh.GitBranch)
	}
}

// TestRefreshOnActivityNeverReportsAnotherDirectorysBranch is #1551 review
// finding 3, and it is a defect the FIRST DRAFT of #1543 introduced rather
// than a pre-existing one: applying "never overwrite what you already hold"
// to a session that has MOVED keeps values describing the OLD directory.
//
// A blank branch is a missing fact. Another repo's branch is a false one — and
// it was permanent, because backfillOne returns early while ProjectName is set
// and RefreshOnActivity skips the whole block once cwd == state.CWD.
//
// RED-FIRST: against the first draft this reports
//
//	GitBranch = "old-branch" after moving to /new/repo
func TestRefreshOnActivityNeverReportsAnotherDirectorysBranch(t *testing.T) {
	git := &enricherGit{} // every probe reports a non-answer
	e := newMetadataEnricher(git, emptyMetrics{})
	st := &session.SessionState{
		SessionID:   "s",
		CWD:         "/old/repo",
		GitBranch:   "old-branch",
		ProjectName: "old-project",
	}

	e.RefreshOnActivity(st, "/transcripts/s.jsonl")

	if st.CWD != "/new/repo" {
		t.Fatalf("CWD = %q, want /new/repo — the fixture is not exercising a move", st.CWD)
	}
	if st.GitBranch == "old-branch" {
		t.Error("the session moved to /new/repo but still reports /old/repo's branch; that is a " +
			"claim about the new directory the daemon never established")
	}
	// ProjectName is the opposite obligation, and #1551's QA (B1) is where the
	// first draft had it backwards: blanking it is DESTRUCTIVE, because an
	// empty project name is bucketed as "unknown" by aggregateYieldBySession
	// and is invisible to DORA. main kept it here, #1543's polarity says a
	// non-answer leaves what we hold alone, and the only repair path is
	// bounded and idle-only.
	if st.ProjectName != "old-project" {
		t.Errorf("ProjectName = %q — a git that could not be run destroyed a resolved project "+
			"name, which costs the session its yield spend and its DORA membership", st.ProjectName)
	}

	// Vacuity guard: a git that ANSWERS must still update both, or an enricher
	// that blanked everything unconditionally would pass the arms above.
	answering := newMetadataEnricher(&enricherGit{
		branchAnswered: true, branch: "new-branch",
		rootAnswered: true, root: "/new/new-project",
	}, emptyMetrics{})
	st2 := &session.SessionState{SessionID: "s", CWD: "/old/repo", GitBranch: "old-branch", ProjectName: "old-project"}
	answering.RefreshOnActivity(st2, "/transcripts/s.jsonl")
	if st2.GitBranch != "new-branch" {
		t.Errorf("GitBranch = %q, want new-branch", st2.GitBranch)
	}
	if st2.ProjectName != "new-project" {
		t.Errorf("ProjectName = %q, want new-project", st2.ProjectName)
	}
}

// TestCaptureYieldOnReadyDoesNotRecordAVerdictItDidNotReach pins the yield
// path. "unknown" is not a neutral placeholder here — it is what
// aggregateYieldBySession buckets a session's dollars into, and it is
// persisted. A git that could not be run has not established it.
//
// RED-FIRST (with the `!answered` arm removed): "a non-answer was persisted as
// the verdict YieldUnknown".
func TestCaptureYieldOnReadyDoesNotRecordAVerdictItDidNotReach(t *testing.T) {
	e := newMetadataEnricher(&enricherGit{rootAnswered: true}, nil)
	st := &session.SessionState{SessionID: "s", CWD: "/repo", YieldState: session.YieldProductive, HeadCommit: "abc"}

	e.CaptureYieldOnReady(st)

	if st.YieldState != session.YieldProductive {
		t.Errorf("YieldState = %q — a git that could not be run overwrote a verdict it did not reach", st.YieldState)
	}
	if st.HeadCommit != "abc" {
		t.Errorf("HeadCommit = %q — a non-answer cleared a resolved commit", st.HeadCommit)
	}

	// #1551 QA, B2: a session with NO verdict yet must not be left empty.
	// aggregateYieldBySession skips YieldState == "" entirely, so an empty
	// state removes the session's spend from the chart, where main put it in
	// the unknown bucket. Unknown is the honest bucket for a non-answer.
	fresh := &session.SessionState{SessionID: "s2", CWD: "/gone"}
	e.CaptureYieldOnReady(fresh)
	if fresh.YieldState != session.YieldUnknown {
		t.Errorf("YieldState = %q for a first-ever non-answer, want unknown — an empty state is "+
			"skipped by aggregateYieldBySession, so the session's spend leaves the yield chart "+
			"altogether. A cleaned-up worktree makes this permanent, not transient.", fresh.YieldState)
	}

	// Vacuity guard: a git that ANSWERS with no HEAD is a genuine
	// not-a-git-repo verdict and must still be recorded.
	genuine := newMetadataEnricher(&enricherGit{headAnswered: true, head: "", rootAnswered: true}, nil)
	st2 := &session.SessionState{SessionID: "s", CWD: "/repo", YieldState: session.YieldProductive}
	genuine.CaptureYieldOnReady(st2)
	if st2.YieldState != session.YieldUnknown {
		t.Errorf("YieldState = %q, want unknown — an answered empty HEAD IS the not-a-repo verdict", st2.YieldState)
	}
}

// TestYieldSweepReportsRootsItCouldNotScan pins the sweeper. A revert scan that
// never ran is not "this repo has no reverts": the sweep would report "nothing
// flipped" for history it never read, with no log and nothing to look at.
//
// RED-FIRST (with collectRevertedSHAs's `!answered` arm removed): no error was
// logged and the sweep reported a clean pass.
func TestYieldSweepReportsRootsItCouldNotScan(t *testing.T) {
	log := &gateLog{}
	git := &unreadRevertGit{root: "/repo", unread: true}
	s := NewYieldSweeper(&diagFakeRepo{}, git, log, 0)

	s.collectRevertedSHAs(map[string]string{"/repo": "/repo/sub"})

	if !log.errorMentioning("could not read git history") {
		t.Errorf("a root whose revert scan never ran was not reported; logged: %v\n"+
			"Silence here means the sweep reports \"nothing flipped\" for history it never read", log.errors)
	}

	// Vacuity guard: a sweep that read everything must log nothing, or a
	// sweeper that always complained would pass the arm above.
	quiet := &gateLog{}
	NewYieldSweeper(&diagFakeRepo{}, &unreadRevertGit{root: "/repo"}, quiet, 0).
		collectRevertedSHAs(map[string]string{"/repo": "/repo/sub"})
	if quiet.errorMentioning("could not read git history") {
		t.Errorf("a fully readable sweep logged an unread-roots error: %v", quiet.errors)
	}
}

// TestYieldSweepScansTheRootNotAPossiblyDeletedCWD is #1551 QA finding B2's
// second half. GetGitRoot walks up to the nearest existing ancestor, so it
// answers for a cleaned-up worktree — but the cwd it resolved FROM may be gone,
// and running the revert scan there fails at chdir before exec, which is a
// permanent non-answer. Recording the cwd left that repo unscanned forever and,
// once this PR added the report, logging it every 30 minutes.
//
// RED-FIRST: with `rootDirs[root] = cwd` this reports
//
//	scan dir = "/repo/worktrees/gone", want the resolved root "/repo"
func TestYieldSweepScansTheRootNotAPossiblyDeletedCWD(t *testing.T) {
	git := &unreadRevertGit{root: "/repo"}
	s := NewYieldSweeper(&diagFakeRepo{}, git, &gateLog{}, 0)

	rootDirs := map[string]string{}
	if !s.recordRootDir(rootDirs, "/repo/worktrees/gone") {
		t.Fatal("a resolvable root was reported as unresolved")
	}
	if got := rootDirs["/repo"]; got != "/repo" {
		t.Errorf("scan dir = %q, want the resolved root %q — the cwd it was resolved from may "+
			"no longer exist, and a deleted directory is a PERMANENT non-answer", got, "/repo")
	}
}

// unreadRevertGit is a yieldGitProbe whose revert scan reports a non-answer.
// Local to this file rather than reusing yield_sweeper_test.go's fakeYieldGit,
// because that one lives in the EXTERNAL services_test package and these tests
// need the unexported collectRevertedSHAs.
type unreadRevertGit struct {
	root string
	// unread / rootUnread pick WHICH probe reports the non-answer; the two
	// sweeper tests need different ones, and one type with two knobs beats two
	// types differing by a line.
	unread     bool
	rootUnread bool
}

func (g *unreadRevertGit) GetGitRoot(string) (string, bool) {
	if g.rootUnread {
		return "", false
	}
	return g.root, true
}
func (g *unreadRevertGit) RevertedCommits(string) ([]string, bool) {
	if g.unread {
		return nil, false
	}
	return nil, true
}

// TestYieldSweepReportsRootsItCouldNotResolve is #1551 review finding 4: a cwd
// whose GetGitRoot never answered never entered rootDirs, so
// collectRevertedSHAs — which walks that map — could not report it, and the
// sweep went completely silent for history it never read.
//
// RED-FIRST: against the previous code this reports
//
//	a cwd whose repo root never resolved was not reported; logged: []
func TestYieldSweepReportsRootsItCouldNotResolve(t *testing.T) {
	log := &gateLog{}
	s := NewYieldSweeper(&diagFakeRepo{}, &unreadRevertGit{rootUnread: true}, log, 0)

	s.indexByCommit([]*session.SessionState{
		{SessionID: "a", HeadCommit: "abc", CWD: "/repo/one"},
		{SessionID: "b", HeadCommit: "def", CWD: "/repo/two"},
	})

	if !log.errorMentioning("could not resolve a repo root") {
		t.Errorf("a cwd whose repo root never resolved was not reported; logged: %v\n"+
			"collectRevertedSHAs cannot report it — the cwd never reaches rootDirs at all", log.errors)
	}

	// Vacuity guard: a sweep that resolved every root must log nothing.
	quiet := &gateLog{}
	NewYieldSweeper(&diagFakeRepo{}, &unreadRevertGit{root: "/repo"}, quiet, 0).
		indexByCommit([]*session.SessionState{{SessionID: "a", HeadCommit: "abc", CWD: "/repo/one"}})
	if quiet.errorMentioning("could not resolve a repo root") {
		t.Errorf("a fully resolvable sweep logged an unresolved-roots error: %v", quiet.errors)
	}
}
