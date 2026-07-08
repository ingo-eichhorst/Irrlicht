// metadataEnricher resolves git metadata and computes transcript metrics for
// sessions. It consolidates all CWD/branch/project resolution and metrics
// computation that was previously spread across SessionDetector event handlers.
package services

import (
	"path/filepath"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// metadataEnricher enriches session state with git metadata and transcript
// metrics. It holds references to GitResolver and MetricsCollector, which were
// previously fields on SessionDetector.
type metadataEnricher struct {
	git     outbound.GitResolver
	metrics outbound.MetricsCollector
}

// newMetadataEnricher creates a metadataEnricher with the given dependencies.
func newMetadataEnricher(git outbound.GitResolver, metrics outbound.MetricsCollector) *metadataEnricher {
	return &metadataEnricher{git: git, metrics: metrics}
}

// CaptureYieldOnReady records the session's HEAD commit and an initial yield
// verdict at the moment it transitions to ready (#373). A git-tracked CWD is
// presumed productive — it shipped a commit — until the yield sweep proves
// otherwise; a non-git CWD is permanently unknown. It never clobbers a session
// already marked reverted, and re-captures HEAD on each ready transition so the
// latest shipped commit wins.
func (e *metadataEnricher) CaptureYieldOnReady(state *session.SessionState) {
	if state == nil || state.YieldState == session.YieldReverted {
		return
	}
	head := e.git.GetHeadCommit(state.CWD)
	if head == "" {
		state.YieldState = session.YieldUnknown
		return
	}
	state.HeadCommit = head
	state.YieldState = session.YieldProductive
}

// PruneMetrics releases per-session metrics state when a session ends.
func (e *metadataEnricher) PruneMetrics(transcriptPath string) {
	if e.metrics == nil || transcriptPath == "" {
		return
	}
	e.metrics.PruneEntry(transcriptPath)
}

// EnrichNewSession resolves git metadata and computes initial metrics for a
// newly created session. It prefers CWD from the event (set by process
// scanner), falling back to transcript inspection for file-based sessions.
func (e *metadataEnricher) EnrichNewSession(state *session.SessionState, ev agent.Event) {
	// Resolve git metadata.
	if ev.CWD != "" {
		state.CWD = ev.CWD
		state.GitBranch = e.git.GetBranch(ev.CWD)
		state.ProjectName = e.git.GetProjectName(ev.CWD)
	} else if ev.TranscriptPath != "" {
		if cwd := e.git.GetCWDFromTranscript(ev.TranscriptPath); cwd != "" {
			state.CWD = cwd
			state.GitBranch = e.git.GetBranch(cwd)
			state.ProjectName = e.git.GetProjectName(cwd)
		} else if b := e.git.GetBranchFromTranscript(ev.TranscriptPath); b != "" {
			state.GitBranch = b
		}
	}

	// Compute initial metrics (no-op for pre-sessions with no transcript).
	// state.Adapter is already populated by the caller in onNewSession
	// before this enricher runs.
	if m, _ := e.metrics.ComputeMetrics(ev.TranscriptPath, state.Adapter); m != nil {
		state.Metrics = m
		// Some adapters (Gemini CLI) record the cwd only in the transcript
		// body — not in a field GetCWDFromTranscript can read, the encoded
		// path, or a sidecar — so the parser is the only source and surfaces
		// it as metrics.LastCWD. Use it here (mirroring RefreshOnActivity) so
		// PID discovery has a cwd at creation instead of never binding on a
		// short session that produces no further activity. Only fills a gap;
		// an already-resolved cwd above wins.
		if state.CWD == "" && m.LastCWD != "" {
			state.CWD = m.LastCWD
			state.GitBranch = e.git.GetBranch(m.LastCWD)
			state.ProjectName = e.git.GetProjectName(m.LastCWD)
		}
	}
}

// RefreshOnActivity refreshes CWD/branch/project from the latest transcript
// content and recomputes metrics. A single transcript read serves both metrics
// and CWD extraction, eliminating the redundant 32KB read that
// GetCWDFromTranscript would perform.
func (e *metadataEnricher) RefreshOnActivity(state *session.SessionState, transcriptPath string) {
	// Refresh metrics first — the tailer now extracts LastCWD during parsing,
	// so we get CWD for free without a separate file read.
	var metricsCWD string
	if m, _ := e.metrics.ComputeMetrics(transcriptPath, state.Adapter); m != nil {
		metricsCWD = m.LastCWD
		state.Metrics = session.MergeMetrics(m, state.Metrics)
	}

	// Update CWD from metrics (preferred) or fallback to dedicated read.
	cwd := metricsCWD
	if cwd == "" && transcriptPath != "" {
		cwd = e.git.GetCWDFromTranscript(transcriptPath)
	}
	if cwd != "" && cwd != state.CWD {
		state.CWD = cwd
		state.GitBranch = e.git.GetBranch(cwd)
		// Only update ProjectName when the new CWD is inside a git repo.
		// For non-git directories, keep the original project name set at
		// session creation to avoid subdirectory names overriding it.
		// However, if ProjectName was never set (initial enrichment failed
		// because the transcript was too new), use the full fallback chain.
		if gitRoot := e.git.GetGitRoot(cwd); gitRoot != "" {
			state.ProjectName = filepath.Base(gitRoot)
		} else if state.ProjectName == "" {
			state.ProjectName = e.git.GetProjectName(cwd)
		}
	}
}

// RefreshMetrics recomputes metrics from the transcript without touching
// CWD/branch/project. Used during startup re-evaluation of persisted states.
func (e *metadataEnricher) RefreshMetrics(state *session.SessionState) {
	if state.TranscriptPath == "" {
		return
	}
	if m, _ := e.metrics.ComputeMetrics(state.TranscriptPath, state.Adapter); m != nil {
		state.Metrics = session.MergeMetrics(m, state.Metrics)
	}
}

// BackfillMetadata fills in missing ProjectName, CWD, and GitBranch for
// sessions saved before these fields were populated. Returns the list of
// states that were updated (caller is responsible for persisting and
// broadcasting).
func (e *metadataEnricher) BackfillMetadata(states []*session.SessionState) []*session.SessionState {
	var changed []*session.SessionState
	for _, state := range states {
		if e.backfillOne(state) {
			changed = append(changed, state)
		}
	}
	return changed
}

// backfillOne fills in state's missing CWD, ProjectName, and GitBranch in
// place (state.ProjectName already set means it was fully backfilled before,
// so it's left untouched). Returns true when any field was updated.
func (e *metadataEnricher) backfillOne(state *session.SessionState) bool {
	if state.ProjectName != "" {
		return false
	}
	updated := false
	if state.CWD == "" && state.TranscriptPath != "" {
		if cwd := e.git.GetCWDFromTranscript(state.TranscriptPath); cwd != "" {
			state.CWD = cwd
			updated = true
		}
	}
	if state.CWD == "" {
		return updated
	}
	if state.ProjectName == "" {
		state.ProjectName = e.git.GetProjectName(state.CWD)
		updated = true
	}
	if state.GitBranch == "" {
		state.GitBranch = e.git.GetBranch(state.CWD)
		updated = true
	}
	return updated
}
