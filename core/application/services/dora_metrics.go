package services

import (
	"context"
	"errors"
	"time"

	"irrlicht/core/domain/dora"
	"irrlicht/core/domain/session"
)

// doraHotfixWindowHours is the fixed threshold for the hotfix-window
// Change Failure Rate signal (#951) — a release landing within this many
// hours of the prior one is treated as an emergency fix. Not user-tunable
// in this iteration.
const doraHotfixWindowHours = 24

// DoraGitBudget is the ONE aggregate deadline covering an entire DORA
// computation: the repo-root resolution, the release-tag listing, one commit
// range per in-window tag, and one `tag --contains` per revert candidate.
//
// It exists because core/adapters/outbound/git bounds one CALL and this
// operation stacks `1 + len(in-window tags) + len(revertCandidates)` of them,
// with nothing bounding the sum, on a handler served by a server whose
// WriteTimeout is deliberately 0 (core/cmd/irrlichd/startup.go). #1553 chose
// its 30s history ceiling AGAINST that stack rather than against the largest
// repository imaginable, which is why the two numbers are one decision and this
// is its other half (#1563).
//
// The value is set by what it has to fit inside — except that here, uniquely,
// there is nothing behind it to fall back on. There is no server-side write
// ceiling, so the only real bound today is how long a browser tab (and its
// user) will hold on. So it is derived from the OTHER direction, from the one
// number that is measured: it must be at least git.HistoryCeiling, or a single
// legitimate history walk on a repository at #1553's design point (1M commits,
// measured at 30.5s) can never complete and the panel is permanently blank for
// exactly the repositories #1553 raised that ceiling for. 2x is the smallest
// multiple that lets a window containing more than one release finish there.
//
// That relation is pinned rather than restated, in core/cmd/irrlichd — the one
// package that depends on both this service and the git adapter, since
// core/architecture_test.go forbids application/services from importing an
// adapter. Which is also why this constant is EXPORTED: nothing else here
// needs it.
//
// It is not the only bound in play at runtime, and the second one is free.
// serveHistoryDoraChart derives this from the REQUEST's context, so a browser
// that gives up — or a daemon shutting down — cancels the git walks instead of
// leaving them to run out this budget against a client nobody is listening on.
const DoraGitBudget = 60 * time.Second

// doraGitProbe is the narrow git surface ComputeDoraMetrics needs, matching
// the yieldGitProbe/historySessionLister convention of small per-consumer
// interfaces rather than a shared, ever-growing one.
//
// Every method takes the aggregate budget explicitly rather than the probe
// holding one, because the alternative is two objects that can disagree: the
// context this operation checks its loops against and the context its children
// actually run under would be separately supplied, and a probe wired to a
// different one would look identical from here. Passing it per call is
// #1529's own shape (resolveClientHostIdentityVia hands ctx to each candidate
// probe) and it welds the two together.
type doraGitProbe interface {
	GetGitRoot(ctx context.Context, dir string) (root string, answered bool)
	ListReleaseTags(ctx context.Context, dir string) (tags []dora.TagInfo, answered bool)
	CommitsInRange(ctx context.Context, dir, fromRef, toRef string) (commits []dora.CommitInfo, answered bool)
	TagContaining(ctx context.Context, dir, hash string) (tag string, answered bool)
}

// doraSessionLister is the narrow read ComputeDoraMetrics needs over the
// session repository, to resolve a project name to a representative
// session's CWD.
type doraSessionLister interface {
	ListAll() ([]*session.SessionState, error)
}

// doraGitUnreadable is the Message for every outcome where git could not be
// RUN. It is one string on purpose: until #1543 each of those outcomes wore
// the message of the ANSWER it was collapsed into — a timed-out for-each-ref
// reached the user as "no releases found for this project", and an unreadable
// repo root as "project not found or not a git repository". Both are claims
// about the repository made by a probe that never reached it.
const doraGitUnreadable = "could not read this project's git history"

// doraGitTooSlow is the Message for the one non-answer #1563 newly admits: the
// aggregate budget ran out with git calls still to make.
//
// A SECOND string, where #1543 deliberately collapsed every unreadable outcome
// into one, and the two arguments do not conflict. #1543's point is that a
// probe which never reached the repository must not wear the message of an
// ANSWER about the repository ("no releases found for this project"). This is
// not an answer about the repository either — it is a different fact about the
// READ, one the user can act on differently: git works fine, this window is too
// expensive, narrow it. Collapsing it into doraGitUnreadable would send someone
// looking for a broken git that is not broken.
//
// It is also the whole of this budget's observability, and deliberately so.
// #1529's abandonment needed a counter (#1558) because it happens inside a
// background sweep nobody is watching; this one is user-visible on the very
// request that produced it, in the panel and in the JSON, so a counter would be
// a second, weaker copy of a fact already on screen.
const doraGitTooSlow = "this project's git history took too long to read — try a narrower window"

// DoraResult is the outcome of ComputeDoraMetrics, ready for the HTTP
// handler to serialize. Available is false when project didn't resolve to
// a git repo, that repo has no release tags, or git could not be read at all
// — Message explains which, and the four metrics are zero values that should
// not be rendered.
type DoraResult struct {
	Available           bool
	Message             string
	DeploymentFrequency dora.Metric
	LeadTime            dora.Metric
	ChangeFailureRate   dora.Metric
	MTTR                dora.Metric
}

// doraUnavailable is the one blank result every read that did not happen
// returns, and the single place the two unreadable messages are told apart.
//
// It keys on the budget rather than on which call failed, because the budget is
// the fact that distinguishes them and the call site cannot see it: an
// abandoned sub-call and a git that could not be run arrive at exactly the same
// `answered == false` (core/adapters/outbound/git's run: an expired context
// fails before Start, so shellout.Answered reports false — see that predicate's
// doc). What matters far more than which message it picks is what it always
// does: Available:false with all four metrics at their zero values. A budget
// that abandoned mid-sequence and published the metrics computed so far would
// be a biased sample presented as complete — a median lead time over the tags
// that happened to fit, a Change Failure Rate missing the reverts nobody got to
// — which is the failure gitMaxOutput's doc and #1553's rejection of `--since`
// both exist to refuse. Blanking is the conservative answer; a smaller sample
// wearing Available:true is a wrong one.
func doraUnavailable(ctx context.Context) DoraResult {
	if budgetSpent(ctx) {
		return DoraResult{Available: false, Message: doraGitTooSlow}
	}
	return DoraResult{Available: false, Message: doraGitUnreadable}
}

// ComputeDoraMetrics computes all four DORA metrics for project (a
// ProjectName, as used by the History tab's existing ?project= filter)
// over [start, end], entirely on request — no persistence, no background
// sweep (unlike YieldSweeper; DORA has no per-session state to mutate).
func ComputeDoraMetrics(ctx context.Context, git doraGitProbe, sessions doraSessionLister, project string, start, end int64) (DoraResult, error) {
	if project == "" {
		return DoraResult{}, errors.New("project is required")
	}
	if git == nil || sessions == nil {
		return DoraResult{Available: false, Message: "git or session data unavailable"}, nil
	}

	// One deadline over the whole sequence, derived HERE rather than taken
	// from the caller, so that every entry point gets it by calling this
	// function instead of by remembering to wrap. The caller's own context
	// still bounds it from outside (a cancelled request cancels this).
	ctx, cancel := context.WithTimeout(ctx, DoraGitBudget)
	defer cancel()

	root, rootAnswered, err := resolveDoraProjectRoot(ctx, git, sessions, project)
	if err != nil {
		return DoraResult{}, err
	}
	if !rootAnswered {
		return doraUnavailable(ctx), nil
	}
	if root == "" {
		return DoraResult{Available: false, Message: "project not found or not a git repository"}, nil
	}

	if budgetSpent(ctx) {
		return doraUnavailable(ctx), nil
	}
	tags, answered := git.ListReleaseTags(ctx, root)
	if !answered {
		return doraUnavailable(ctx), nil
	}
	if len(tags) == 0 {
		return DoraResult{Available: false, Message: "no releases found for this project"}, nil
	}

	// Every read below is all-or-nothing. The load-bearing half of that is
	// never publishing a number computed over "whatever happened to succeed":
	// dropping one tag's commits still yields a median lead time, and dropping
	// one revert's containing tag still yields a Change Failure Rate, each
	// quietly biased and therefore harder to notice than a chart that says it
	// could not be drawn (#1543).
	//
	// #1563 adds one more way for a read not to happen — the aggregate budget
	// running out — and it takes exactly the same exit rather than a gentler
	// one. That is the decision the budget rests on: a deadline that abandoned
	// mid-sequence and published a partial computation would have introduced
	// the biased sample through a fourth door, this time as a LATENCY fix,
	// which is a worse trade than the latency it removes.
	//
	// Blanking ALL FOUR metrics is the conservative part, not the principled
	// part, and it is stated that way rather than defended. dora.Metric already
	// carries its own Available/Message and the handler, the dashboard and the
	// CSV builder all honour it, so a per-metric version is expressible today:
	// a TagContaining non-answer biases ChangeFailureRate and MTTR while
	// DeploymentFrequency (computed from tags alone) was fully read. That is a
	// user-visible behaviour change beyond #1543's scope — a blank panel is
	// conservative but honest, which is what this issue is about — so it is
	// left for a follow-up rather than smuggled in here.
	commitsByTag := make(map[string][]dora.CommitInfo, len(tags))
	for i, tag := range tags {
		// Skip the tags no metric will read. LeadTime and DetectReverts are
		// the only two consumers of commitsByTag and both index it through
		// dora.filterRange, so an out-of-window tag's commits are fetched,
		// parsed, held in memory and then never looked at — and until #1553
		// every release in the repository's history was fetched on every
		// request, however narrow the window. The oldest tag is the expensive
		// one: its range starts at the repo root, so on a project that adopted
		// tags late it is a full-history walk paid for a chart that spans a
		// week. dora.InWindow rather than a local comparison, so this and
		// filterRange cannot disagree about which tags those are.
		//
		// This is a WORK bound, not a time bound, and it is the only one of
		// the three #1553 weighed that costs no semantics: nothing reads what
		// is no longer fetched. (`--max-count` was measured not to bound the
		// work of a `--grep` scan at all, and `--since` biases LeadTime's
		// median downward — both recorded on gitHistoryTimeout.)
		//
		// One user-visible consequence, stated rather than left implied: a
		// repository whose out-of-window history cannot be read no longer
		// blanks the panel. Before this, one unreadable ancient range made
		// every window unavailable; now the panel is blank only when a read
		// the metrics actually needed failed. That is the same polarity as the
		// rest of #1543 — claim nothing about history nobody asked about.
		if !dora.InWindow(tag.Epoch, start, end) {
			continue
		}
		// #1563: the budget is checked HERE, by the loop, rather than left to
		// the children to inherit. An expired deadline does reach them — the
		// adapter derives every call from this context — but inheriting alone
		// produces the WRONG FACT: each remaining range still starts a git that
		// fails instantly, and this reads as "every range was unreadable" where
		// the truth is "I stopped after k". It is also the only way the
		// aggregate becomes a bound a caller can OBSERVE rather than an
		// accident of the children's own ceilings.
		//
		// After the InWindow skip, not before: a tag no metric reads costs
		// nothing, and checking ahead of it would abandon a loop that had no
		// work left to do.
		if budgetSpent(ctx) {
			return doraUnavailable(ctx), nil
		}
		from := ""
		if i > 0 {
			from = tags[i-1].Name
		}
		commits, answered := git.CommitsInRange(ctx, root, from, tag.Name)
		if !answered {
			return doraUnavailable(ctx), nil
		}
		commitsByTag[tag.Name] = commits
	}

	hotfixes := dora.DetectHotfixes(tags, doraHotfixWindowHours, start, end)
	candidates, _ := dora.DetectReverts(tags, commitsByTag, start, end)

	failures := make([]dora.ResolvedFailure, 0, len(hotfixes)+len(candidates))
	failures = append(failures, hotfixes...)
	for _, c := range candidates {
		// The loop checks the budget itself, for the reason the range loop
		// above does. This is also the stage a shared budget STARVES (#1558's
		// shape): the tag listing and the ranges run first and can consume
		// everything, leaving the candidates unread on a repository where the
		// history walks are chronically slow. That is a real cost and it is
		// stated rather than argued away — but note what it costs and what it
		// does not. Starvation here costs AVAILABILITY, never correctness: the
		// panel goes blank with doraGitTooSlow, exactly as an unreadable
		// candidate already blanked it, and no number computed over a subset is
		// ever published. #1529's herdr case had to weigh a wrong answer;
		// this one does not.
		if budgetSpent(ctx) {
			return doraUnavailable(ctx), nil
		}
		originalTag, answered := git.TagContaining(ctx, root, c.OriginalHash)
		if !answered {
			// Silently dropping this candidate would make Change Failure Rate
			// read BETTER than reality — the one collapse in this adapter
			// that flatters the number rather than blanking it.
			return doraUnavailable(ctx), nil
		}
		if resolved, ok := dora.ResolveRevert(tags, c, originalTag); ok {
			failures = append(failures, resolved)
		}
	}

	return DoraResult{
		Available:           true,
		DeploymentFrequency: dora.DeploymentFrequency(tags, start, end),
		LeadTime:            dora.LeadTime(tags, commitsByTag, start, end),
		ChangeFailureRate:   dora.ChangeFailureRate(tags, failures, start, end),
		MTTR:                dora.MTTR(failures),
	}, nil
}

// resolveDoraProjectRoot finds the git repo root for project by scanning
// sessions for a representative CWD, mirroring YieldSweeper.recordRootDir.
// Returns "" with answered=true (not an error) when no session matches or none
// resolves to a git repo — that's a normal "nothing to compute" outcome, not a
// failure.
//
// A single cwd that RESOLVES is enough to answer the question being asked, and
// that case returns from inside the loop. answered therefore only ever reports
// on the remaining case: no candidate produced a root. There, every candidate
// that answered answered NEGATIVELY, so one unread candidate is enough to make
// "project not found or not a git repository" a claim this daemon cannot
// support — which is the class of claim #1543 removes everywhere else. Hence
// `unread == 0` rather than a majority test; it subsumes the no-candidates case,
// where unread is 0 and "nothing to compute" is the honest answer.
func resolveDoraProjectRoot(ctx context.Context, git doraGitProbe, sessions doraSessionLister, project string) (string, bool, error) {
	all, err := sessions.ListAll()
	if err != nil {
		return "", true, err
	}
	unread := 0
	for _, st := range all {
		if st == nil || st.ProjectName != project || st.CWD == "" {
			continue
		}
		// #1563: the candidates left are ones we DECLINED to look at, which is
		// the unread case rather than the "no candidate resolved" case — so it
		// poisons the answer exactly as an unread candidate does, and stops.
		// The loop checks rather than inheriting, for the reason the two loops
		// in ComputeDoraMetrics do.
		if budgetSpent(ctx) {
			return "", false, nil
		}
		root, answered := git.GetGitRoot(ctx, st.CWD)
		if !answered {
			unread++
			continue
		}
		if root != "" {
			return root, true, nil
		}
	}
	return "", unread == 0, nil
}
