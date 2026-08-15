package git

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"irrlicht/core/domain/dora"
	"irrlicht/core/pkg/pathutil"
	"irrlicht/core/pkg/shellout"
	"irrlicht/core/pkg/transcript"
)

// revertTrailer matches the "This reverts commit <sha>." trailer that
// `git revert` writes into the body of a revert commit (#373).
var revertTrailer = regexp.MustCompile(`(?m)^This reverts commit ([0-9a-f]{7,40})`)

// releaseTagPattern matches release tags of the form v<major>.<minor>.<patch>
// (this repo's version.json convention) — used by the DORA metrics methods
// below to filter out non-release tags (#951).
var releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// gitPath is resolved once from a fixed set of trusted directories rather
// than trusted PATH, per go:S4036.
var gitPath = pathutil.MustResolve("git")

// gitRevParseCmd is the git subcommand shared by GetBranch, GetHeadCommit,
// and GetGitRoot to resolve refs, commits, and repo-relative paths.
const gitRevParseCmd = "rev-parse"

// gitTimeout is the ceiling for every shellout in this adapter whose cost does
// NOT grow with the number of commits in the repository. gitHistoryTimeout
// below is the other profile, and the reason there are two is #1553. Until
// #1543 there was no ceiling at all: seven plain exec.Command calls with no
// context, inside a long-lived daemon, on directories that can be network
// mounts or repos holding a lock.
//
// The value is this adapter's own rather than a copy of the 2s next door
// (processlifecycle's shelloutTimeout), because the workloads are not
// comparable — a `ps` answers about one PID, `git tag --contains` walks a
// commit graph. Measured on this repo (3209 commits across all refs, 325 MiB
// .git, git 2.50.1 / Apple Git-155, darwin/arm64, warm cache, ~7ms of each
// figure being process start): `rev-parse` 33ms, `for-each-ref refs/tags`
// 38ms, `tag --contains` 39ms. 5s is ~130x the heaviest of those.
//
// `tag --contains` is the one whose membership in this profile is a judgement
// rather than a fact, and #1553's own table had it backwards: it does not
// scale with the TAG count, it walks the commit graph, so it scales with
// history. Measured on a synthetic 1,000,000-commit repo carrying 40 tags:
// 1.04s for a commit near the root (the worst case — every tag contains it)
// against 38ms for a recent one. That is ~1us/commit where the two `log` walks
// below cost ~22, because it reads the commit GRAPH rather than every commit
// MESSAGE, so 5s covers roughly 5M commits. It stays in this profile rather
// than moving to the longer ceiling because it is also the call
// ComputeDoraMetrics makes most often — once per revert candidate — so a
// longer ceiling multiplies worst there (#1563).
//
// The consequence worth stating: these reads sit inside the detector loop,
// where 5s of stall is bad but bounded. That is what the value is chosen
// against, and it is why the history walks could not simply take it with them.
//
// It bounds ONE CALL, not one operation, and callers stack it —
// adoptGitMetadata makes 2, RefreshOnActivity up to 3, ComputeDoraMetrics
// 1 + len(in-window tags) + len(revertCandidates). That was #1563, and since
// #1563 the SUM is the caller's to bound: every method here takes a
// context.Context, so an operation that stacks calls derives one aggregate
// deadline over the whole sequence and each call runs under whichever of the
// two is shorter. What is NOT delegated is this ceiling — a caller with no
// aggregate at all (services.noGitBudget) still gets it per call, which is the
// property #1543 established and #1563 deliberately did not weaken.
const gitTimeout = 5 * time.Second

// gitHistoryTimeout is the ceiling for the two shellouts that read every commit
// MESSAGE in their range: RevertedCommits (`log --all --grep`) and
// CommitsInRange (`log --pretty=%B`). They are the only two whose cost grows
// with the size of the history, and #1553 is that one 5s ceiling covered both
// profiles — so a repository big enough to exceed it got a permanent NON-answer
// where before #1543 it got a correct-but-slow one, the DORA panel reading
// "could not read this project's git history" forever and the yield sweep
// logging an unread root every 30 minutes.
//
// MEASURED, on synthetic `git fast-import` repositories (one branch, ~60-byte
// commit messages, packed, warm cache, same machine and git as above):
//
//	commits              log --all --grep   log --pretty=%B
//	    3,209 (this repo)           0.15s   0.08s (over the 1,386 from HEAD)
//	  100,000                       1.89s   2.24s
//	1,000,000                      22.5s   30.5s
//
// Two things follow, and the first corrects #1553's own headline figure. 5s is
// NOT reached at 100k commits — that costs 1.9s, comfortably inside it; the
// wall is nearer 250k for the grep walk (19us/commit at 100k, 22us at 1M) and
// ~200k for the body walk. And 30s covers the largest history measured here,
// 1M commits — Linux-kernel scale — with ~25% headroom on the grep walk.
//
// The caveats belong to the measurement rather than to the constant: a
// synthetic repo's commit objects are far smaller than a real one's (this repo
// averages ~1.9 KB of message per commit against the synthetic's ~60 bytes), it
// carries ONE ref where `--all` on a real large repo iterates thousands, and
// the cache was warm. Read 1M commits as the shape of the curve, not as a
// guarantee for any particular repository.
//
// Why 30s and not 60. It bounds one CALL, and ComputeDoraMetrics stacks
// 1 + len(in-window tags) + len(revertCandidates) of them on a request served
// by a server whose WriteTimeout is deliberately 0 (#1563 is that aggregate).
// Every second here is multiplied by that count, so the value is chosen against
// the stack rather than against the largest repository imaginable.
//
// #1563 supplied the aggregate and did NOT revisit this number, which is worth
// stating because the relation now runs the other way too: an operation budget
// smaller than this ceiling would silently narrow it for every caller that
// stacks, so services.doraGitBudget is derived from it rather than picked
// beside it (the relation is pinned in core/cmd/irrlichd, the one package that
// depends on both — see HistoryCeiling below).
//
// And it is a TIME bound rather than a work bound, which #1553 asked to have
// argued rather than assumed. Both work bounds that issue proposed were
// measured and rejected:
//
//   - `--max-count` does not bound the work of a `--grep` scan at all. It caps
//     the commits OUTPUT, after filtering, so git still walks everything
//     looking for matches it never finds. Measured at 100k commits with no
//     matching commit: 1921.8ms with `--max-count=10` against 1923.5ms without.
//     "This repo has no reverts" is the ordinary case, so the option leaves
//     precisely the worst case untouched.
//   - `--since` derived from the DORA window changes the NUMBER, not only its
//     cost. LeadTime is the median of (release time - commit author time) over
//     a release's commits, so dropping the commits authored before the window
//     start drops the LONGEST lead times first: the median moves down and is
//     still published as Available:true. That is the biased-sample failure
//     gitMaxOutput's doc argues must never happen, arriving through a third
//     door.
//
// What DOES bound the work is the one bound that costs no semantics, and it is
// #1553's other half: ComputeDoraMetrics no longer asks for the commits of tags
// outside its window, because nothing reads them — the domain filters both
// LeadTime and DetectReverts through filterRange.
const gitHistoryTimeout = 30 * time.Second

// HistoryCeiling is gitHistoryTimeout under an exported name, and it exists for
// exactly one reader: the test in core/cmd/irrlichd that pins
// services.DoraGitBudget against it (#1563).
//
// A second spelling of one number is a drift risk this repo normally refuses,
// so note that this one cannot drift — it is a constant DEFINED as the other,
// not a copy of its value — and that the alternative was worse. The aggregate
// budget lives in application/services, which core/architecture_test.go forbids
// from importing an adapter, so services cannot read gitHistoryTimeout at all;
// without an exported name the relation between the two would be a sentence in
// a doc comment, which is precisely the "number that documents behaviour but is
// not produced by it" AGENTS.md records drifting twice in two PRs.
const HistoryCeiling = gitHistoryTimeout

// costProfile names which of the two ceilings above a shellout runs under. It
// is a REQUIRED argument of run() rather than a default the call sites can
// inherit by omission, because picking wrong is silent in both directions: a
// history walk on the short ceiling is #1553 reintroduced, and an enrichment
// read on the long one stalls the detector loop for 30s. Deriving the profile
// from the subcommand was the alternative and was rejected — `git log -1
// --format=%s` is an entirely plausible future enrichment read, and it would
// inherit the history ceiling from its argv with nobody deciding that.
type costProfile int

const (
	// fixedCost is a shellout whose cost does not grow with the number of
	// commits — or, for `tag --contains`, grows ~20x more slowly than the two
	// history walks because it reads the commit graph rather than every commit
	// message (see gitTimeout, which carries that measurement).
	fixedCost costProfile = iota
	// historyCost is a shellout that reads every commit MESSAGE in its range.
	// There are exactly two and both are `git log`.
	historyCost
)

// gitMaxOutput caps how much of git's stdout is held in memory. The ceilings
// above bound how long git runs, not how much it writes in that time, and this
// runs inside a long-lived daemon: `git log --pretty=format:...%B` over full
// history measured 2,611,982 bytes on this repo — over the 1,386 commits
// reachable from HEAD, which is the population that command actually walks, so
// ~1.9 KB per commit. (It said "~810 bytes/commit" until #1553, which divided
// that same byte count by the 3,192-commit ALL-REFS population: two different
// populations, and every extrapolation from it was ~2.3x low.) So a
// 100k-commit repo is ~190 MB and a 1M-commit one ~1.9 GB, all buffered by
// cmd.Output() before #1543 with nothing to stop it.
//
// Which of the two bounds actually binds for CommitsInRange at scale is worth
// knowing before reading #1553 as more than it is: 64 MiB is reached at
// ~36,000 commits in a single range at that rate, while the 30s ceiling is not
// reached until ~1M. Raising the ceiling therefore restores RevertedCommits,
// whose output is only the MATCHING bodies, and leaves CommitsInRange's real
// wall where it was — #1564.
//
// run() reports overflow as a NON-ANSWER rather than truncating, which is the
// opposite of cliprobe's choice and deliberately so: a truncated version banner
// still contains the version, whereas a truncated commit list silently drops
// releases and reverts, and DORA would then publish a smaller sample as though
// it were the whole one. "I could not read it all" is a fact; a biased median
// presented as Available:true is not.
const gitMaxOutput = 64 << 20

// gitCmd builds one bounded git child process. Injected so a test can arrange
// the one distinction that matters here and that no faked return value can
// pin: a child that RAN to a normal exit versus one killed or never started.
// The shape is processlifecycle's shelloutCmd (#1524/#1538) with the two
// arguments every git call varies.
type gitCmd func(ctx context.Context, dir string, args ...string) *exec.Cmd

// Adapter implements ports/outbound.GitResolver using local git commands and
// transcript file inspection.
//
// Its four fields are the adapter's four test seams, and they share ONE shape
// rather than four: each is read through an accessor that falls back to the
// production value, so the zero value of the struct is the production adapter
// and no seam can be left in a state the daemon would never build. #1554 is
// why that uniformity is worth stating — the binary was the one field-less
// seam, so the only way to drive the production builder at a stub was to
// mutate the package var `gitPath`, which every parallel test in the package
// shares. #1553's second ceiling took the same shape rather than inventing a
// fifth.
type Adapter struct {
	// cmd is execGitCmd unless a test injected a fake child. Injected so a
	// test can arrange the one distinction no faked return value can pin: a
	// child that RAN to a normal exit versus one killed or never started.
	cmd gitCmd
	// timeout is gitTimeout unless a test lowered it. Injected because driving
	// the ceiling means waiting it out, and two tests waiting out 5s each is
	// both slow and the most load-sensitive thing in the package.
	// TestProductionAdapterIsBounded deliberately does NOT use this seam: it
	// goes through New(), so the real constant stays proven.
	timeout time.Duration
	// historyTimeout is gitHistoryTimeout unless a test lowered it — #1553's
	// second ceiling. It is its own field rather than a multiple of timeout
	// because the only way to observe that a history walk is NOT bounded by
	// the enrichment ceiling is to drive the two at different values in one
	// adapter (TestTheTwoCeilingsAreIndependentAtRuntime), and a multiple
	// cannot express that. Waiting out the real 30s is not a cost this suite
	// can pay, so the production wiring is proven by the DEADLINE the
	// production adapter sets rather than by outliving it
	// (TestEachShelloutRunsUnderTheCeilingForItsCostProfile).
	historyTimeout time.Duration
	// path is gitPath unless a test injected another executable — the seam
	// #1554 added, and the reason it exists is the one the other two already
	// had: TestProductionAdapterIsBounded must point the PRODUCTION builder at
	// a stalling stub, and the alternative it used to take was assigning to
	// the package var while calling t.Parallel(). That was clean only for as
	// long as no other parallel test called New(), an invariant nothing
	// enforced.
	path string
}

// New returns a new git Adapter.
//
// It names the three production values it can name even though the accessors
// below would supply the same ones from the zero value, so what the daemon
// runs stays readable here rather than only assembled from fallbacks. The
// builder is the exception and is left to builder(): naming it would mean
// storing a method value bound to this very Adapter (`a.cmd = a.execGitCmd`),
// and a copy of that struct would then keep running the ORIGINAL's binary.
func New() *Adapter {
	return &Adapter{timeout: gitTimeout, historyTimeout: gitHistoryTimeout, path: gitPath}
}

// execGitCmd is the production builder: git, resolved through pathutil rather
// than the inherited PATH (go:S4036), run in dir.
//
// It is a METHOD rather than the package-level function it was until #1554,
// because the executable it runs is now the adapter's own field. That is the
// whole of the fix: a test injects into the value it built instead of into
// state every other test in the package shares.
func (a *Adapter) execGitCmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, a.binary(), args...)
	cmd.Dir = dir
	return cmd
}

// builder is the adapter's command builder, defaulting to the production one
// so that an Adapter built by a struct literal shells out to real git rather
// than panicking on a nil func — the same polarity ceiling() and binary()
// take, and the reason New() can leave the field unset (see New()).
func (a *Adapter) builder() gitCmd {
	if a.cmd != nil {
		return a.cmd
	}
	return a.execGitCmd
}

// ceiling is the adapter's fixed-cost timeout, defaulting to gitTimeout so a
// zero-valued Adapter (a struct literal in a test) is still bounded rather
// than unbounded — the failure mode #1543 is about.
func (a *Adapter) ceiling() time.Duration {
	if a.timeout > 0 {
		return a.timeout
	}
	return gitTimeout
}

// historyCeiling is the adapter's timeout for the two history walks,
// defaulting to gitHistoryTimeout exactly as ceiling() defaults to gitTimeout.
// The fallback matters more here than the symmetry does: every helper in this
// package's tests predates #1553 and sets only `timeout`, so without it a
// history walk in any of them would run under a ZERO ceiling — a
// context.WithTimeout that fires immediately, i.e. every history read a
// non-answer for a reason that has nothing to do with git.
func (a *Adapter) historyCeiling() time.Duration {
	if a.historyTimeout > 0 {
		return a.historyTimeout
	}
	return gitHistoryTimeout
}

// ceilingFor is the single place a cost profile chooses a ceiling, so the two
// accessors above stay one-per-field and the mapping stays one expression
// rather than one per call site.
func (a *Adapter) ceilingFor(cost costProfile) time.Duration {
	if cost == historyCost {
		return a.historyCeiling()
	}
	return a.ceiling()
}

// binary is the git executable this adapter runs, defaulting to gitPath
// exactly as ceiling() defaults to gitTimeout. An empty path would otherwise
// reach exec.CommandContext as "", which fails with a non-answer on every
// call — a zero-valued Adapter that silently resolves NOTHING.
func (a *Adapter) binary() string {
	if a.path != "" {
		return a.path
	}
	return gitPath
}

// run executes `git args...` in dir under the ceiling cost names, and reports
// whether git ANSWERED — as opposed to never having been asked.
//
// ctx is the CALLER'S budget, and it is a required argument for the reason
// costProfile is: an operation that stacks calls has to decide where its
// aggregate comes from, and a default would let it decide by omission. Two
// deadlines are in play and the shorter always wins — this adapter's own
// ceiling, derived here per call so a caller with no aggregate is still bounded
// (#1543), and whatever the caller brought (#1563). A caller that deliberately
// has none says so by name rather than by silence; services.noGitBudget is that
// name, and its doc carries which paths are on it and why.
//
// An expired ctx arriving here is a NON-answer, not an empty one, and it costs
// no new code: os/exec fails before Start, the error is not an *exec.ExitError
// at all, and shellout.Answered already reports false for it (measured — see
// that predicate's doc). That is what lets an abandoned sub-call compose with
// #1543's polarity instead of needing a fourth state.
//
// answered is false for exactly three things: the ceiling killed the child,
// the child never started (git missing, fork failure), or its output exceeded
// gitMaxOutput. It is TRUE for every non-zero exit git produces on its own,
// because git has no exit status meaning "I could not look": measured on git
// 2.50.1 / Apple Git-155 (darwin/arm64), not-a-repo is 128 for all six
// subcommands this adapter runs, an unborn branch's rev-parse is 128, a range
// naming an unknown ref is 128, `tag --contains` with an unresolvable object
// name is 129, and "no tags" / "no reverts" are 0 with empty stdout. That is
// why shellout.Answered is called in its EMPTY-variadic form here — the same
// form, for the same reason, that plutil uses (#1524).
//
// One non-answer is PERMANENT rather than transient, and callers must not
// reason as if retrying fixes it: when dir no longer exists, os/exec fails at
// chdir BEFORE the child is started, so there is no exit status and this
// reports answered=false forever. Cleaned-up worktrees are the ordinary case
// here — GetGitRoot and GetProjectName survive it because they walk up to the
// nearest existing ancestor first (nearestExistingDir), and the other six
// deliberately do not, because walking up would answer about a DIFFERENT
// directory's branch or history. Measured across all eight methods in #1551's
// QA.
//
// The limit that reading buys: a repo whose objects are corrupt, or whose
// .git is on a filesystem returning EIO, also exits 128, so this reports it as
// "git answered: not a repo". That is the reading git itself gives (it prints
// `fatal:`), and it is the pre-existing behaviour; the distinction #1543 is
// about is "git could not be RUN" versus "git ran and said no", and that is
// the line drawn here.
func (a *Adapter) run(ctx context.Context, cost costProfile, dir string, args ...string) (out []byte, answered bool) {
	if dir == "" {
		// Nothing was asked, so nothing failed — the reading processTTYVia
		// gives a non-positive pid. Deciding it here rather than at each
		// method keeps it one fact: it is a property of starting a child, not
		// of any particular git subcommand.
		return nil, true
	}
	ctx, cancel := context.WithTimeout(ctx, a.ceilingFor(cost))
	defer cancel()

	cmd := a.builder()(ctx, dir, args...)
	cmd.WaitDelay = shellout.WaitDelay
	// Explicit rather than load-bearing: a nil Stdin is already /dev/null, so
	// os/exec never hands the daemon's own stdin to the child. Spelled out so
	// a later edit does not "helpfully" wire one up — a git that decided to
	// prompt (a credential helper, a hook) would then block until the ceiling.
	cmd.Stdin = nil
	buf := shellout.CappedBuffer{Limit: gitMaxOutput}
	cmd.Stdout = &buf
	// stderr is left nil (i.e. /dev/null) rather than captured: nothing here
	// reads it, and a pipe nobody drains is a way to block.
	err := cmd.Run()

	if !shellout.Answered(err) {
		return nil, false
	}
	if buf.Overflowed() {
		return nil, false
	}
	if err != nil {
		// git RAN and reported a failure. That is an answer — "not a repo",
		// "no such ref" — but its stdout is NOT the answer's content, and
		// returning it is a defect in both directions:
		//
		//   - `git rev-parse HEAD` on an unborn branch exits 128 and writes
		//     the literal string "HEAD" to STDOUT (measured, git 2.50.1 /
		//     Apple Git-155, darwin/arm64). Forwarding that made
		//     GetHeadCommit report "HEAD" as a commit SHA, which
		//     CaptureYieldOnReady then persisted as YieldProductive.
		//   - A `git log` that streams commits and then hits a fatal (a
		//     corrupt object) exits 128 having already written a PREFIX of
		//     the history. Forwarding it hands DORA a silently truncated
		//     commit list — the exact harm gitMaxOutput's doc argues must
		//     never happen, arriving through a different door.
		//
		// Discarding it restores what .Output()'s `if err != nil` arm did
		// before #1543, while keeping the non-answer distinction that arm
		// could not express.
		return nil, true
	}
	return buf.Bytes(), true
}

// GetBranch returns the current git branch for the given working directory.
//
// answered is false when git could not be RUN at all (see run). It is TRUE
// with an empty branch for every case where git answered and there is no
// branch to report: dir is not a repo, HEAD is detached, or the branch is
// unborn. Callers must not overwrite a branch they already hold on a
// non-answer — an empty string there carries no information (#1485/#1543).
func (a *Adapter) GetBranch(ctx context.Context, dir string) (string, bool) {
	out, answered := a.run(ctx, fixedCost, dir, gitRevParseCmd, "--abbrev-ref", "HEAD")
	if !answered {
		return "", false
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" || branch == "HEAD" {
		return "", true
	}
	// Claude Code worktree branches are named "worktree-<slug>" — strip the prefix.
	branch = strings.TrimPrefix(branch, "worktree-")
	return branch, true
}

// GetHeadCommit returns the full SHA of the current HEAD commit for the given
// working directory. An empty SHA with answered=true means git ran and there
// is no HEAD — not a git repo, or an unborn branch with no commits yet (#373).
// answered=false means git could not be run, which is not evidence about
// either.
func (a *Adapter) GetHeadCommit(ctx context.Context, dir string) (string, bool) {
	out, answered := a.run(ctx, fixedCost, dir, gitRevParseCmd, "HEAD")
	if !answered {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// RevertedCommits returns the full SHAs reverted in the repo containing dir,
// parsed from the "This reverts commit <sha>." trailer that `git revert`
// writes. It scans all reachable history (`--all`), so a revert on any branch
// counts — the documented v1 behavior (#373).
//
// A nil slice with answered=true means git ran and this repo has no reverts
// (or is not a repo at all): a sweep over many projects must tolerate non-git,
// permission-denied and missing directories without failing, and git reports
// all of those as ordinary non-zero exits. answered=false means the scan did
// not happen, which is NOT "no reverts" — treating it as such is what makes a
// yield sweep report "nothing flipped" for a repo it never read (#1543).
func (a *Adapter) RevertedCommits(ctx context.Context, dir string) ([]string, bool) {
	out, answered := a.run(ctx, historyCost, dir, "log", "--all", "--grep", "^This reverts commit ", "--pretty=format:%b")
	if !answered {
		return nil, false
	}
	matches := revertTrailer.FindAllSubmatch(out, -1)
	if len(matches) == 0 {
		return nil, true
	}
	shas := make([]string, 0, len(matches))
	for _, m := range matches {
		shas = append(shas, string(m[1]))
	}
	return shas, true
}

// ListReleaseTags returns every release tag in the repo containing dir,
// oldest-first by creation date (#951). A nil slice with answered=true means
// git ran and there are no release tags (or dir is not a repo);
// answered=false means git could not be run, and reporting that as "no
// releases found for this project" is the false claim #1543 removes.
func (a *Adapter) ListReleaseTags(ctx context.Context, dir string) ([]dora.TagInfo, bool) {
	out, answered := a.run(ctx, fixedCost, dir, "for-each-ref", "--sort=creatordate", "--format=%(refname:short)%09%(creatordate:unix)", "refs/tags")
	if !answered {
		return nil, false
	}
	var tags []dora.TagInfo
	for _, line := range strings.Split(string(out), "\n") {
		name, epochStr, ok := strings.Cut(line, "\t")
		if !ok || !releaseTagPattern.MatchString(name) {
			continue
		}
		epoch, err := strconv.ParseInt(epochStr, 10, 64)
		if err != nil {
			continue
		}
		tags = append(tags, dora.TagInfo{Name: name, Epoch: epoch})
	}
	return tags, true
}

// CommitsInRange returns the commits reachable from toRef but not fromRef
// (fromRef empty walks toRef's entire history — for the oldest release
// tag, which has no predecessor) (#951).
//
// answered=false is the case DORA must not average over: a partial failure is
// worse than a total one, because a median lead time computed over the tags
// that happened to succeed is a biased number reported as Available:true.
func (a *Adapter) CommitsInRange(ctx context.Context, dir, fromRef, toRef string) ([]dora.CommitInfo, bool) {
	// toRef, unlike dir, is still guarded HERE: an empty one would become an
	// empty argv entry rather than no call at all. run() handles the dir case.
	if toRef == "" {
		return nil, true
	}
	rangeSpec := toRef
	if fromRef != "" {
		rangeSpec = fromRef + ".." + toRef
	}
	// \x01/\x02 are ASCII control bytes used as record/field separators —
	// they won't collide with real commit message content, so a multi-line
	// %B body can be parsed without spawning a process per commit.
	out, answered := a.run(ctx, historyCost, dir, "log", "--pretty=format:%x01%H%x02%at%x02%B", rangeSpec)
	if !answered {
		return nil, false
	}
	var commits []dora.CommitInfo
	for _, record := range bytes.Split(out, []byte{0x01}) {
		if len(record) == 0 {
			continue
		}
		parts := bytes.SplitN(record, []byte{0x02}, 3)
		if len(parts) != 3 {
			continue
		}
		epoch, err := strconv.ParseInt(string(parts[1]), 10, 64)
		if err != nil {
			continue
		}
		commits = append(commits, dora.CommitInfo{Hash: string(parts[0]), AuthorEpoch: epoch, Body: string(parts[2])})
	}
	return commits, true
}

// TagContaining returns the earliest release tag (by creation date) that
// contains hash (#951). Used to resolve which release shipped the original
// commit a revert commit targets.
//
// An empty tag with answered=true means git ran and no release contains it —
// the commit was never released, dir is not a repo, or the object name does
// not resolve (measured: exit 129). answered=false means git could not be run,
// and treating that as "never released" DROPS the revert from the failure
// list, which makes Change Failure Rate read BETTER than reality — the one
// path in this adapter where the collapse flatters the number (#1543).
func (a *Adapter) TagContaining(ctx context.Context, dir, hash string) (string, bool) {
	// hash, unlike dir, is still guarded HERE — an empty one would become an
	// empty argv entry. run() handles the dir case.
	if hash == "" {
		return "", true
	}
	out, answered := a.run(ctx, fixedCost, dir, "tag", "--contains", hash, "--sort=creatordate")
	if !answered {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if releaseTagPattern.MatchString(line) {
			return line, true
		}
	}
	return "", true
}

// GetGitRoot returns the absolute path of the git repo root for the given
// directory. For worktrees it returns the main repo root (not the worktree
// path). If dir has been deleted (e.g. a cleaned-up worktree), it walks up to
// the nearest existing ancestor so the repo can still be resolved.
//
// An empty root with answered=true means git ran and dir is not inside a
// repository; answered=false means git could not be run, and reporting that to
// a user as "project not found or not a git repository" is a second false
// claim #1543 removes.
func (a *Adapter) GetGitRoot(ctx context.Context, dir string) (string, bool) {
	// This guard must stay, and it is NOT the one run() absorbed:
	// nearestExistingDir("") walks Dir("") == "." and Stat(".") succeeds, so
	// without it an empty dir would resolve to the DAEMON'S OWN working
	// directory and report that repo's root.
	if dir == "" {
		return "", true
	}
	dir = nearestExistingDir(dir)
	out, answered := a.run(ctx, fixedCost, dir, gitRevParseCmd, "--path-format=absolute", "--git-common-dir")
	if !answered {
		return "", false
	}
	gitDir := strings.TrimSpace(string(out))
	if gitDir == "" {
		return "", true
	}
	// gitDir is e.g. "/Users/x/projects/irrlicht/.git"
	root := filepath.Dir(gitDir)
	if root == "" || root == "." || root == "/" {
		return "", true
	}
	return root, true
}

// nearestExistingDir returns dir if it exists, otherwise walks up to the
// nearest existing ancestor directory. Returns "" if no ancestor exists.
func nearestExistingDir(dir string) string {
	for {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// GetProjectName returns the project name for the given directory.
// It uses the git repo root directory name so that sessions in subdirectories
// of the same repo share the same project name.
// Falls back to filepath.Base(dir) if not inside a git repo.
//
// It runs no child process of its own — the second return is GetGitRoot's,
// forwarded. That forwarding is the point rather than bookkeeping: on a
// non-answer the returned name is the directory basename, which for a worktree
// is the WORKTREE's name rather than the repo's, and the one consumer that
// caches this (filesystem.ConcurrencyTracker) memoises for the daemon's
// lifetime. A guess cached forever as though it were resolved is the same
// defect one layer up (#1528's "memoize a non-answer as a non-answer").
func (a *Adapter) GetProjectName(ctx context.Context, dir string) (string, bool) {
	root, answered := a.GetGitRoot(ctx, dir)
	if root != "" {
		return filepath.Base(root), answered
	}
	// Fallback for non-git directories.
	if dir == "" {
		return "", answered
	}
	name := filepath.Base(dir)
	if name == "." || name == "/" || name == "" {
		return "", answered
	}
	return name, answered
}

// GetCWDFromTranscript extracts the working directory from a transcript file.
// It reads the last ~32KB and returns the LAST cwd found, which reflects the
// agent's current working directory (important for worktree switches).
func (a *Adapter) GetCWDFromTranscript(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	file, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer file.Close()

	// Read from the tail of the file to find the latest CWD.
	stat, err := file.Stat()
	if err != nil {
		return ""
	}
	const maxTail = 32 * 1024
	startPos := int64(0)
	if stat.Size() > maxTail {
		startPos = stat.Size() - maxTail
	}
	if _, err := file.Seek(startPos, 0); err != nil {
		return ""
	}

	var lastCWD string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var data map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &data); err != nil {
			continue
		}
		if cwd := transcript.ExtractCWDFromLine(data); cwd != "" {
			lastCWD = cwd
		}
	}
	if lastCWD == "" {
		lastCWD = cwdFromFallbackSidecars(transcriptPath)
	}
	return lastCWD
}

// cwdFromFallbackSidecars tries each agent-specific sidecar/index convention
// in turn, stopping at the first that resolves a cwd. These are the agents
// whose transcript lines never carry a cwd at all, so GetCWDFromTranscript's
// main scan above always comes up empty for them.
func cwdFromFallbackSidecars(transcriptPath string) string {
	extractors := []func(string) string{
		// Some agents (Kiro CLI) record the cwd only in a metadata sidecar
		// next to the transcript, never in the JSONL lines themselves.
		transcript.ExtractCWDFromSidecar,
		// Antigravity records only its sandbox scratch dir in the transcript
		// body; the real workspace lives in the sibling history.jsonl index,
		// keyed by conversationId (no-op for non-antigravity paths).
		transcript.ExtractCWDFromAntigravityHistory,
		// mistral-vibe never writes cwd into messages.jsonl, and its meta.json
		// sidecar doesn't follow the generic same-basename convention above
		// (fixed filename, cwd nested under environment.working_directory) —
		// see issue #906 (presession promotion can't CWD-match without this).
		transcript.ExtractCWDFromVibeMetaJSON,
	}
	for _, extract := range extractors {
		if cwd := extract(transcriptPath); cwd != "" {
			return cwd
		}
	}
	return ""
}

// GetBranchFromTranscript tries to extract the gitBranch field from the last
// few lines of a Claude Code transcript file.
func (a *Adapter) GetBranchFromTranscript(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	file, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > 10 {
			lines = lines[1:]
		}
	}

	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if !strings.Contains(line, "gitBranch") {
			continue
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(line), &data); err == nil {
			if branch, ok := data["gitBranch"].(string); ok && branch != "" {
				return branch
			}
		}
	}
	return ""
}
