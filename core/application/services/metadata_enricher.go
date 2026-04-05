// MetadataEnricher resolves git metadata and computes transcript metrics for
// sessions. It consolidates all CWD/branch/project resolution and metrics
// computation that was previously spread across SessionDetector event handlers.
package services

import (
	"path/filepath"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// MetadataEnricher enriches session state with git metadata and transcript
// metrics. It holds references to GitResolver and MetricsCollector, which were
// previously fields on SessionDetector.
type MetadataEnricher struct {
	git     outbound.GitResolver
	metrics outbound.MetricsCollector
}

// NewMetadataEnricher creates a MetadataEnricher with the given dependencies.
func NewMetadataEnricher(git outbound.GitResolver, metrics outbound.MetricsCollector) *MetadataEnricher {
	return &MetadataEnricher{git: git, metrics: metrics}
}

// EnrichNewSession resolves git metadata and computes initial metrics for a
// newly created session. It prefers CWD from the event (set by process
// scanner), falling back to transcript inspection for file-based sessions.
func (e *MetadataEnricher) EnrichNewSession(state *session.SessionState, ev agent.Event) {
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
	if m, _ := e.metrics.ComputeMetrics(ev.TranscriptPath, ev.Adapter); m != nil {
		state.Metrics = m
	}
}

// RefreshOnActivity refreshes CWD/branch/project from the latest transcript
// content and recomputes metrics. A single transcript read serves both metrics
// and CWD extraction, eliminating the redundant 32KB read that
// GetCWDFromTranscript would perform.
func (e *MetadataEnricher) RefreshOnActivity(state *session.SessionState, transcriptPath string) {
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
func (e *MetadataEnricher) RefreshMetrics(state *session.SessionState) {
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
func (e *MetadataEnricher) BackfillMetadata(states []*session.SessionState) []*session.SessionState {
	var changed []*session.SessionState
	for _, state := range states {
		if state.ProjectName != "" {
			continue
		}
		updated := false
		if state.CWD == "" && state.TranscriptPath != "" {
			if cwd := e.git.GetCWDFromTranscript(state.TranscriptPath); cwd != "" {
				state.CWD = cwd
				updated = true
			}
		}
		if state.CWD != "" {
			if state.ProjectName == "" {
				state.ProjectName = e.git.GetProjectName(state.CWD)
				updated = true
			}
			if state.GitBranch == "" {
				state.GitBranch = e.git.GetBranch(state.CWD)
				updated = true
			}
		}
		if updated {
			changed = append(changed, state)
		}
	}
	return changed
}
