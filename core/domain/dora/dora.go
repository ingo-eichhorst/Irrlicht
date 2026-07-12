// Package dora computes DORA (DevOps Research and Assessment) metrics —
// Deployment Frequency, Lead Time for Changes, Change Failure Rate, and Mean
// Time to Restore — from release tags and commit history. Pure functions,
// no I/O: callers (an application service) gather TagInfo/CommitInfo from a
// git adapter and pass them in. Inputs are read-only; no argument is
// mutated.
package dora

import (
	"fmt"
	"regexp"
	"strings"

	"irrlicht/core/domain/stats"
)

// TagInfo is a release tag: its name and creation-time unix epoch. Callers
// must supply tags already filtered to real releases and sorted ascending
// by Epoch — this package does not re-derive either.
type TagInfo struct {
	Name  string
	Epoch int64
}

// CommitInfo is a single commit's hash, author-time unix epoch, and full
// message body (subject + body, as git's %B gives it).
type CommitInfo struct {
	Hash        string
	AuthorEpoch int64
	Body        string
}

// RevertCandidate is a revert commit found while walking a release's
// shipped commits, identified by subject + trailer alone — it does not yet
// know which release shipped the original commit, since resolving that
// needs a live git call (find the earliest tag containing OriginalHash),
// which is the application service's job, not this pure package's.
type RevertCandidate struct {
	RevertTag    string
	OriginalHash string
}

// ResolvedFailure is one instance of a release "failure" that has already
// been correlated to the release that shipped the fix and how long the
// restore took — signal-agnostic, so ChangeFailureRate/MTTR aggregate
// hotfix/revert signals (and, later, an issue-tracker-driven signal) the
// same way. A future GitHub-API adapter for the "bug-labeled issue linked
// to a release" signal would produce more of these and append them to the
// same slice — no change needed here.
type ResolvedFailure struct {
	FixTag       string
	RestoreHours float64
}

// Metric is one computed DORA statistic, ready for wire serialization.
// Available is false when there isn't enough data to compute Value — Message
// then explains why; Value/Unit are zero and should not be rendered.
type Metric struct {
	Value      float64
	Unit       string
	SampleSize int
	Available  bool
	Message    string
}

var (
	revertSubjectPattern = regexp.MustCompile(`(?i)^revert`)
	revertTrailerPattern = regexp.MustCompile(`(?m)^This reverts commit ([0-9a-f]{7,40})`)
)

// filterRange returns the tags whose Epoch falls within [from, to].
func filterRange(tags []TagInfo, from, to int64) []TagInfo {
	var out []TagInfo
	for _, t := range tags {
		if t.Epoch >= from && t.Epoch <= to {
			out = append(out, t)
		}
	}
	return out
}

// indexByName maps each tag's Name to its position in tags.
func indexByName(tags []TagInfo) map[string]int {
	idx := make(map[string]int, len(tags))
	for i, t := range tags {
		idx[t.Name] = i
	}
	return idx
}

// DeploymentFrequency computes releases/week over [from, to] from the tags
// in range. Unavailable with zero or one release in range, or when every
// release in range shares the same instant (a zero time span makes a rate
// undefined).
func DeploymentFrequency(tags []TagInfo, from, to int64) Metric {
	inRange := filterRange(tags, from, to)
	n := len(inRange)
	if n == 0 {
		return Metric{Unit: "per_week", Available: false, Message: "no releases in range"}
	}
	if n == 1 {
		return Metric{Unit: "per_week", SampleSize: 1, Available: false, Message: "only one release in range"}
	}
	spanSeconds := inRange[n-1].Epoch - inRange[0].Epoch
	if spanSeconds <= 0 {
		return Metric{Unit: "per_week", SampleSize: n, Available: false, Message: "releases span too little time to compute a rate"}
	}
	perWeek := float64(n) / (float64(spanSeconds) / (7 * 86400))
	return Metric{Value: perWeek, Unit: "per_week", SampleSize: n, Available: true}
}

// LeadTime computes the median hours from a commit landing (author time) to
// the release that first ships it, for releases in [from, to]. commitsByTag
// holds, for each tag name, the commits walked from the previous tag up to
// it (or from the repo root for the oldest tag) — the caller assembles this
// with one commit-range call per adjacent tag pair. Only commits whose
// AuthorEpoch itself falls in [from, to] count, so a commit landing near the
// range boundary is attributed to when it landed, not when it shipped.
func LeadTime(tags []TagInfo, commitsByTag map[string][]CommitInfo, from, to int64) Metric {
	inRange := filterRange(tags, from, to)
	if len(inRange) == 0 {
		return Metric{Unit: "hours", Available: false, Message: "no releases in range"}
	}
	var hours []float64
	for _, tag := range inRange {
		for _, c := range commitsByTag[tag.Name] {
			if c.AuthorEpoch < from || c.AuthorEpoch > to {
				continue
			}
			hours = append(hours, float64(tag.Epoch-c.AuthorEpoch)/3600)
		}
	}
	if len(hours) == 0 {
		return Metric{Unit: "hours", Available: false, Message: "no commits found for releases in range"}
	}
	median, _ := stats.Median(hours)
	return Metric{Value: median, Unit: "hours", SampleSize: len(hours), Available: true}
}

// DetectHotfixes flags every release in [from, to] that landed within
// windowHours of the immediately preceding release (in the tag's full
// history, which may be outside the range) as a failure — it shipping that
// fast is itself evidence the prior release needed an emergency fix.
// RestoreHours is the gap between the two releases.
func DetectHotfixes(tags []TagInfo, windowHours int, from, to int64) []ResolvedFailure {
	windowSeconds := int64(windowHours) * 3600
	idx := indexByName(tags)
	var out []ResolvedFailure
	for _, tag := range filterRange(tags, from, to) {
		i := idx[tag.Name]
		if i == 0 {
			continue
		}
		delta := tag.Epoch - tags[i-1].Epoch
		if delta >= 0 && delta < windowSeconds {
			out = append(out, ResolvedFailure{FixTag: tag.Name, RestoreHours: float64(delta) / 3600})
		}
	}
	return out
}

// DetectReverts scans each in-range tag's shipped commits (via
// commitsByTag, the same data LeadTime consumes) for git's standard revert
// shape — a subject starting with "revert" and a "This reverts commit
// <hash>." trailer in the body — and returns one candidate per resolvable
// match. unresolved counts revert-looking commits with no trailer (e.g. a
// non-standard `revert:`-style commit) — reported separately rather than
// silently dropped or guessed at.
func DetectReverts(tags []TagInfo, commitsByTag map[string][]CommitInfo, from, to int64) (candidates []RevertCandidate, unresolved int) {
	for _, tag := range filterRange(tags, from, to) {
		for _, c := range commitsByTag[tag.Name] {
			subject := c.Body
			if i := strings.IndexByte(subject, '\n'); i >= 0 {
				subject = subject[:i]
			}
			if !revertSubjectPattern.MatchString(subject) {
				continue
			}
			m := revertTrailerPattern.FindStringSubmatch(c.Body)
			if m == nil {
				unresolved++
				continue
			}
			candidates = append(candidates, RevertCandidate{RevertTag: tag.Name, OriginalHash: m[1]})
		}
	}
	return candidates, unresolved
}

// ResolveRevert turns a RevertCandidate into a ResolvedFailure once the
// application service has looked up which tag first contains OriginalHash
// (originalTag) — empty when unresolvable (the original commit was never
// released) or when it resolves to the same release as the revert itself
// (fixed within the same release — not a cross-release failure). ok is
// false in either case, and the caller should skip the candidate.
func ResolveRevert(tags []TagInfo, candidate RevertCandidate, originalTag string) (ResolvedFailure, bool) {
	if originalTag == "" || originalTag == candidate.RevertTag {
		return ResolvedFailure{}, false
	}
	idx := indexByName(tags)
	revertIdx, ok := idx[candidate.RevertTag]
	if !ok {
		return ResolvedFailure{}, false
	}
	originalIdx, ok := idx[originalTag]
	if !ok {
		return ResolvedFailure{}, false
	}
	restoreHours := float64(tags[revertIdx].Epoch-tags[originalIdx].Epoch) / 3600
	return ResolvedFailure{FixTag: candidate.RevertTag, RestoreHours: restoreHours}, true
}

// ChangeFailureRate is the fraction of releases in [from, to] flagged by any
// signal in failures, deduped by FixTag (a release flagged by more than one
// signal still counts once).
func ChangeFailureRate(tags []TagInfo, failures []ResolvedFailure, from, to int64) Metric {
	inRange := filterRange(tags, from, to)
	if len(inRange) == 0 {
		return Metric{Unit: "percent", Available: false, Message: "no releases in range"}
	}
	unique := make(map[string]struct{}, len(failures))
	for _, f := range failures {
		unique[f.FixTag] = struct{}{}
	}
	pct := float64(len(unique)) / float64(len(inRange)) * 100
	return Metric{
		Value:      pct,
		Unit:       "percent",
		SampleSize: len(inRange),
		Available:  true,
		Message:    fmt.Sprintf("%d of %d releases flagged", len(unique), len(inRange)),
	}
}

// MTTR is the median RestoreHours across failures, one sample per flagged
// instance rather than per unique release — a release flagged by two
// signals contributes two samples, since each is a distinct restore event.
func MTTR(failures []ResolvedFailure) Metric {
	if len(failures) == 0 {
		return Metric{Unit: "hours", Available: false, Message: "no failures detected in range"}
	}
	hours := make([]float64, len(failures))
	for i, f := range failures {
		hours[i] = f.RestoreHours
	}
	median, _ := stats.Median(hours)
	return Metric{Value: median, Unit: "hours", SampleSize: len(failures), Available: true}
}
