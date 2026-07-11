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
	GetGitRoot(dir string) string
	ListReleaseTags(dir string) []dora.TagInfo
	CommitsInRange(dir, fromRef, toRef string) []dora.CommitInfo
	TagContaining(dir, hash string) string
}

// doraSessionLister is the narrow read ComputeDoraMetrics needs over the
// session repository, to resolve a project name to a representative
// session's CWD.
type doraSessionLister interface {
	ListAll() ([]*session.SessionState, error)
}

// DoraResult is the outcome of ComputeDoraMetrics, ready for the HTTP
// handler to serialize. Available is false when project didn't resolve to
// a git repo, or that repo has no release tags — Message explains why, and
// the four metrics are zero values that should not be rendered.
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

	root, err := resolveDoraProjectRoot(git, sessions, project)
	if err != nil {
		return DoraResult{}, err
	}
	if root == "" {
		return DoraResult{Available: false, Message: "project not found or not a git repository"}, nil
	}

	tags := git.ListReleaseTags(root)
	if len(tags) == 0 {
		return DoraResult{Available: false, Message: "no releases found for this project"}, nil
	}

	commitsByTag := make(map[string][]dora.CommitInfo, len(tags))
	for i, tag := range tags {
		from := ""
		if i > 0 {
			from = tags[i-1].Name
		}
		commitsByTag[tag.Name] = git.CommitsInRange(root, from, tag.Name)
	}

	hotfixes := dora.DetectHotfixes(tags, doraHotfixWindowHours, start, end)
	candidates, _ := dora.DetectReverts(tags, commitsByTag, start, end)

	failures := make([]dora.ResolvedFailure, 0, len(hotfixes)+len(candidates))
	failures = append(failures, hotfixes...)
	for _, c := range candidates {
		originalTag := git.TagContaining(root, c.OriginalHash)
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
// Returns "" (not an error) when no session matches or none resolves to a
// git repo — that's a normal "nothing to compute" outcome, not a failure.
func resolveDoraProjectRoot(git doraGitProbe, sessions doraSessionLister, project string) (string, error) {
	all, err := sessions.ListAll()
	if err != nil {
		return "", err
	}
	for _, st := range all {
		if st == nil || st.ProjectName != project || st.CWD == "" {
			continue
		}
		if root := git.GetGitRoot(st.CWD); root != "" {
			return root, nil
		}
	}
	return "", nil
}
