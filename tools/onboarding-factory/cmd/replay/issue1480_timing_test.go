package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"irrlicht/core/domain/lifecycle"
)

// Issue #1480: replay goldens pin a virtual_time on every transition and
// nothing compared it to anything, so a transition reproduced at the right
// position in the ORDER but 31 seconds from when the daemon made it passed
// every gate in this package and the golden then pinned it as correct.
//
// These two tests are the measurement. The first proves the measurement can
// fail — it has no "before the fix" to run against, so per AGENTS.md it owes a
// deliberate mutation, and that mutation is committed under testdata/timing/
// rather than described in a PR body. The second runs it over the whole
// catalog and enumerates what drifts today.

// ---------------------------------------------------------------------------
// 1. The detector itself, over a committed corpus.
// ---------------------------------------------------------------------------

type timingCase struct {
	Description  string            `json:"description"`
	Recorded     []lifecycle.Event `json:"recorded"`
	Replayed     []transition      `json:"replayed"`
	WantDeltasNs []int64           `json:"want_deltas_ns"`
	WantFirstNs  *int64            `json:"want_first_drift"`
	WantWorstNs  *int64            `json:"want_worst_ns"`
}

// TestTransitionTimeDeltas_Corpus is #1480's mutation evidence. Every case is a
// timing shape the measurement must classify correctly, and the corpus carries
// both verdicts on purpose: first-transition-31s-early.json must be reported,
// aligned.json and sub-second-justified.json must not. A detector that flags
// everything and one that flags correctly are indistinguishable without the
// second kind, which is why the balance is asserted below rather than left to
// whoever adds the next case.
func TestTransitionTimeDeltas_Corpus(t *testing.T) {
	dir := filepath.Join("testdata", "timing")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read corpus dir: %v", err)
	}

	var ran, sawDrift, sawClean int
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		name := e.Name()
		t.Run(strings.TrimSuffix(name, ".json"), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err != nil {
				t.Fatalf("read %s: %v", name, err)
			}
			var tc timingCase
			if err := json.Unmarshal(raw, &tc); err != nil {
				t.Fatalf("parse %s: %v", name, err)
			}
			if tc.Description == "" {
				t.Fatalf("%s has no description — a corpus row states what it pins", name)
			}
			// A case that supplies no transitions asserts nothing; the corpus
			// would still look populated. Refuse rather than skip.
			if len(tc.Recorded) == 0 || len(tc.Replayed) == 0 {
				t.Fatalf("%s supplies %d recorded / %d replayed — an empty side pins nothing",
					name, len(tc.Recorded), len(tc.Replayed))
			}

			got := transitionTimeDeltas(tc.Recorded, tc.Replayed)

			if len(got) != len(tc.WantDeltasNs) {
				t.Fatalf("%s: got %d deltas, want %d\n  got:  %v\n  want: %v",
					tc.Description, len(got), len(tc.WantDeltasNs), durations(got), tc.WantDeltasNs)
			}
			for i, d := range got {
				if int64(d.Delta) != tc.WantDeltasNs[i] {
					t.Errorf("%s: delta[%d] = %v (%d ns), want %v (%d ns)",
						tc.Description, i, d.Delta, int64(d.Delta),
						time.Duration(tc.WantDeltasNs[i]), tc.WantDeltasNs[i])
				}
			}

			first, ok := firstDrift(got)
			switch {
			case tc.WantFirstNs == nil && ok:
				t.Errorf("%s: reported first drift %v, want none (threshold %v)",
					tc.Description, first.Delta, driftThreshold)
			case tc.WantFirstNs != nil && !ok:
				t.Errorf("%s: reported NO first drift, want %v — the measurement stopped measuring",
					tc.Description, time.Duration(*tc.WantFirstNs))
			case tc.WantFirstNs != nil && int64(first.Delta) != *tc.WantFirstNs:
				t.Errorf("%s: first drift = %v, want %v",
					tc.Description, first.Delta, time.Duration(*tc.WantFirstNs))
			}

			if tc.WantWorstNs != nil {
				worst, found := worstDrift(got)
				if !found {
					t.Errorf("%s: no worst pair, want %v", tc.Description, time.Duration(*tc.WantWorstNs))
				} else if int64(worst.Delta) != *tc.WantWorstNs {
					t.Errorf("%s: worst = %v, want %v",
						tc.Description, worst.Delta, time.Duration(*tc.WantWorstNs))
				}
			}

			if tc.WantFirstNs != nil {
				sawDrift++
			} else {
				sawClean++
			}
			ran++
		})
	}

	if ran == 0 {
		t.Fatal("no corpus case ran — testdata/timing/ is empty or unreadable, and an empty corpus reads exactly like a passing one")
	}
	if sawDrift == 0 {
		t.Error("no corpus case expects drift — nothing here proves the measurement can fire")
	}
	if sawClean == 0 {
		t.Error("no corpus case expects a clean result — nothing here proves the measurement can stay silent")
	}
	t.Logf("corpus: %d cases (%d expect drift, %d expect clean)", ran, sawDrift, sawClean)
}

func durations(ds []timeDelta) []time.Duration {
	out := make([]time.Duration, 0, len(ds))
	for _, d := range ds {
		out = append(out, d.Delta)
	}
	return out
}

// ---------------------------------------------------------------------------
// 2. The catalog measurement.
// ---------------------------------------------------------------------------

// knownFirstTransitionDrift enumerates every sidecar-driven recording whose
// FIRST kind-matched transition is reproduced more than driftThreshold (1s)
// from the timestamp the daemon logged for it.
//
// This list is the answer to #1480's acceptance criterion, and it is machine-
// generated: TestSidecarReplayTransitionTimesMatchTheDaemonsOwnLog prints the
// exact map literal to paste when the set legitimately changes. It is not
// maintained by hand and must not be.
//
// The 20 entries marked "#1476" are that PR's documented, ACCEPTED cost, not a
// backlog. readBoundaryFor's doc comment carries the full argument and the
// rejected alternative: bounding the widening by elapsed time collapses these
// six worst cases (-30.976s -> -0.022s) but breaks 35 gemini-cli recordings
// that reproduce exactly today, because the sidecar cannot distinguish "the gap
// is idle time AFTER a write the daemon absorbed" from "the gap is before the
// write happened". Net across the catalog the widening still moves times TOWARD
// the daemon. They are listed here so the cost is enumerated rather than living
// in a paragraph of a doc comment — which is precisely what #1480 was filed
// about.
//
// Everything unmarked PRE-DATES #1476 and is untouched by it. Two families
// dominate:
//
//   - copilot and antigravity recordings whose first turn opens with a long
//     tool call: the daemon classified on a read that reached content the
//     replay's clamped boundary does not, so the replay fires at the debounce
//     deadline instead.
//   - pi and gemini-cli recordings sitting at almost exactly +2.00s: a
//     synthesized debounce-deadline stamp, one full window after the last
//     coalesced fs event, where the daemon's own timer had fired earlier.
//
// Neither is #1480's to fix. #1480 is the measurement; what to do about the
// populations it reveals is argued in the PR with this list in hand.
var knownFirstTransitionDrift = map[string]string{
	"antigravity/scenarios/1-1_session-start/recordings/2026-06-20-12-33-40_irrlichd-0.5.2+be45695/transcript.jsonl":                   "pre-dates #1476: -6.609s at pair 0 (ready→working)",
	"antigravity/scenarios/1-2_session-end/recordings/2026-06-20-12-36-01_irrlichd-0.5.2+a00b826/transcript.jsonl":                     "pre-dates #1476: -4.304s at pair 0 (ready→working)",
	"antigravity/scenarios/1-3_long-idle-live-session/recordings/2026-06-20-17-58-44_irrlichd-0.5.2+1fcae3a/transcript.jsonl":          "pre-dates #1476: -3.937s at pair 0 (ready→working)",
	"antigravity/scenarios/1-4_session-resume/recordings/2026-06-20-12-43-04_irrlichd-0.5.2+bb79f4c/transcript.jsonl":                  "pre-dates #1476: -3.771s at pair 0 (ready→working)",
	"antigravity/scenarios/2-11_auto-classified-permission/recordings/2026-06-20-13-41-55_irrlichd-0.5.2+b756a52/transcript.jsonl":     "pre-dates #1476: -5.774s at pair 0 (ready→working)",
	"antigravity/scenarios/2-12_context-compaction/recordings/2026-06-20-13-46-30_irrlichd-0.5.2+77308b9/transcript.jsonl":             "pre-dates #1476: -3.809s at pair 0 (ready→working)",
	"antigravity/scenarios/2-13_turn-end-terminal-text/recordings/2026-06-20-02-50-41_irrlichd-0.5.2+2900672/transcript.jsonl":         "pre-dates #1476: -3.522s at pair 0 (ready→working)",
	"antigravity/scenarios/2-15_shell-escape-command/recordings/2026-06-20-13-49-04_irrlichd-0.5.2+349bafc/transcript.jsonl":           "pre-dates #1476: -4.067s at pair 0 (ready→working)",
	"antigravity/scenarios/2-16_oversized-transcript-line/recordings/2026-06-20-13-18-38_irrlichd-0.5.2+76d805a/transcript.jsonl":      "pre-dates #1476: -67.369s at pair 0 (ready→working)",
	"antigravity/scenarios/2-1_basic-turn/recordings/2026-06-20-02-48-55_irrlichd-0.5.2+daeb6e1/transcript.jsonl":                      "pre-dates #1476: -3.199s at pair 0 (ready→working)",
	"antigravity/scenarios/2-21_streaming-partial-writes/recordings/2026-06-20-13-21-01_irrlichd-0.5.2+c7f1c6d/transcript.jsonl":       "pre-dates #1476: -5.111s at pair 0 (ready→working)",
	"antigravity/scenarios/2-4_self-correction-iteration/recordings/2026-06-20-13-14-32_irrlichd-0.5.2+a489d47/transcript.jsonl":       "pre-dates #1476: -4.859s at pair 0 (ready→working)",
	"antigravity/scenarios/2-5_synchronous-slash-command/recordings/2026-06-20-13-38-51_irrlichd-0.5.2+7a48e0d/transcript.jsonl":       "pre-dates #1476: -3.726s at pair 0 (ready→working)",
	"antigravity/scenarios/2-7_autonomous-loop/recordings/2026-06-20-14-00-16_irrlichd-0.5.2+250cd02/transcript.jsonl":                 "pre-dates #1476: -4.521s at pair 0 (ready→working)",
	"antigravity/scenarios/2-8_autonomous-loop-iteration-limit/recordings/2026-06-20-14-02-53_irrlichd-0.5.2+2de7ce6/transcript.jsonl": "pre-dates #1476: -5.681s at pair 0 (ready→working)",
	"antigravity/scenarios/3-1_foreground-subagent/recordings/2026-06-20-18-04-13_irrlichd-0.5.2+09e9cf9/transcript.jsonl":             "pre-dates #1476: -6.595s at pair 0 (ready→working)",
	"antigravity/scenarios/3-2_background-subagent/recordings/2026-06-20-18-15-41_irrlichd-0.5.2+b305884/transcript.jsonl":             "pre-dates #1476: -4.468s at pair 0 (ready→working)",
	"antigravity/scenarios/4-2_multiple-agents-same-workspace/recordings/2026-06-20-16-53-31_irrlichd-0.5.2+2eb84fb/transcript.jsonl":  "pre-dates #1476: -5.079s at pair 0 (ready→working)",
	"antigravity/scenarios/5-1_token-accounting/recordings/2026-06-28-11-54-27_irrlichd-0.5.3+7f52a76/transcript.jsonl":                "pre-dates #1476: -3.343s at pair 0 (ready→working)",
	"claudecode/scenarios/2-7_autonomous-loop/recordings/2026-05-18-22-47-11_irrlichd-0.4.5+8499138/transcript.jsonl":                  "#1476 accepted: -5.075s at pair 0 (ready→working)",
	"claudecode/scenarios/2-7_autonomous-loop/recordings/2026-05-18-22-52-38_irrlichd-0.4.5+8499138/transcript.jsonl":                  "#1476 accepted: -4.561s at pair 0 (ready→working)",
	"claudecode/scenarios/2-8_autonomous-loop-iteration-limit/recordings/2026-05-18-22-56-34_irrlichd-0.4.5+6898561/transcript.jsonl":  "#1476 accepted: -9.012s at pair 0 (ready→working)",
	"claudecode/scenarios/5-8_task-estimate-marker/recordings/2026-06-03-21-50-21_irrlichd-0.4.8+defb4d8/transcript.jsonl":             "pre-dates #1476: -1.167s at pair 0 (ready→working)",
	"codex/regressions/fork-conversation/recordings/2026-05-23-19-39-41_irrlichd-0.4.7+3c6427c/transcript.jsonl":                       "#1476 accepted: -3.759s at pair 0 (ready→working)",
	"codex/scenarios/1-4_session-resume/recordings/2026-05-23-20-30-54_irrlichd-0.4.7+c461eef/transcript.jsonl":                        "#1476 accepted: -4.007s at pair 0 (ready→working)",
	"codex/scenarios/1-5_session-reset/recordings/2026-05-25-00-26-42_irrlichd-0.4.7+9d1ef56.dirty/transcript.jsonl":                   "#1476 accepted: -3.673s at pair 0 (ready→working)",
	"codex/scenarios/2-13_turn-end-terminal-text/recordings/2026-05-23-21-44-53_irrlichd-0.4.7+f83dc27/transcript.jsonl":               "#1476 accepted: -2.246s at pair 0 (ready→working)",
	"codex/scenarios/2-15_shell-escape-command/recordings/2026-05-23-22-23-45_irrlichd-0.4.7+7075bff/transcript.jsonl":                 "#1476 accepted: -2.032s at pair 0 (ready→working)",
	"codex/scenarios/2-18_user-blocking-plan-mode-approval/recordings/2026-05-23-23-48-22_irrlichd-0.4.7+eb984db/transcript.jsonl":     "#1476 accepted: -5.670s at pair 0 (ready→working)",
	"codex/scenarios/2-1_basic-turn/recordings/2026-05-23-20-36-20_irrlichd-0.4.7+723609a/transcript.jsonl":                            "#1476 accepted: -3.691s at pair 0 (ready→working)",
	"codex/scenarios/2-20_interrupted-turn/recordings/2026-05-14-23-36-59_irrlichd-dev/transcript.jsonl":                               "pre-dates #1476: +2.187s at pair 0 (ready→working)",
	"codex/scenarios/2-20_interrupted-turn/recordings/2026-05-16-22-53-47_irrlichd-0.3.13+4662be4/transcript.jsonl":                    "pre-dates #1476: +2.214s at pair 0 (ready→working)",
	"codex/scenarios/2-20_interrupted-turn/recordings/2026-05-16-22-56-57_irrlichd-0.3.13+4662be4/transcript.jsonl":                    "pre-dates #1476: +2.652s at pair 0 (ready→working)",
	"codex/scenarios/2-2_auto-executed-tool-call/recordings/2026-05-23-22-48-46_irrlichd-0.4.7+14e1e7a/transcript.jsonl":               "#1476 accepted: -2.234s at pair 0 (ready→working)",
	"codex/scenarios/2-5_synchronous-slash-command/recordings/2026-05-23-21-53-28_irrlichd-0.4.7+6934b22/transcript.jsonl":             "#1476 accepted: -3.615s at pair 0 (ready→working)",
	"codex/scenarios/2-6_long-agentic-session-stress/recordings/2026-05-23-23-03-45_irrlichd-0.4.7+ccb564c/transcript.jsonl":           "#1476 accepted: -2.230s at pair 0 (ready→working)",
	"codex/scenarios/2-7_autonomous-loop/recordings/2026-05-23-23-15-25_irrlichd-0.4.7+774c575/transcript.jsonl":                       "#1476 accepted: -2.287s at pair 0 (ready→working)",
	"codex/scenarios/2-8_autonomous-loop-iteration-limit/recordings/2026-05-23-23-20-25_irrlichd-0.4.7+6a1a098/transcript.jsonl":       "#1476 accepted: -6.782s at pair 0 (ready→working)",
	"codex/scenarios/4-1_multiple-sessions-same-cwd/recordings/2026-05-24-21-42-19_irrlichd-0.4.7+83509be.dirty/transcript.jsonl":      "#1476 accepted: -3.775s at pair 0 (ready→working)",
	"codex/scenarios/5-3_model-switch-midsession/recordings/2026-05-23-21-34-12_irrlichd-0.4.7+b8b1cfc/transcript.jsonl":               "#1476 accepted: -4.079s at pair 0 (ready→working)",
	"copilot/scenarios/1-1_session-start/recordings/2026-08-03-12-06-21_irrlichd-0.5.9+4c1b2e4/transcript.jsonl":                       "pre-dates #1476: -3.842s at pair 0 (ready→working)",
	"copilot/scenarios/1-2_session-end/recordings/2026-08-03-12-07-28_irrlichd-0.5.9+57d59de/transcript.jsonl":                         "pre-dates #1476: -10.828s at pair 0 (ready→working)",
	"copilot/scenarios/1-4_session-resume/recordings/2026-08-05-19-02-01_irrlichd-0.5.9+4b58365/transcript.jsonl":                      "pre-dates #1476: -18.621s at pair 1 (working→ready)",
	"copilot/scenarios/2-13_turn-end-terminal-text/recordings/2026-08-03-12-16-12_irrlichd-0.5.9+ced85ef/transcript.jsonl":             "pre-dates #1476: -4.190s at pair 0 (ready→working)",
	"gemini-cli/scenarios/2-12_context-compaction/recordings/2026-06-12-10-36-37_irrlichd-0.5.1+93db11a/transcript.jsonl":              "pre-dates #1476: +2.001s at pair 0 (ready→working)",
	"mistral-vibe/scenarios/2-12_context-compaction/recordings/2026-07-07-17-22-57_irrlichd-0.5.5+bc77a37.dirty/transcript.jsonl":      "#1476 accepted: -30.976s at pair 0 (ready→working)",
	"mistral-vibe/scenarios/2-15_shell-escape-command/recordings/2026-07-07-17-41-58_irrlichd-0.5.5+22a01d2.dirty/transcript.jsonl":    "#1476 accepted: -8.599s at pair 0 (ready→working)",
	"mistral-vibe/scenarios/6-1_backchannel-control/recordings/2026-07-08-09-15-24_irrlichd-0.5.5+35c4012.dirty/transcript.jsonl":      "#1476 accepted: -27.522s at pair 0 (ready→working)",
	"pi/regressions/model-switch/recordings/2026-05-25-04-09-10_irrlichd-0.4.7+2a46388/transcript.jsonl":                               "pre-dates #1476: +2.003s at pair 0 (ready→working)",
	"pi/regressions/multi-turn-conversation/recordings/2026-04-26-12-27-56_irrlichd-unknown/transcript.jsonl":                          "pre-dates #1476: +2.045s at pair 0 (ready→working)",
	"pi/scenarios/1-2_session-end/recordings/2026-05-25-05-48-26_irrlichd-0.4.7+597f655/transcript.jsonl":                              "pre-dates #1476: +2.003s at pair 0 (ready→working)",
	"pi/scenarios/1-3_long-idle-live-session/recordings/2026-05-23-11-12-58_irrlichd-0.4.7+d30518a.dirty/transcript.jsonl":             "pre-dates #1476: +2.002s at pair 0 (ready→working)",
	"pi/scenarios/1-5_session-reset/recordings/2026-05-27-23-52-56_irrlichd-0.4.7+b4e2076-2/transcript.jsonl":                          "pre-dates #1476: +2.004s at pair 0 (ready→working)",
	"pi/scenarios/1-5_session-reset/recordings/2026-05-27-23-52-56_irrlichd-0.4.7+b4e2076/transcript.jsonl":                            "pre-dates #1476: +2.004s at pair 0 (ready→working)",
	"pi/scenarios/1-8_model-context-display/recordings/2026-06-05-10-58-09_irrlichd-0.4.8+1f22f6f/transcript.jsonl":                    "pre-dates #1476: +2.005s at pair 0 (ready→working)",
	"pi/scenarios/2-10_mid-turn-message-queued/recordings/2026-05-25-03-03-21_irrlichd-0.4.7+2f5b484-2/transcript.jsonl":               "pre-dates #1476: +2.002s at pair 0 (ready→working)",
	"pi/scenarios/2-10_mid-turn-message-queued/recordings/2026-05-25-03-03-21_irrlichd-0.4.7+2f5b484/transcript.jsonl":                 "pre-dates #1476: +2.002s at pair 0 (ready→working)",
	"pi/scenarios/2-12_context-compaction/recordings/2026-05-25-04-43-02_irrlichd-0.4.7+ac2dcc1/transcript.jsonl":                      "pre-dates #1476: +2.004s at pair 0 (ready→working)",
	"pi/scenarios/2-13_turn-end-terminal-text/recordings/2026-05-25-02-11-57_irrlichd-0.4.7+327fbc2/transcript.jsonl":                  "pre-dates #1476: +2.003s at pair 0 (ready→working)",
	"pi/scenarios/2-15_shell-escape-command/recordings/2026-05-25-02-51-25_irrlichd-0.4.7+021b5b0/transcript.jsonl":                    "pre-dates #1476: +2.003s at pair 0 (ready→working)",
	"pi/scenarios/2-17_agent-question-pending/recordings/2026-05-25-05-28-25_irrlichd-0.4.7+730a676/transcript.jsonl":                  "pre-dates #1476: +2.003s at pair 0 (ready→working)",
	"pi/scenarios/2-5_synchronous-slash-command/recordings/2026-05-25-02-43-00_irrlichd-0.4.7+3810f2e/transcript.jsonl":                "pre-dates #1476: +2.003s at pair 0 (ready→working)",
	"pi/scenarios/5-1_token-accounting/recordings/2026-05-25-02-16-07_irrlichd-0.4.7+ec7acf6/transcript.jsonl":                         "pre-dates #1476: +2.006s at pair 0 (ready→working)",
	"pi/scenarios/5-2_model-identification/recordings/2026-05-25-02-20-47_irrlichd-0.4.7+d379043/transcript.jsonl":                     "pre-dates #1476: +2.004s at pair 0 (ready→working)",
	"pi/scenarios/5-3_model-switch-midsession/recordings/2026-05-25-04-02-05_irrlichd-0.4.7+4de276d/transcript.jsonl":                  "pre-dates #1476: +2.003s at pair 0 (ready→working)",
}

// aggregate ratchets. The named list above keys on the FIRST kind-matched pair,
// which is the figure a read-boundary model moves and the one #1476 reported.
// A classifier change that moved only a LATER transition would slip past it
// entirely, so the two scalars below hold the whole population — every
// kind-matched pair in every recording — without a second list of names to
// maintain.
const (
	maxRecordingsDriftingOverThreshold = 119
	maxRecordingsDriftingOver5s        = 50
)

// TestSidecarReplayTransitionTimesMatchTheDaemonsOwnLog is #1480's mechanical
// report: it walks every sidecar-driven recording in the catalog, measures each
// reproduced transition against the ts the recording's own daemon logged, and
// prints the distribution.
//
// Catalog-wide rather than a list of paths, so a newly recorded fixture is
// covered by existing rather than by somebody remembering to add it — the same
// reason TestSidecarReplayAgreesWithTheDaemonsOwnLog walks instead of listing.
//
// It is a RATCHET, not a tolerance gate. A transition drifting 900ms is not
// asserted to be correct; it is asserted not to be NEW. The distinction matters
// because 24.5% of the committed catalog's transitions are already over 1s from
// their daemon, and a gate that failed on all of them would be reverted within
// the day and would protect nothing.
func TestSidecarReplayTransitionTimesMatchTheDaemonsOwnLog(t *testing.T) {
	root := replaydataRoot(t)

	var checked int
	var allDeltas []timeDelta
	drifted := map[string]timeDelta{}
	overThreshold, over5s := 0, 0

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Base(path) != eventsSidecarName {
			return nil
		}
		transcript := filepath.Join(filepath.Dir(path), "transcript.jsonl")
		if _, statErr := os.Stat(transcript); statErr != nil {
			return nil
		}
		tp, sp, useSidecar := resolveInputPaths(transcript)
		if !useSidecar {
			return nil
		}
		report, runErr := runReplay(tp, sp, useSidecar, replaySettingsForTest(t, tp))
		if runErr != nil {
			t.Errorf("runReplay(%s): %v", rel(root, transcript), runErr)
			return nil
		}
		ec := report.ExtendedCheck
		if ec == nil {
			return nil
		}
		checked++

		name := rel(root, transcript)
		allDeltas = append(allDeltas, ec.TimeDeltas...)

		if first, ok := firstDrift(ec.TimeDeltas); ok {
			drifted[name] = first
		}
		var anyOver, anyOver5 bool
		for _, dd := range ec.TimeDeltas {
			if dd.Abs() > driftThreshold {
				anyOver = true
			}
			if dd.Abs() > 5*time.Second {
				anyOver5 = true
			}
		}
		if anyOver {
			overThreshold++
		}
		if anyOver5 {
			over5s++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk replaydata/agents: %v", err)
	}
	if checked == 0 {
		t.Fatal("no sidecar-driven recording found under replaydata/agents — the measurement is vacuous")
	}
	// The measurement's own fail-loudly guard (AGENTS.md): a run that reached
	// every recording but produced no comparable pair is not a clean result,
	// it is a broken one, and without this it reads as a pass.
	if len(allDeltas) == 0 {
		t.Fatalf("walked %d sidecar-driven recordings and produced ZERO kind-matched pairs — "+
			"nothing was compared, which is the exact failure #1480 exists to close", checked)
	}

	dist := newDriftDistribution(allDeltas)
	t.Logf("#1480 transition-timing drift, %d sidecar-driven recordings\n%s", checked, dist)

	// The enumeration is the deliverable, so print it whether or not the
	// ratchet trips — with the map literal to paste when it legitimately moves.
	names := make([]string, 0, len(drifted))
	for n := range drifted {
		names = append(names, n)
	}
	sort.Strings(names)
	var literal strings.Builder
	for _, n := range names {
		fmt.Fprintf(&literal, "\t%q: %q,\n", n, fmt.Sprintf("%+.3fs at pair %d (%s)",
			drifted[n].Delta.Seconds(), drifted[n].Index, drifted[n].Kind))
	}
	t.Logf("%d recordings drift >%v on their first kind-matched transition:\n%s",
		len(drifted), driftThreshold, literal.String())

	// A catalog where nothing drifts means the comparison stopped comparing —
	// 20 recordings are known-drifted by #1476's own measurement, and this
	// check exists because reporting zero is the failure mode, not the goal.
	if len(drifted) == 0 {
		t.Fatal("no recording drifts at all — #1476 documents 20 that do, so the measurement is not reaching them")
	}

	var appeared []string
	for _, n := range names {
		if _, known := knownFirstTransitionDrift[n]; !known {
			appeared = append(appeared, fmt.Sprintf("%s (%+.3fs at pair %d, %s)",
				n, drifted[n].Delta.Seconds(), drifted[n].Index, drifted[n].Kind))
		}
	}
	if len(appeared) > 0 {
		t.Errorf("%d recording(s) newly drift >%v on their first transition — a classifier or "+
			"read-boundary change moved WHEN a transition fires, which is exactly what nothing "+
			"could see before #1480:\n  %s\nPaste the printed literal into "+
			"knownFirstTransitionDrift only with a reason per entry.",
			len(appeared), driftThreshold, strings.Join(appeared, "\n  "))
	}
	// Stale entries rot the list into a permanent excuse.
	for n := range knownFirstTransitionDrift {
		if _, still := drifted[n]; !still {
			t.Errorf("knownFirstTransitionDrift entry %q no longer drifts — delete it", n)
		}
	}

	if overThreshold > maxRecordingsDriftingOverThreshold {
		t.Errorf("%d recordings have at least one transition drifting >%v (ratchet: %d) — "+
			"the named list above keys on the FIRST transition only, so this is what catches a "+
			"change that moved a later one", overThreshold, driftThreshold, maxRecordingsDriftingOverThreshold)
	}
	if over5s > maxRecordingsDriftingOver5s {
		t.Errorf("%d recordings have at least one transition drifting >5s (ratchet: %d)",
			over5s, maxRecordingsDriftingOver5s)
	}
	t.Logf("aggregate: %d/%d recordings drift >%v somewhere, %d >5s",
		overThreshold, checked, driftThreshold, over5s)
}
