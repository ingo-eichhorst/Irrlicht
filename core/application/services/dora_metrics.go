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

	// Every read below is all-or-nothing, and that is the point rather than
	// caution. A PARTIAL failure is worse than a total one here: dropping one
	// tag's commits still yields a median lead time, and dropping one revert's
	// containing tag still yields a Change Failure Rate — each computed over
	// whatever happened to succeed and published as Available:true. A number
	// that is quietly biased is harder to notice than a chart that says it
	// could not be drawn (#1543).
	commitsByTag := make(map[string][]dora.CommitInfo, len(tags))
	for i, tag := range tags {
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
// answered is false only when EVERY candidate cwd went unread, which is the
// case that must not reach the user as "not a git repository". A single cwd
// that resolves is enough to answer the question being asked, so one unread
// candidate among several is not a failure — it is why the flag is computed
// over the loop rather than returned from inside it.
func resolveDoraProjectRoot(git doraGitProbe, sessions doraSessionLister, project string) (string, bool, error) {
	all, err := sessions.ListAll()
	if err != nil {
		return "", true, err
	}
	asked, unread := 0, 0
	for _, st := range all {
		if st == nil || st.ProjectName != project || st.CWD == "" {
			continue
		}
		asked++
		root, answered := git.GetGitRoot(st.CWD)
		if !answered {
			unread++
			continue
		}
		if root != "" {
			return root, true, nil
		}
	}
	return "", asked == 0 || unread < asked, nil
}
