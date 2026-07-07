// Package etaresearch is a read-only replay harness that scores task-completion
// ETA estimators against recorded sessions (issue #753). It replays each
// marker-bearing transcript turn-by-turn, runs every candidate estimator at the
// turn's transcript timestamp (never wall-clock, so results are deterministic),
// and compares the projected remaining time against the ground-truth completion
// — the working→waiting/ready transition. The corpus is the committed claudecode
// replay fixtures plus, optionally, real local transcripts pointed at by
// IRRLICHT_ETA_CORPUS (never committed). Claude Code transcripts only: that is
// the parser the harness drives and where the real local corpus lives.
package etaresearch

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/application/replayengine"
	"irrlicht/core/domain/session"
	"irrlicht/core/pkg/tailer"
)

// markerBytes is the literal an in-band ETA marker always contains; a transcript
// without it carries no estimate to score.
var markerBytes = []byte("irrlicht-eta")

// Turn is one observation point during a task episode: the agent's estimate at a
// transcript turn plus the exact inputs the production forecaster sees.
type Turn struct {
	VirtualUnix    int64
	Est            *session.TaskEstimate // marker estimate at this turn
	Base           *session.TaskEstimate // the task's first marker (rate anchor)
	ElapsedSeconds int64
	Tokens         int64
}

// Episode is a single task within a session: the marker turns from the task's
// first marker through its completion, plus the ground-truth wall-clock end.
//
// Ground truth is the LAST marker, not the working→waiting/ready transition.
// The issue named the transition as the candidate target; replaying the real
// corpus showed it is idle-contaminated — it fires when the user next returns,
// a median 3.5min and up to ~20h after the agent actually stopped advancing the
// task. The last marker is the agent's final progress report and lands when the
// work stops, so it is the clean, idle-free completion signal.
type Episode struct {
	Source        string // transcript path
	Turns         []Turn
	ActualEndUnix int64 // ground truth: the episode's last marker timestamp
	Reached       bool  // completed==total was reached → last marker IS the completion (clean accuracy subset)
}

// FirstUnix is the episode's first marker timestamp (the task's start).
func (e Episode) FirstUnix() int64 {
	if len(e.Turns) == 0 {
		return 0
	}
	return e.Turns[0].VirtualUnix
}

// DiscoverTranscripts walks root for Claude Code transcripts (*.jsonl) that
// carry an ETA marker. events.jsonl (the daemon's observation log, not a
// transcript) and replay goldens are skipped. A missing root yields no files,
// not an error, so callers can pass an unset corpus dir freely.
func DiscoverTranscripts(root string) []string {
	if root == "" {
		return nil
	}
	var out []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr — an unreadable entry is skipped, not fatal
		}
		name := d.Name()
		if name == "events.jsonl" || !strings.HasSuffix(name, ".jsonl") {
			return nil
		}
		if fileContainsMarker(p) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func fileContainsMarker(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Contains(b, markerBytes)
}

// LoadEpisodes replays every transcript and segments each into task episodes. A
// transcript that fails to replay is skipped (one bad file must not sink a
// corpus run). Order is deterministic in the input slice order.
func LoadEpisodes(transcripts []string) []Episode {
	var eps []Episode
	for _, p := range transcripts {
		eps = append(eps, episodesFromTranscript(p)...)
	}
	return eps
}

func episodesFromTranscript(path string) []Episode {
	res, err := replayengine.ReplayTranscript(path, replayengine.Options{
		Adapter:                    "claude-code",
		Parser:                     &claudecode.Parser{},
		DisableModelConfigFallback: true,
		EmitMetricsTimeline:        true,
	})
	if err != nil || res == nil {
		return nil
	}

	// Segment by the tailer's own task anchor (the base re-anchors on a new
	// task / user message), so an episode is exactly one task's marker run.
	var eps []Episode
	var cur *Episode
	curAnchor := int64(-1)
	for _, s := range res.MetricsTimeline {
		est := toDomainEstimate(s.TaskEstimate)
		if est == nil {
			continue
		}
		base := toDomainEstimate(s.TaskEstimateBase)
		anchor := est.UpdatedAt
		if base != nil {
			anchor = base.UpdatedAt
		}
		eps, cur, curAnchor = advanceEpisode(eps, cur, curAnchor, anchor, path)
		cur.Turns = append(cur.Turns, Turn{
			VirtualUnix:    s.VirtualTime.Unix(),
			Est:            est,
			Base:           base,
			ElapsedSeconds: s.Metrics.ElapsedSeconds,
			Tokens:         s.Metrics.TotalTokens,
		})
	}
	if cur != nil && len(cur.Turns) > 0 {
		eps = append(eps, *cur)
	}
	for i := range eps {
		finalizeEpisode(&eps[i])
	}
	return eps
}

// advanceEpisode closes out the current episode and opens a new one when the
// task anchor changes (a new task / user message), matching the tailer's own
// re-anchoring. It is a no-op — returning cur and curAnchor unchanged — when
// the current episode is still open for this anchor.
func advanceEpisode(eps []Episode, cur *Episode, curAnchor int64, anchor int64, path string) ([]Episode, *Episode, int64) {
	if cur != nil && anchor == curAnchor {
		return eps, cur, curAnchor
	}
	if cur != nil && len(cur.Turns) > 0 {
		eps = append(eps, *cur)
	}
	return eps, &Episode{Source: path}, anchor
}

// finalizeEpisode sets the ground-truth completion to the episode's last marker
// and flags whether the task reached completed==total (see Episode doc for why
// the last marker, not the working→terminal transition, is the target).
func finalizeEpisode(ep *Episode) {
	last := ep.Turns[len(ep.Turns)-1]
	ep.ActualEndUnix = last.VirtualUnix
	ep.Reached = last.Est.CompletedRounds >= last.Est.TotalRounds
}

func toDomainEstimate(e *tailer.TaskEstimate) *session.TaskEstimate {
	if e == nil {
		return nil
	}
	return &session.TaskEstimate{
		TotalRounds:     e.TotalRounds,
		CompletedRounds: e.CompletedRounds,
		Risk:            e.Risk,
		Confidence:      e.Confidence,
		UpdatedAt:       e.ObservedAt,
		Source:          session.MarkerEstimateSource,
	}
}

func unix(sec int64) time.Time { return time.Unix(sec, 0) }
