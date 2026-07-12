package main

import (
	"fmt"
	"strings"
	"time"

	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

// ghostAgg accumulates the per-session signals that decide whether a session
// was a "ghost" — minted, never did substantive work, then reaped. Computed in
// a second pass over the sidecar events so the JSON report path stays untouched
// (issue #757).
type ghostAgg struct {
	substantive   bool
	removed       bool
	removedAt     time.Time
	removalReason string
	finalReason   string
}

// buildGhostTimelines reuses the standard per-session aggregation and layers
// ghost-detection signals on top for the --ghosts text view. A session is a
// ghost when it was removed yet never showed substantive activity — it never
// bound a PID and never entered working/waiting. The predicate is conservative
// (prefers false negatives): anything that ever got a PID or actively worked is
// not a ghost, even if it is later reaped.
func buildGhostTimelines(events []lifecycle.Event) []sessionTimeline {
	base := buildSessionTimelines(events)

	agg := make(map[string]*ghostAgg, len(base))
	for _, ev := range events {
		g := agg[ev.SessionID]
		if g == nil {
			g = &ghostAgg{}
			agg[ev.SessionID] = g
		}
		applyGhostEvent(ev, g)
	}

	for i := range base {
		g := agg[base[i].SessionID]
		if g == nil || !g.removed {
			continue
		}
		finalizeGhostTimeline(&base[i], g)
	}
	return base
}

// applyGhostEvent folds one lifecycle event into its session's ghost
// aggregate: PID discovery and working/waiting transitions mark the session
// substantive; the first removal edge (process exit, transcript removal, or
// pre-session removal) is kept as the ghost's removal signal.
func applyGhostEvent(ev lifecycle.Event, g *ghostAgg) {
	switch ev.Kind {
	case lifecycle.KindPIDDiscovered:
		g.substantive = true
	case lifecycle.KindStateTransition:
		if ev.NewState == session.StateWorking || ev.NewState == session.StateWaiting {
			g.substantive = true
		}
		if ev.Reason != "" {
			g.finalReason = ev.Reason
		}
	case lifecycle.KindTranscriptRemoved, lifecycle.KindProcessExited, lifecycle.KindPreSessionRemoved:
		// Keep the FIRST removal edge: a HandleProcessExit reap records
		// KindProcessExited and then KindTranscriptRemoved for the same
		// session, and the process-exit edge is the true (earlier) one.
		if !g.removed {
			g.removed = true
			g.removedAt = ev.Timestamp
			g.removalReason = ev.Reason
		}
	}
}

// finalizeGhostTimeline overlays a removed session's ghost aggregate onto its
// timeline row: substantive/removal reasons, removed-at + lifetime, and the
// final IsGhost verdict. Children (subagents) are not ghosts in the
// antigravity PID=0 sense: a subagent reaped via "parent deleted" can lack
// both a PID and a working/waiting transition of its own, yet it did real
// work, so the conservative predicate doesn't false-positive on them.
func finalizeGhostTimeline(t *sessionTimeline, g *ghostAgg) {
	t.HadSubstantive = g.substantive
	t.RemovalReason = g.removalReason
	t.FinalReason = g.finalReason
	if !g.removedAt.IsZero() {
		at := g.removedAt
		t.RemovedAt = &at
		if !t.FirstSeen.IsZero() {
			t.LifetimeMs = at.Sub(t.FirstSeen).Milliseconds()
		}
	}
	t.IsGhost = !g.substantive && t.ParentSessionID == ""
}

// lastTransitionInputs maps each session to the classifier-input snapshot from
// its most recent state transition, so the ghost view can explain *why* the
// classifier reached the session's final state (the Inputs captured in 1a).
func lastTransitionInputs(events []lifecycle.Event) map[string]*lifecycle.ClassifierInputs {
	out := make(map[string]*lifecycle.ClassifierInputs)
	for _, ev := range events {
		if ev.Kind == lifecycle.KindStateTransition && ev.Inputs != nil {
			out[ev.SessionID] = ev.Inputs
		}
	}
	return out
}

// formatClassifierInputs renders the set (non-zero) classifier-input fields as a
// compact comma-separated list. Returns "(none recorded)" when nil — sidecars
// recorded before issue #757 carry no Inputs.
func formatClassifierInputs(in *lifecycle.ClassifierInputs) string {
	if in == nil {
		return "(none recorded)"
	}
	var parts []string
	add := func(name string, set bool) {
		if set {
			parts = append(parts, name)
		}
	}
	add("has_live_background_process", in.HasLiveBackgroundProcess)
	add("permission_pending", in.PermissionPending)
	add("compact_in_progress", in.CompactInProgress)
	add("open_tool_stalled", in.OpenToolStalled)
	add("saw_user_blocking_tool_closed", in.SawUserBlockingToolClosedThisPass)
	add("saw_manual_compact_boundary", in.SawManualCompactBoundary)
	add("no_substantive_activity", in.NoSubstantiveActivity)
	add("has_open_tool_call", in.HasOpenToolCall)
	add("last_was_user_interrupt", in.LastWasUserInterrupt)
	add("last_was_tool_denial", in.LastWasToolDenial)
	if in.LastEventType != "" {
		parts = append(parts, "last_event_type="+in.LastEventType)
	}
	if len(in.LastOpenToolNames) > 0 {
		parts = append(parts, "open_tools=["+strings.Join(in.LastOpenToolNames, ",")+"]")
	}
	if len(parts) == 0 {
		return "(all defaults)"
	}
	return strings.Join(parts, ", ")
}

// renderGhostTimeline produces a human-first text report of the sidecar's
// sessions, ghosts first with full lifecycle detail, then a one-line summary of
// the substantive sessions. This view is intentionally kept off the JSON/golden
// path — it exists for an agent reconstructing why a transient session (e.g. an
// antigravity PID=0 ghost) appeared and was reaped.
func renderGhostTimeline(sidecarPath string, timelines []sessionTimeline, inputs map[string]*lifecycle.ClassifierInputs) string {
	ghosts, others := partitionGhosts(timelines)

	var b strings.Builder
	fmt.Fprintf(&b, "ghost timeline — %s\n", sidecarPath)
	fmt.Fprintf(&b, "%d session(s), %d ghost(s)\n\n", len(timelines), len(ghosts))

	if len(ghosts) == 0 {
		b.WriteString("GHOSTS\n  (none)\n\n")
	} else {
		b.WriteString("GHOSTS\n")
		for _, t := range ghosts {
			renderGhostEntry(&b, t, inputs[t.SessionID])
		}
		b.WriteString("\n")
	}

	b.WriteString("OTHER SESSIONS\n")
	if len(others) == 0 {
		b.WriteString("  (none)\n")
	}
	for _, t := range others {
		renderOtherEntry(&b, t)
	}
	return b.String()
}

// partitionGhosts splits timelines into ghost and non-ghost sessions,
// preserving each group's relative order.
func partitionGhosts(timelines []sessionTimeline) (ghosts, others []sessionTimeline) {
	for _, t := range timelines {
		if t.IsGhost {
			ghosts = append(ghosts, t)
		} else {
			others = append(others, t)
		}
	}
	return ghosts, others
}

// renderGhostEntry writes one ghost session's full lifecycle detail block:
// minted time, lifetime, final state/reason, the classifier inputs at its
// final transition, and reap time/reason.
func renderGhostEntry(b *strings.Builder, t sessionTimeline, inputs *lifecycle.ClassifierInputs) {
	adapter := t.Adapter
	if adapter == "" {
		adapter = "?"
	}
	fmt.Fprintf(b, "  %s  [%s]  GHOST\n", t.SessionID, adapter)
	fmt.Fprintf(b, "    minted    %s\n", fmtTime(t.FirstSeen))
	fmt.Fprintf(b, "    lifetime  %s\n", fmtLifetime(t.LifetimeMs))
	final := t.FinalState
	if final == "" {
		final = "(no transition)"
	}
	fmt.Fprintf(b, "    final     %s%s\n", final, fmtReason(t.FinalReason))
	fmt.Fprintf(b, "    inputs    %s\n", formatClassifierInputs(inputs))
	fmt.Fprintf(b, "    reaped    %s%s\n", fmtTimePtr(t.RemovedAt), fmtReason(t.RemovalReason))
	b.WriteString("    substantive activity: none\n")
}

// renderOtherEntry writes one non-ghost session's one-line summary.
func renderOtherEntry(b *strings.Builder, t sessionTimeline) {
	adapter := t.Adapter
	if adapter == "" {
		adapter = "?"
	}
	final := t.FinalState
	if final == "" {
		final = "-"
	}
	fmt.Fprintf(b, "  %s  [%s]  %s  pid=%d  events=%d  duration=%s\n",
		t.SessionID, adapter, final, t.PID, t.EventCount, fmtLifetime(t.DurationMs))
}

func fmtTime(t time.Time) string {
	if t.IsZero() {
		return "(unknown)"
	}
	return t.UTC().Format(time.RFC3339)
}

func fmtTimePtr(t *time.Time) string {
	if t == nil {
		return "(not reaped)"
	}
	return fmtTime(*t)
}

func fmtLifetime(ms int64) string {
	return (time.Duration(ms) * time.Millisecond).String()
}

func fmtReason(reason string) string {
	if reason == "" {
		return ""
	}
	return fmt.Sprintf(" — %q", reason)
}

// runGhostDump loads a lifecycle sidecar and prints its ghost timeline to
// stdout. Backs the --ghosts flag.
func runGhostDump(sidecarPath string) error {
	events, err := loadAllLifecycleEvents(sidecarPath)
	if err != nil {
		return fmt.Errorf("load sidecar %s: %w", sidecarPath, err)
	}
	// buildGhostTimelines already returns timelines sorted by FirstSeen
	// (buildSessionTimelines sorts; the ghost-layering pass preserves order).
	timelines := buildGhostTimelines(events)
	fmt.Print(renderGhostTimeline(sidecarPath, timelines, lastTransitionInputs(events)))
	return nil
}
