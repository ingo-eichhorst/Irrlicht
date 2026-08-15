package services

import (
	"errors"

	"irrlicht/core/domain/dora"
	"irrlicht/core/domain/session"
)

// doraHotfixWindowHours is the fixed threshold for the hotfix-window
// Change Failure Rate signal (#951) — a release landing within this many
// hours of the prior one is treated as an emergency fix. Not user-tunable
// in this iteration.
const doraHotfixWindowHours = 24

// doraGitProbe is the narrow git surface ComputeDoraMetrics needs, matching
// the yieldGitProbe/historySessionLister convention of small per-consumer
// interfaces rather than a shared, ever-growing one.
type doraGitProbe interface {
	GetGitRoot(dir string) (root string, answered bool)
	ListReleaseTags(dir string) (tags []dora.TagInfo, answered bool)
	CommitsInRange(dir, fromRef, toRef string) (commits []dora.CommitInfo, answered bool)
	TagContaining(dir, hash string) (tag string, answered bool)
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

// ComputeDoraMetrics computes all four DORA metrics for project (a
// ProjectName, as used by the History tab's existing ?project= filter)
// over [start, end], entirely on request — no persistence, no background
// sweep (unlike YieldSweeper; DORA has no per-session state to mutate).
func ComputeDoraMetrics(git doraGitProbe, sessions doraSessionLister, project string, start, end int64) (DoraResult, error) {
	if project == "" {
		return DoraResult{}, errors.New("project is required")
	}
	if git == nil || sessions == nil {
		return DoraResult{Available: false, Message: "git or session data unavailable"}, nil
	}

	root, rootAnswered, err := resolveDoraProjectRoot(git, sessions, project)
	if err != nil {
		return DoraResult{}, err
	}
	if !rootAnswered {
		return DoraResult{Available: false, Message: doraGitUnreadable}, nil
	}
	if root == "" {
		return DoraResult{Available: false, Message: "project not found or not a git repository"}, nil
	}

	tags, answered := git.ListReleaseTags(root)
	if !answered {
		return DoraResult{Available: false, Message: doraGitUnreadable}, nil
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
		from := ""
		if i > 0 {
			from = tags[i-1].Name
		}
		commits, answered := git.CommitsInRange(root, from, tag.Name)
		if !answered {
			return DoraResult{Available: false, Message: doraGitUnreadable}, nil
		}
		commitsByTag[tag.Name] = commits
	}

	hotfixes := dora.DetectHotfixes(tags, doraHotfixWindowHours, start, end)
	candidates, _ := dora.DetectReverts(tags, commitsByTag, start, end)

	failures := make([]dora.ResolvedFailure, 0, len(hotfixes)+len(candidates))
	failures = append(failures, hotfixes...)
	for _, c := range candidates {
		originalTag, answered := git.TagContaining(root, c.OriginalHash)
		if !answered {
			// Silently dropping this candidate would make Change Failure Rate
			// read BETTER than reality — the one collapse in this adapter
			// that flatters the number rather than blanking it.
			return DoraResult{Available: false, Message: doraGitUnreadable}, nil
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
func resolveDoraProjectRoot(git doraGitProbe, sessions doraSessionLister, project string) (string, bool, error) {
	all, err := sessions.ListAll()
	if err != nil {
		return "", true, err
	}
	unread := 0
	for _, st := range all {
		if st == nil || st.ProjectName != project || st.CWD == "" {
			continue
		}
		root, answered := git.GetGitRoot(st.CWD)
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
