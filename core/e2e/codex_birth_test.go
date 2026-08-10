// Package e2e — birth-state coverage for header-linked adapters (issue #1447).
package e2e_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/outbound/metrics"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/inbound"
)

// Real codex rollout heads — the two lines that exist on disk at the instant
// the daemon first sees the file, transcribed from recordings of two codex
// releases fourteen weeks apart.
//
// They are here as a PAIR to pin both shapes, but be precise about what that
// does and does not prove. Issue #1447 offered two candidate causes for codex
// sessions being born `working`: upstream drift (codex 0.147 materialising its
// rollout differently than 0.130 did) or a daemon regression.
//
// These two subtests CANNOT discriminate between them on their own, and the
// earlier version of this comment wrongly claimed they could. The only field
// that differs between the heads is payload.cli_version, which codex's parser
// turns into ev.AgentVersion on a Skip=true session_meta — and no rule in
// stateRules reads AgentVersion. The two subtests are therefore incapable of
// disagreeing. The discrimination happened when these bytes were transcribed
// and compared, not here.
//
// What they DO pin is that both shapes are insubstantial at birth, so a future
// parser change that starts finding signal in either one is caught.
//
// Provenance, which is asymmetric and worth knowing before trusting them:
//   - 0.130.0 is verifiable from this repo, field-by-field against
//     replaydata/agents/codex/scenarios/2-13_turn-end-terminal-text/
//     recordings/2026-05-23-21-44-53_irrlichd-0.4.7+f83dc27/transcript.jsonl.
//   - 0.147.0 is NOT in the repo. It was transcribed from the 2026-08-10
//     staged re-record of the same cell, which lives under .build/ in #1388's
//     worktree and is unpromotable (it was recorded by the very daemon this
//     fix repairs). A reader cannot check it here. When #1388 lands a
//     re-recorded fixture, read the head off disk and delete this constant.
//
// Both emit session_meta, then event_msg/task_started, 1-3ms apart. The
// payloads are trimmed to the fields codex's parser dispatches on — it keys
// off the top-level "type" and, for event_msg, payload.type; everything else
// in the real ~19KB header is data this code path never reads.
const (
	codexHead0130 = `{"timestamp":"2026-05-23T19:45:09.665Z","type":"session_meta","payload":{"id":"019e565e-7c1d-7ce3-ad6f-7f422672d7c7","cwd":"/tmp/cwd","originator":"codex-tui","cli_version":"0.130.0","source":"cli"}}
{"timestamp":"2026-05-23T19:45:09.668Z","type":"event_msg","payload":{"type":"task_started"}}
`
	codexHead0147 = `{"timestamp":"2026-08-10T00:51:56.768Z","type":"session_meta","payload":{"id":"019fe927-889e-73d1-98c8-4f1ed0c064a5","cwd":"/tmp/cwd","originator":"codex-tui","cli_version":"0.147.0","source":"cli"}}
{"timestamp":"2026-08-10T00:51:56.769Z","type":"event_msg","payload":{"type":"task_started"}}
`
)

// bornState drives one EventNewSession through a real SessionDetector wired
// to the PRODUCTION metrics adapter, and returns the session as it was first
// persisted. Both tests below assert on nothing but its State, so the wiring
// is factored out here rather than repeated around each assertion. The
// session's CWD is derived from the transcript's own directory — what both
// callers want, and one fewer argument for them to keep in sync.
//
// The production adapter is the point: the birth path trusts whatever the
// real collector hands back, and a stub returning nil is exactly what hid
// #1447 for as long as it hid.
func bornState(t *testing.T, adapterName, sessionID, transcriptPath string) *session.SessionState {
	t.Helper()

	repo := newMemRepo()
	deps := defaultSessionDetectorDeps(repo)
	deps.Metrics = realCodexMetrics()

	w := newMockWatcher(1)
	w.identity = agent.Identity{Name: adapterName}
	detector := services.NewSessionDetector([]inbound.Watcher{w}, deps)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go detector.Run(ctx)

	w.ch <- agent.Event{
		Type:           agent.EventNewSession,
		SessionID:      sessionID,
		TranscriptPath: transcriptPath,
		CWD:            filepath.Dir(transcriptPath),
	}

	if !waitForSession(repo, sessionID, 5*time.Second) {
		t.Fatalf("session %s never reached the repo", sessionID)
	}
	got, err := repo.Load(sessionID)
	if err != nil {
		t.Fatalf("load session %s: %v", sessionID, err)
	}
	return got
}

// realCodexMetrics builds the production metrics adapter over the real
// adapter registry, so ComputeMetrics runs codex's own parser rather than a
// double. That is the point of putting this test in e2e: the defect is that
// the birth path trusts whatever the real collector hands back, and a stub
// returning nil is exactly what hid it.
func realCodexMetrics() *metrics.Adapter {
	all := agents.All()
	return metrics.New(metrics.Registry{
		Parsers:          agents.Parsers(all),
		SubagentCounters: agents.SubagentCounters(all),
		MetricsProviders: agents.MetricsProviders(all),
		FallbackName:     "claude-code",
	})
}

// TestCodexSession_BornReadyOnAnInsubstantialRolloutHead is the end-to-end
// half of issue #1447's defect test; the unit half is
// TestNewTopLevelSession_StaysReadyWhenParsedLinesAreAllInsubstantial in
// core/application/services.
//
// Codex is header-linked (#1055): the fswatcher parks the zero-byte create
// and emits EventNewSession only once the rollout's first line is readable.
// So unlike Claude Code, codex's birth event ALWAYS carries a parseable file,
// and ComputeMetrics therefore always returns non-nil metrics. Both lines
// present at that moment are Skip=true in codex's parser, so those metrics
// describe no activity whatsoever — but they are not nil, and ClassifyState
// only short-circuits on nil. The rule ladder is total, so the session was
// birthed `working` on zero evidence.
//
// The user-visible damage is that every codex session appears to be
// generating from the moment it is discovered until its first real turn
// boundary, and 34 fixture cells assert the opposite.
func TestCodexSession_BornReadyOnAnInsubstantialRolloutHead(t *testing.T) {
	for _, tc := range []struct {
		name string
		head string
	}{
		{"codex 0.130.0 (May 2026 recording)", codexHead0130},
		{"codex 0.147.0 (Aug 2026 re-record)", codexHead0147},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := realTempDir(t)
			rollout := filepath.Join(dir, "rollout-2026-01-01T00-00-00-test.jsonl")
			if err := os.WriteFile(rollout, []byte(tc.head), 0o644); err != nil {
				t.Fatalf("write rollout: %v", err)
			}

			// Vacuity guard. If codex's parser ever starts treating either of
			// these lines as substantive, or ComputeMetrics starts returning
			// nil for them, the assertion below would pass without exercising
			// the defect at all — the "stub blind to the asserted field"
			// failure. Fail loudly instead of reporting a green that proves
			// nothing.
			//
			// Run against a SEPARATE copy of the bytes, through a separate
			// adapter instance. Tailers are stateful and cached per transcript
			// path: probing the detector's own file here would consume its
			// lines, so the detector's pass would read zero new ones and the
			// test would measure the empty-pass case instead of the
			// insubstantial-pass case it is about.
			probeFile := filepath.Join(dir, "probe.jsonl")
			if err := os.WriteFile(probeFile, []byte(tc.head), 0o644); err != nil {
				t.Fatalf("write probe: %v", err)
			}
			m, err := realCodexMetrics().ComputeMetrics(probeFile, "codex")
			if err != nil {
				t.Fatalf("ComputeMetrics: %v", err)
			}
			if m == nil {
				t.Fatalf("ComputeMetrics returned nil metrics — this test no longer reaches " +
					"the #1447 defect, which is specifically the NON-nil-but-insubstantial case")
			}
			if m.LastEventType != "" {
				t.Fatalf("LastEventType = %q, want empty — codex's parser now finds signal in "+
					"session_meta/task_started, so this head no longer reproduces #1447",
					m.LastEventType)
			}

			got := bornState(t, "codex", "019fe927-889e-73d1-98c8-4f1ed0c064a5", rollout)
			if got.State != session.StateReady {
				t.Errorf("born %q, want %q — the rollout head holds only session_meta and "+
					"task_started, both of which codex's own parser skips, so nothing "+
					"observed says the agent is generating",
					got.State, session.StateReady)
			}
		})
	}
}

// TestSession_BornReadyOnAZeroByteTranscript covers the second, wider arm of
// #1447 — and it is the one that makes this a whole-daemon defect rather than
// a codex quirk.
//
// #1256 reasoned that non-header-linked adapters were safe because "there is
// genuinely nothing to classify at discovery" for an agent that creates its
// transcript and writes to it later. That is true of the FILE and false of
// the METRICS. ComputeMetrics returns nil only when the transcript is ABSENT;
// an existing zero-byte one is opened, tailed, and yields a non-nil metrics
// struct with nothing in it — which then reaches the total rule ladder and
// comes back `working`, exactly like codex's insubstantial head.
//
// The fswatcher parks the zero-byte create only for header-linked adapters
// (codex is the only one), so for every OTHER adapter the birth event is free
// to fire in the window between create and first write. That makes this arm a
// race rather than a certainty — which is precisely why it went unnoticed:
// it produces an intermittently wrong opening state instead of a reproducible
// one.
func TestSession_BornReadyOnAZeroByteTranscript(t *testing.T) {
	dir := realTempDir(t)
	transcript := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(transcript, nil, 0o644); err != nil {
		t.Fatalf("write empty transcript: %v", err)
	}

	// Vacuity guard: the whole point is that this is NOT the nil case.
	probe := filepath.Join(dir, "probe.jsonl")
	if err := os.WriteFile(probe, nil, 0o644); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	if m, err := realCodexMetrics().ComputeMetrics(probe, "claude-code"); err != nil {
		t.Fatalf("ComputeMetrics: %v", err)
	} else if m == nil {
		// Fatal, not Skip. This test is the ONLY lock on the
		// LastEventType-vs-NoSubstantiveActivity choice: mutate the guard to
		// NoSubstantiveActivity and every other test in this PR stays green
		// while this one fails. A skip reads as a pass, which would retire
		// that lock silently — the same reasoning AGENTS.md applies to
		// posix-lint.sh's three refusals.
		t.Fatalf("a zero-byte transcript now yields nil metrics — this arm of #1447 is no " +
			"longer reachable, so this test would pass vacuously and stop locking the " +
			"guard choice; re-derive it rather than deleting it")
	}

	got := bornState(t, "claude-code", "zero-byte-session", transcript)
	if got.State != session.StateReady {
		t.Errorf("born %q, want %q — the transcript is zero bytes, so no event has been "+
			"observed at all; only an ABSENT file yields nil metrics, and it is that gap "+
			"between absent and empty that this asserts",
			got.State, session.StateReady)
	}
}
