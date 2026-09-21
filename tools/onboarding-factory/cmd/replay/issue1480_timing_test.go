package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
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

// wantPair is one expected (index, kind, delta) triple. One array of these
// rather than three parallel want_* arrays: the parallel form needs a
// hand-written same-length guard because the representation permits an
// inconsistency, and the comparison fans out into three branches.
type wantPair struct {
	Index   int    `json:"index"`
	Kind    string `json:"kind"`
	DeltaNs int64  `json:"delta_ns"`
}

type timingCase struct {
	Description string            `json:"description"`
	Recorded    []lifecycle.Event `json:"recorded"`
	Replayed    []transition      `json:"replayed"`
	Want        []wantPair        `json:"want"`
	WantFirstNs *int64            `json:"want_first_drift"`
}

// TestTransitionTimeDeltas_Corpus is #1480's mutation evidence. Every case is a
// timing shape the measurement must classify correctly, and the corpus carries
// both verdicts on purpose: first-transition-31s-early.json must be reported,
// aligned.json and sub-second-justified.json must not. A detector that flags
// everything and one that flags correctly are indistinguishable without the
// second kind, which is why the balance is asserted below rather than left to
// whoever adds the next case.
//
// The per-file body is runTimingCorpusCase: it takes this fixture's own name
// and returns THIS fixture's own drift/clean classification, so the split
// carries no data across the function boundary that is not that one case's.
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
			if runTimingCorpusCase(t, dir, name) {
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

// runTimingCorpusCase loads and asserts one #1480 corpus fixture (dir/name):
// its shape, compareOrdered's classification of it against what the fixture
// pins, and firstDrift's verdict. It reports whether the fixture is a "drift"
// case (want_first_drift set) or a "clean" one, which is the one piece of
// information TestTransitionTimeDeltas_Corpus's drift/clean balance guard
// needs back — everything else about the case stays local to this function,
// the same case data it was constructed from.
func runTimingCorpusCase(t *testing.T, dir, name string) bool {
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
	// And a case that expects no pairs asserts nothing either, while
	// still passing and still counting toward the drift/clean balance
	// guard below. A probe with kind-mismatched sides and no want_*
	// fields at all passed and incremented sawClean before this check
	// existed, which would have satisfied that guard on its own.
	if len(tc.Want) == 0 {
		t.Fatalf("%s expects no measured pairs — a corpus row must pin at least one "+
			"pair, or it passes without asserting anything", name)
	}

	// compareOrdered is the production pairing, and since #1480 it is
	// the only one — the corpus therefore exercises the function the
	// daemon-comparison actually runs, not a parallel copy of it.
	got, _ := compareOrdered(tc.Recorded, tc.Replayed)

	want := make([]timeDelta, 0, len(tc.Want))
	for _, w := range tc.Want {
		want = append(want, timeDelta{Index: w.Index, Kind: w.Kind, Delta: time.Duration(w.DeltaNs)})
	}
	// Index is the INPUT pair position, not the output slot, and the
	// two differ exactly where a pair was excluded — which is what
	// kind-mismatch-excluded.json pins. Every "at pair N" in the
	// enumeration below is that number, so a regression writing the
	// output slot there would leave the deltas right and every
	// reported position wrong.
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s:\n  got  %v\n  want %v", tc.Description, got, want)
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

	return tc.WantFirstNs != nil
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
// dominated when #1480 first enumerated this list at 65 entries:
//
//   - copilot and antigravity recordings whose first turn opens with a long
//     tool call: the daemon classified on a read that reached content the
//     replay's clamped boundary does not, so the replay fires at the debounce
//     deadline instead.
//   - pi and gemini-cli recordings sitting at almost exactly +2.00s: a
//     synthesized debounce-deadline stamp, one full window after the last
//     coalesced fs event, where the daemon's own timer had fired earlier.
//
// #1478 then removed the SECOND family almost entirely, which is the strongest
// evidence that its cluster window models something real rather than fitting
// this catalog. 17 entries left the list and none joined it, and all 17 were
// pi recordings sitting at +2.002s..+2.045s. The mechanism is direct: those
// recordings write their first turn in one burst, so the widened first pass
// now classifies in-pass — at the daemon's own instant — instead of skipping
// on #329's guard and deferring the transition to the debounce flush 2s later.
// The +2.00s signature was the deferral, not a timer the daemon actually ran.
//
// The remaining 48 are not #1480's to fix and were not #1478's either. #1480
// is the measurement; what to do about the populations it reveals is argued in
// each PR with this list in hand.
var knownFirstTransitionDrift = map[string]string{
	"deepseek-harness/scenarios/2-5_synchronous-slash-command/recordings/2026-09-19-01-18-31_irrlichd-0.6.4+b4a1949/transcript.jsonl.zstd": "#1985 core fixture, measured during #1986: -3.910s at pair 0 (ready→working); replay first activity is 01:18:36.216, while events.jsonl records the daemon transition at 01:18:40.126. The source of the read delay is not established.",
	"deepseek-harness/scenarios/2-7_autonomous-loop/recordings/2026-09-19-01-24-19_irrlichd-0.6.4+860f6c4/transcript.jsonl.zstd":           "#1986 measured: -2.101s at pair 0 (ready→working); the replay fires on first turn_start at 01:24:20.166, while events.jsonl records the daemon transition at 01:24:22.267. The source of the read delay is not established.",
	"claudecode/scenarios/2-8_autonomous-loop-iteration-limit/recordings/2026-09-07-22-36-31_irrlichd-0.6.2+c858d61/transcript.jsonl":      "the first Desktop recording driven by a `slash` step: +2.489s at pair 0 (ready→working). Claude Desktop creates the session only when the prompt is SUBMITTED, so the row is born and goes working within milliseconds, while the replay reconstructs the boundary from the transcript it can read. Same shape as every other desktop-local first pair.",
	"antigravity/scenarios/1-1_session-start/recordings/2026-06-20-12-33-40_irrlichd-0.5.2+be45695/transcript.jsonl":                       "pre-dates #1476: -6.609s at pair 0 (ready→working)",
	"antigravity/scenarios/1-2_session-end/recordings/2026-06-20-12-36-01_irrlichd-0.5.2+a00b826/transcript.jsonl":                         "pre-dates #1476: -4.304s at pair 0 (ready→working)",
	"antigravity/scenarios/1-3_long-idle-live-session/recordings/2026-06-20-17-58-44_irrlichd-0.5.2+1fcae3a/transcript.jsonl":              "pre-dates #1476: -3.937s at pair 0 (ready→working)",
	"antigravity/scenarios/1-4_session-resume/recordings/2026-06-20-12-43-04_irrlichd-0.5.2+bb79f4c/transcript.jsonl":                      "pre-dates #1476: -3.771s at pair 0 (ready→working)",
	"antigravity/scenarios/2-11_auto-classified-permission/recordings/2026-06-20-13-41-55_irrlichd-0.5.2+b756a52/transcript.jsonl":         "pre-dates #1476: -5.774s at pair 0 (ready→working)",
	"antigravity/scenarios/2-12_context-compaction/recordings/2026-06-20-13-46-30_irrlichd-0.5.2+77308b9/transcript.jsonl":                 "pre-dates #1476: -3.809s at pair 0 (ready→working)",
	"antigravity/scenarios/2-13_turn-end-terminal-text/recordings/2026-06-20-02-50-41_irrlichd-0.5.2+2900672/transcript.jsonl":             "pre-dates #1476: -3.522s at pair 0 (ready→working)",
	"antigravity/scenarios/2-15_shell-escape-command/recordings/2026-06-20-13-49-04_irrlichd-0.5.2+349bafc/transcript.jsonl":               "pre-dates #1476: -4.067s at pair 0 (ready→working)",
	"antigravity/scenarios/2-16_oversized-transcript-line/recordings/2026-06-20-13-18-38_irrlichd-0.5.2+76d805a/transcript.jsonl":          "pre-dates #1476: -67.369s at pair 0 (ready→working)",
	"antigravity/scenarios/2-1_basic-turn/recordings/2026-06-20-02-48-55_irrlichd-0.5.2+daeb6e1/transcript.jsonl":                          "pre-dates #1476: -3.199s at pair 0 (ready→working)",
	"antigravity/scenarios/2-21_streaming-partial-writes/recordings/2026-06-20-13-21-01_irrlichd-0.5.2+c7f1c6d/transcript.jsonl":           "pre-dates #1476: -5.111s at pair 0 (ready→working)",
	"antigravity/scenarios/2-4_self-correction-iteration/recordings/2026-06-20-13-14-32_irrlichd-0.5.2+a489d47/transcript.jsonl":           "pre-dates #1476: -4.859s at pair 0 (ready→working)",
	"antigravity/scenarios/2-5_synchronous-slash-command/recordings/2026-06-20-13-38-51_irrlichd-0.5.2+7a48e0d/transcript.jsonl":           "pre-dates #1476: -3.726s at pair 0 (ready→working)",
	"antigravity/scenarios/2-7_autonomous-loop/recordings/2026-06-20-14-00-16_irrlichd-0.5.2+250cd02/transcript.jsonl":                     "pre-dates #1476: -4.521s at pair 0 (ready→working)",
	"antigravity/scenarios/2-8_autonomous-loop-iteration-limit/recordings/2026-06-20-14-02-53_irrlichd-0.5.2+2de7ce6/transcript.jsonl":     "pre-dates #1476: -5.681s at pair 0 (ready→working)",
	"antigravity/scenarios/3-1_foreground-subagent/recordings/2026-06-20-18-04-13_irrlichd-0.5.2+09e9cf9/transcript.jsonl":                 "pre-dates #1476: -6.595s at pair 0 (ready→working)",
	"antigravity/scenarios/3-2_background-subagent/recordings/2026-06-20-18-15-41_irrlichd-0.5.2+b305884/transcript.jsonl":                 "pre-dates #1476: -4.468s at pair 0 (ready→working)",
	"antigravity/scenarios/4-2_multiple-agents-same-workspace/recordings/2026-06-20-16-53-31_irrlichd-0.5.2+2eb84fb/transcript.jsonl":      "pre-dates #1476: -5.079s at pair 0 (ready→working)",
	"antigravity/scenarios/5-1_token-accounting/recordings/2026-06-28-11-54-27_irrlichd-0.5.3+7f52a76/transcript.jsonl":                    "pre-dates #1476: -3.343s at pair 0 (ready→working)",
	"claudecode/scenarios/2-7_autonomous-loop/recordings/2026-05-18-22-47-11_irrlichd-0.4.5+8499138/transcript.jsonl":                      "#1476 accepted: -5.075s at pair 0 (ready→working)",
	"claudecode/scenarios/2-7_autonomous-loop/recordings/2026-05-18-22-52-38_irrlichd-0.4.5+8499138/transcript.jsonl":                      "#1476 accepted: -4.561s at pair 0 (ready→working)",
	"claudecode/scenarios/2-8_autonomous-loop-iteration-limit/recordings/2026-05-18-22-56-34_irrlichd-0.4.5+6898561/transcript.jsonl":      "#1476 accepted: -9.012s at pair 0 (ready→working)",
	"claudecode/scenarios/5-8_task-estimate-marker/recordings/2026-06-03-21-50-21_irrlichd-0.4.8+defb4d8/transcript.jsonl":                 "pre-dates #1476: -1.167s at pair 0 (ready→working)",
	"codex/regressions/fork-conversation/recordings/2026-05-23-19-39-41_irrlichd-0.4.7+3c6427c/transcript.jsonl":                           "#1476 accepted: -3.759s at pair 0 (ready→working)",
	"codex/regressions/turn-end-terminal-text-pre-hooks/recordings/2026-05-23-21-44-53_irrlichd-0.4.7+f83dc27/transcript.jsonl":            "#1476 accepted: -2.246s at pair 0 (ready→working)",
	"codex/scenarios/1-4_session-resume/recordings/2026-05-23-20-30-54_irrlichd-0.4.7+c461eef/transcript.jsonl":                            "#1476 accepted: -4.007s at pair 0 (ready→working)",
	"codex/scenarios/1-5_session-reset/recordings/2026-05-25-00-26-42_irrlichd-0.4.7+9d1ef56.dirty/transcript.jsonl":                       "#1476 accepted: -3.673s at pair 0 (ready→working)",
	"codex/scenarios/2-15_shell-escape-command/recordings/2026-05-23-22-23-45_irrlichd-0.4.7+7075bff/transcript.jsonl":                     "#1476 accepted: -2.032s at pair 0 (ready→working)",
	"codex/scenarios/2-18_user-blocking-plan-mode-approval/recordings/2026-05-23-23-48-22_irrlichd-0.4.7+eb984db/transcript.jsonl":         "#1476 accepted: -5.670s at pair 0 (ready→working)",
	"codex/scenarios/2-1_basic-turn/recordings/2026-05-23-20-36-20_irrlichd-0.4.7+723609a/transcript.jsonl":                                "#1476 accepted: -3.691s at pair 0 (ready→working)",
	"codex/scenarios/2-20_interrupted-turn/recordings/2026-05-14-23-36-59_irrlichd-dev/transcript.jsonl":                                   "pre-dates #1476: +2.187s at pair 0 (ready→working)",
	"codex/scenarios/2-20_interrupted-turn/recordings/2026-05-16-22-53-47_irrlichd-0.3.13+4662be4/transcript.jsonl":                        "pre-dates #1476: +2.214s at pair 0 (ready→working)",
	"codex/scenarios/2-20_interrupted-turn/recordings/2026-05-16-22-56-57_irrlichd-0.3.13+4662be4/transcript.jsonl":                        "pre-dates #1476: +2.652s at pair 0 (ready→working)",
	"codex/scenarios/2-2_auto-executed-tool-call/recordings/2026-05-23-22-48-46_irrlichd-0.4.7+14e1e7a/transcript.jsonl":                   "#1476 accepted: -2.234s at pair 0 (ready→working)",
	"codex/scenarios/2-5_synchronous-slash-command/recordings/2026-05-23-21-53-28_irrlichd-0.4.7+6934b22/transcript.jsonl":                 "#1476 accepted: -3.615s at pair 0 (ready→working)",
	"codex/scenarios/2-6_long-agentic-session-stress/recordings/2026-05-23-23-03-45_irrlichd-0.4.7+ccb564c/transcript.jsonl":               "#1476 accepted: -2.230s at pair 0 (ready→working)",
	"codex/scenarios/2-7_autonomous-loop/recordings/2026-05-23-23-15-25_irrlichd-0.4.7+774c575/transcript.jsonl":                           "#1476 accepted: -2.287s at pair 0 (ready→working)",
	"codex/scenarios/2-8_autonomous-loop-iteration-limit/recordings/2026-05-23-23-20-25_irrlichd-0.4.7+6a1a098/transcript.jsonl":           "#1476 accepted: -6.782s at pair 0 (ready→working)",
	"codex/scenarios/4-1_multiple-sessions-same-cwd/recordings/2026-05-24-21-42-19_irrlichd-0.4.7+83509be.dirty/transcript.jsonl":          "#1476 accepted: -3.775s at pair 0 (ready→working)",
	"codex/scenarios/5-3_model-switch-midsession/recordings/2026-05-23-21-34-12_irrlichd-0.4.7+b8b1cfc/transcript.jsonl":                   "#1476 accepted: -4.079s at pair 0 (ready→working)",
	"copilot/scenarios/1-1_session-start/recordings/2026-08-03-12-06-21_irrlichd-0.5.9+4c1b2e4/transcript.jsonl":                           "pre-dates #1476: -3.842s at pair 0 (ready→working)",
	"copilot/scenarios/1-2_session-end/recordings/2026-08-03-12-07-28_irrlichd-0.5.9+57d59de/transcript.jsonl":                             "pre-dates #1476: -10.828s at pair 0 (ready→working)",
	"copilot/scenarios/1-4_session-resume/recordings/2026-08-05-19-02-01_irrlichd-0.5.9+4b58365/transcript.jsonl":                          "pre-dates #1476: -18.621s at pair 1 (working→ready)",
	"copilot/scenarios/2-13_turn-end-terminal-text/recordings/2026-08-03-12-16-12_irrlichd-0.5.9+ced85ef/transcript.jsonl":                 "pre-dates #1476: -4.190s at pair 0 (ready→working)",
	"gemini-cli/scenarios/2-12_context-compaction/recordings/2026-06-12-10-36-37_irrlichd-0.5.1+93db11a/transcript.jsonl":                  "pre-dates #1476: +2.001s at pair 0 (ready→working)",
	"mistral-vibe/scenarios/2-12_context-compaction/recordings/2026-07-07-17-22-57_irrlichd-0.5.5+bc77a37.dirty/transcript.jsonl":          "#1476 accepted: -30.976s at pair 0 (ready→working)",
	"mistral-vibe/scenarios/2-15_shell-escape-command/recordings/2026-07-07-17-41-58_irrlichd-0.5.5+22a01d2.dirty/transcript.jsonl":        "#1476 accepted: -8.599s at pair 0 (ready→working)",
	"mistral-vibe/regressions/1846-retired-terminal-control/recordings/2026-07-08-09-15-24_irrlichd-0.5.5+35c4012.dirty/transcript.jsonl":  "#1476 accepted: -27.522s at pair 0 (ready→working)",
	"muse/scenarios/4-1_multiple-sessions-same-cwd/recordings/2026-09-14-06-04-09_irrlichd-0.6.3+0b3e2ad/transcript.jsonl":                 "#1960: -32.388s at pair 0 (ready→working). Pre-fix capture (predates fix commit 9abf13d1) hitting the SAME assignPIDLocked PID-eviction bug that blocked 2-14/2-17: session 01a09e16 born ready at seq152, evicted by a late-arriving proc-53461 pre-session claim at seq170, re-discovered only via a LATER, different session's own birth at seq456. 4-1's own cell assertions don't key on this transition (stayed a clean 5/5 pass), so it was not re-recorded as part of #1960's fix.",
	"muse/scenarios/2-15_shell-escape-command/recordings/2026-09-14-20-47-26_irrlichd-0.6.3+d2dc680/transcript.jsonl":                      "#1960: -2.242s at pair 0 (ready\u2192working). The 2s debounce window, same mechanism this file already documents for claudecode's Stop above \u2014 not a muse defect and not a classifier change. Measured in this recording's own events.jsonl: a burst of transcript_activity at 20:47:33.659-33.667 (seq 521-528) is coalesced on every write (debounce_coalesced at each), each one re-opening the window, and the daemon's state_transition lands at 20:47:35.708 (seq 529) \u2014 2.041s after the last coalesced write, i.e. at the next debounce boundary. The replay sidecar flips at the transcript bytes themselves, which is where the 2.242s comes from. A bound that absorbed it would hide the next one.",
	"muse/scenarios/2-5_synchronous-slash-command/recordings/2026-09-14-21-41-15_irrlichd-0.6.3+3b16079/transcript.jsonl":                  "#1960: -5.623s at pair 0 (ready\u2192working). NOT a debounce boundary alone \u2014 the two sides pick DIFFERENT writes as the turn start, which is worth stating because it is the one entry here that is a genuine classification divergence rather than a timer. Measured in this recording's own events.jsonl: the sidecar flips at the first transcript_activity, 21:41:20.031, which is the LOCAL slash command's own write (/usage and /status resolve in-process, emitting command.invoked and no turn). The daemon does not treat that write as a turn start, correctly; it flips at 21:41:25.654 (seq 526), 2.233s after the real prompt's coalesced burst at 21:41:23.407-23.421, i.e. at that burst's own debounce boundary. 25.654 - 20.031 = 5.623 exactly. Left as committed evidence that the replay sidecar starts a turn on a local-command write where the daemon does not.",
}

// aggregate ratchets. The named list above keys on the FIRST kind-matched pair,
// which is the figure a read-boundary model moves and the one #1476 reported.
// A classifier change that moved only a LATER transition would slip past it
// entirely, so the two scalars below hold the whole population — every
// kind-matched pair in every recording — without a second list of names to
// maintain.
// #1388 moved the first of these from 105 to 107. It added codex's two
// hook-bearing recordings (2-13, 2-19), and BOTH drift >1s on a later pair
// while neither drifts on its first — which is why they are absent from the
// machine-generated list above and visible only here, i.e. exactly the case
// these two scalars exist for.
//
// The cause is structural rather than a defect the recordings carry. In both,
// the drifting pair is the one a HOOK asserts (PermissionRequest → waiting,
// Stop → ready): the daemon flips on the POST, replay flips at the next
// debounce boundary, and the settings those goldens record put that boundary
// at 2s. Measured on 2-19: hook_received at 00:40:47.019, golden
// virtual_time 00:40:48.977 — 1.96s, one debounce window. It is the same
// bimodal population `driftThreshold` was cut from, not a new mode.
//
// #1695 moved it back, 107 -> 105, and the reason is the same sentence read in
// reverse. That "structural" cause was a defect after all: replay flipped at
// the next debounce boundary because applyHookEvent carried a stale per-pass
// NoSubstantiveActivity into the hook's own pass, so #329's short-circuit ate
// the hook-time classification and the transition fell through to the next
// debounce flush. With that fixed, both codex recordings flip at the hook's own
// timestamp — 2-19's pair is now 00:40:47.019 against a golden virtual_time of
// 00:40:47.019 — and both leave this population, taking it back below where
// #1388 found it. Re-tightened rather than left at 107, per the #1517 note
// below: a bound that absorbs an improvement as slack makes the next
// regression free.
//
// #1699 moved it 105 -> 106, and the entry is the SAME sentence read a third
// time, now from the daemon's side. It records claudecode's
// 2-13_turn-end-terminal-text, the catalog's first claudecode Stop, whose pair 1
// (working→ready) is +2.100s: replay flips at the hook's own timestamp
// (20:00:18.932775, byte-identical to the sidecar's hook_received{Stop} ts)
// while the DAEMON flipped at 20:00:21.033255. The daemon's flip is still the
// hook's — sidecar seq 187 carries decided_by_tier "hook" and hook_turn_done
// true — it simply landed at the next debounce boundary, because
// dispatchHookActivity pushes its synthetic event onto the DEBOUNCED channel and
// a transcript write 97ms after the POST (seq 184) re-opened the 2s window. So
// the pre-#1695 sentence ("the daemon flips on the POST, replay flips at the
// next debounce boundary") is now true with the sides swapped, and which side a
// recording lands on is a property of what the agent CLI wrote in the 2s after
// its own Stop. Not re-tightened away and not a defect the recording carries:
// it is one of the two facts #1699 was recorded to make visible, and a bound
// that absorbed it would hide the next one.
// #1960 moves both bounds 107 -> 108 and 51 -> 52 for the same new recording:
// muse/4-1_multiple-sessions-same-cwd's pre-fix capture
// (2026-09-14-06-04-09_irrlichd-0.6.3+0b3e2ad), newly committed alongside the
// #1960 daemon fix (commit 9abf13d1) and its own two re-recorded cells. Its
// raw events.jsonl (seq 151-170) shows the SAME root cause as those two cells:
// session 01a09e16 is born ready at seq152, a proc-53461 pre-session claims
// its already-bound PID at seq168, and assignPIDLocked's pre-fix stale-scan
// deletes 01a09e16 at seq170 (transcript_removed, no re-discovery until a
// LATER, different session's own birth at seq456). The replay sidecar
// reconstructs the swallowed first transition from the transcript bytes,
// -32.388s from where the (pre-fix) daemon's own log next picks the scenario
// up. This did not need a spec correction — 4-1's own assertions do not key
// on that specific transition, so its cell stayed a clean 5/5 pass — so it is
// not one of #1960's two re-recorded cells and is left as committed evidence
// of the pre-fix behavior rather than re-recorded here.
// #1960's recording pass then moves both bounds again, 108 -> 111 and
// 52 -> 54, for its twelve new muse recordings. Only one of them is a NEW
// first-transition drift (2-15_shell-escape-command, entry above, the 2s
// debounce boundary); the rest drift on a LATER transition, which is the case
// the named list cannot see and these two counts exist to catch. Muse drifts
// more than most adapters for a structural reason worth stating: every muse
// prompt spawns internal reminder micro-agents that write to their own
// transcripts under the same process, so a parent's window is re-opened by
// writes that are not the parent's own turn.
// #1960's final nine recordings move them once more, 111 -> 113 and 54 -> 56.
// One new first-transition drift (2-5_synchronous-slash-command, entry above),
// and it is the interesting kind: the sidecar starts the turn on a local slash
// command's write, the daemon does not.
// The keys-primitive batch adds three more recordings and moves only the >1s
// count, 113 -> 114, with no new FIRST-transition drift — the extra one drifts
// on a later transition, which is exactly the case this count exists to catch.
// #1986 measured three additional >1s recordings with
// `go test ./tools/onboarding-factory/cmd/replay -run
// TestSidecarReplayTransitionTimesMatchTheDaemonsOwnLog -v -count=1`:
// 2-5 is the merged #1985 core fixture, 2-7 is a new first-turn drift, and
// 5-4 is a later waiting→working pair. Only 5-4 exceeds 5s: replay fires at
// 04:23:39.682, while the daemon sidecar records 04:23:48.430. This names
// observed timing, not a proven cause of the delayed daemon read.
// #1980's final DSH fixture tree moves the aggregate bounds 118 -> 123 and
// 57 -> 62. The generator command below measured 123/433 recordings over 1s,
// 62 over 5s, 1094 kind-matched pairs, and 366 measured recordings. The new
// DSH recordings add no first-transition entry to the generated map, but they
// do add later-transition drift. Re-tighten both upper bounds and both
// population floors so this fixture growth is not left as slack.
// `go test ./tools/onboarding-factory/cmd/replay -run
// TestSidecarReplayTransitionTimesMatchTheDaemonsOwnLog -v -count=1` produced
// these values on the final fixture tree.
const (
	maxRecordingsDriftingOverThreshold = 123
	maxRecordingsDriftingOver5s        = 62
)

// Lower bounds on HOW MUCH is measured. Every ratchet above is an upper bound,
// and upper bounds alone are satisfied by a measurement that shrinks: a
// classifier change that shifts replay transitions one position turns
// kind-matched pairs into state_differs, which are excluded, so the drift
// counts FALL and every assertion above passes while the population quietly
// drains. Regenerating goldens then erases the only other trace, since
// ordered_matches is the serialized field that would have moved.
//
// 36 recordings already produce zero kind-matched pairs, so "this recording
// measures nothing" is the current state of over a tenth of the catalog rather
// than a hypothetical — which is exactly why the floor is a number and not
// merely a non-zero check.
//
// Both floors are re-ratcheted whenever the population grows, and #1517 is why
// that is spelled out rather than left to judgement. It widened the walk to
// pair transcript.md, adding two recordings and two pairs; the floors were
// sitting at exactly the old measured values, so they absorbed the growth as
// slack and this gate stayed green under a mutation that narrowed the pairing
// straight back. A floor whose stated job is catching a pairing shift that
// drains the population has to be re-tightened onto the new measurement, or
// the very next drain is free.
// Re-tightened onto the new measurement by #1388, per the paragraph above:
// its two codex recordings took the population from 828/275 to 832/277.
// Leaving the old values would have let the growth sit as slack, which is
// precisely the #1517 mistake that paragraph records.
// #1695 re-tightened the pair floor again, 832 -> 834: replay now reproduces
// codex 2-13's hook-driven transition and the two it had been hiding, so that
// recording contributes two more kind-matched pairs. The recording floor does
// not move — 2-13 was already measuring — which is the shape to expect here,
// since a recording joins this population once and then only adds pairs.
// #1699 re-tightens both, 834 -> 836 and 277 -> 278: the new claudecode
// 2-13 recording is a recording that was not in this population at all, and it
// contributes two kind-matched pairs. Both halves move because this is the
// other case the sentence above names — a NEW recording joining, rather than an
// existing one gaining pairs.
//
// #1799 is the FIRST time this floor moves DOWN, 836 -> 835, and that direction
// is the one this gate exists to make expensive — so the reason is measured
// rather than asserted. The whole drop is claudecode
// 2-14_turn-aborted-by-error: its committed golden's ordered_matches goes 4 ->
// 1, because the recording's two terminal API errors now replay as
// `working→error` and `error→working` where the frozen sidecar logged
// `working→ready` and `ready→working`. A kind that differs is a state_differs,
// not a kind-matched pair, so three pairs leave the population. Measured, not
// inferred: `go test -run TestSidecarReplayTransitionTimesMatchTheDaemonsOwnLog
// -v` reports "838 pairs across 279 recordings" at 32ec3efb and "835 pairs
// across 279 recordings" here. The recording count does not move — 2-14 still
// contributes its remaining pair — which is the shape to expect for an existing
// recording losing pairs rather than dropping out.
//
// This is the frozen-sidecar cost of the fourth state, not a replay-fidelity
// bug: those sidecars were recorded before `error` existed, so the daemon they
// are compared against could not have produced it. It resolves when 2-14 is
// re-recorded, and #1800 will move this floor again for the same reason on its
// own adapters.
//
// Re-tightened onto the new measurement, per the paragraph above, rather than
// left as slack. minMeasuredRecordings is deliberately NOT re-tightened even
// though it measures 279 against a floor of 278: neither change moved it, and
// narrowing an untouched ratchet mid-change would fail a later diff for a
// reason that diff does not contain.
//
// #1800 IS THE SECOND DOWNWARD MOVE, and it is the one #1799's paragraph above
// predicted in as many words — "#1800 will move this floor again for the same
// reason on its own adapters". 835 -> 833, two pairs, and the two are named:
//
//	gemini-cli 2-14 (…2026-06-13-20-23-43…)  ordered_matches 2 -> 1
//	pi         2-14 (…2026-05-25-05-20-53…)  ordered_matches 2 -> 1
//
// Measured per golden with
//
//	git show origin/main:<golden> | jq '.extended_check.ordered_matches'
//
// against the same expression over the working copy — not inferred from the
// number of goldens the change rewrote, which is five. Both are
// turn-aborted-by-error cells whose failing turn now replays `working→error`
// where the frozen sidecar logged `working→ready`; a differing kind is a
// state_differs rather than a kind-matched pair, so one pair leaves each. The
// recording count does not move, which is the shape to expect for existing
// recordings losing pairs rather than dropping out.
//
// Same frozen-sidecar cost as #1799's, for the same reason, and it resolves the
// same way: on re-record.
const (
	minKindMatchedPairs   = 1094
	minMeasuredRecordings = 366
)

// reportDriftEnumeration prints the drifted set — the deliverable of #1480,
// printed whether or not any ratchet trips — as the map literal to paste into
// knownFirstTransitionDrift when the set legitimately moves. That literal is
// why the list is machine-generated rather than hand-maintained. Returns the
// names sorted, which the caller's two ratchets then walk.
func reportDriftEnumeration(t *testing.T, drifted map[string]timeDelta) []string {
	t.Helper()
	names := make([]string, 0, len(drifted))
	for n := range drifted {
		names = append(names, n)
	}
	sort.Strings(names)

	var literal strings.Builder
	for _, n := range names {
		fmt.Fprintf(&literal, "\t%q: %q,\n", n, describeDrift(drifted[n]))
	}
	t.Logf("%d recordings drift >%v on their first kind-matched transition:\n%s",
		len(drifted), driftThreshold, literal.String())
	return names
}

// describeDrift is the one spelling of a drift figure, shared by the pasteable
// literal and the newly-drifted error so a reader comparing the two is looking
// at the same rendering.
func describeDrift(d timeDelta) string {
	return fmt.Sprintf("%+.3fs at pair %d (%s)", d.Delta.Seconds(), d.Index, d.Kind)
}

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
// because 24.3% of the committed catalog's transitions are already over 1s from
// their daemon, and a gate that failed on all of them would be reverted within
// the day and would protect nothing.
func TestSidecarReplayTransitionTimesMatchTheDaemonsOwnLog(t *testing.T) {
	var allDeltas []timeDelta
	drifted := map[string]timeDelta{}
	unmeasured := map[string]bool{}
	overThreshold, over5s := 0, 0

	checked := forEachSidecarRecording(t, func(name string, ec *extendedCheck) {
		allDeltas = append(allDeltas, ec.TimeDeltas...)
		if len(ec.TimeDeltas) == 0 {
			unmeasured[name] = true
		}
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
	})
	measured := checked - len(unmeasured)
	// The measurement's own fail-loudly guard (AGENTS.md): a run that reached
	// every recording but produced no comparable pair is not a clean result,
	// it is a broken one, and without this it reads as a pass.
	if len(allDeltas) == 0 {
		t.Fatalf("walked %d sidecar-driven recordings and produced ZERO kind-matched pairs — "+
			"nothing was compared, which is the exact failure #1480 exists to close", checked)
	}

	dist := newDriftDistribution(allDeltas)
	t.Logf("#1480 transition-timing drift, %d sidecar-driven recordings\n%s", checked, dist)

	names := reportDriftEnumeration(t, drifted)

	// A catalog where nothing drifts means the comparison stopped comparing —
	// 20 recordings are known-drifted by #1476's own measurement, and this
	// check exists because reporting zero is the failure mode, not the goal.
	if len(drifted) == 0 {
		t.Fatal("no recording drifts at all — #1476 documents 20 that do, so the measurement is not reaching them")
	}

	var appeared []string
	for _, n := range names {
		if _, known := knownFirstTransitionDrift[n]; !known {
			appeared = append(appeared, fmt.Sprintf("%s (%s)", n, describeDrift(drifted[n])))
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
		if _, still := drifted[n]; still {
			continue
		}
		// firstDrift returns false for two opposite reasons, and reporting
		// them the same way tells a developer to DELETE COVERAGE in response
		// to a regression: the drift genuinely shrank, or the recording
		// stopped producing kind-matched pairs at all, which means replay no
		// longer reproduces that transition and the entry is the last thing
		// that should be removed.
		if unmeasured[n] {
			t.Errorf("knownFirstTransitionDrift entry %q stopped producing kind-matched pairs — "+
				"replay no longer reproduces its transitions at all. Do NOT delete the entry; "+
				"this is a regression, not a fix", n)
			continue
		}
		t.Errorf("knownFirstTransitionDrift entry %q no longer drifts — delete it", n)
	}

	if len(allDeltas) < minKindMatchedPairs {
		t.Errorf("only %d kind-matched pairs measured (floor: %d) — the measurement SHRANK. "+
			"Every other assertion here is an upper bound and passes when it does, so this is "+
			"the one that catches a pairing shift draining the population",
			len(allDeltas), minKindMatchedPairs)
	}
	if measured < minMeasuredRecordings {
		t.Errorf("only %d of %d recordings produced any kind-matched pair (floor: %d)",
			measured, checked, minMeasuredRecordings)
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
	t.Logf("aggregate: %d/%d recordings drift >%v somewhere, %d >5s; "+
		"%d pairs measured across %d recordings (%d produced none)",
		overThreshold, checked, driftThreshold, over5s,
		len(allDeltas), measured, len(unmeasured))
}

// ---------------------------------------------------------------------------
// 3. The reporting helpers.
// ---------------------------------------------------------------------------

// mkBucketTestDeltas builds synthetic timeDelta values at the given
// magnitudes, all under the same kind, for the bucket/percentile subtests
// below. Package-level rather than a closure so each subtest can be its own
// named top-level function (see TestDriftDistribution_BucketsAndPercentiles) —
// it carries no assertion of its own, only fixture construction.
func mkBucketTestDeltas(ds ...time.Duration) []timeDelta {
	out := make([]timeDelta, 0, len(ds))
	for i, d := range ds {
		out = append(out, timeDelta{Index: i, Kind: "ready→working", Delta: d})
	}
	return out
}

// TestDriftDistribution_BucketsAndPercentiles pins the code that produces the
// histogram, because that histogram IS the stated justification for
// driftThreshold — it is pasted verbatim into that constant's doc comment and
// into AGENTS.md. Untested, a bucketing or interpolation bug would silently
// rewrite the rationale for the constant rather than fail anything.
//
// Each case below is its own named top-level function rather than an inline
// t.Run closure: cognitive-complexity scoring (go:S3776) counts control flow
// inside a closure literal toward the ENCLOSING function, so five independent,
// already-self-contained subtests summed into one number that says nothing
// about any of them individually. Naming them does not move any assertion
// away from the data it checks — every case still builds its own input and
// checks it in the same place it always did.
func TestDriftDistribution_BucketsAndPercentiles(t *testing.T) {
	t.Run("one delta per bucket lands in its own bucket", testDriftBucket_OnePerBucket)
	t.Run("bucket edges are upper-exclusive", testDriftBucket_EdgesAreUpperExclusive)
	t.Run("sign is ignored — buckets are taken over magnitude", testDriftBucket_SignIsIgnored)
	t.Run("percentiles", testDriftBucket_Percentiles)
	t.Run("degenerate inputs do not panic", testDriftBucket_DegenerateInputsDoNotPanic)
}

func testDriftBucket_OnePerBucket(t *testing.T) {
	// Deliberately one value strictly inside each of the nine buckets,
	// plus the two edge values that decide the open/closed question.
	dist := newDriftDistribution(mkBucketTestDeltas(
		500*time.Microsecond, // <1ms
		5*time.Millisecond,   // 1-10ms
		50*time.Millisecond,  // 10-100ms
		500*time.Millisecond, // 0.1-1s
		2*time.Second,        // 1-5s
		7*time.Second,        // 5-10s
		20*time.Second,       // 10-30s
		45*time.Second,       // 30-60s
		2*time.Minute,        // >60s
	))
	if dist.N != 9 {
		t.Fatalf("N = %d, want 9", dist.N)
	}
	for i, label := range driftBucketLabels {
		if dist.BucketCount[i] != 1 {
			t.Errorf("bucket %q = %d, want 1 (buckets: %v)", label, dist.BucketCount[i], dist.BucketCount)
		}
	}
}

func testDriftBucket_EdgesAreUpperExclusive(t *testing.T) {
	// Exactly 1s belongs to "1-5s", not "0.1-1s". This is the one place
	// the histogram and driftThreshold deliberately disagree — a delta of
	// exactly 1s is counted above the line here but is NOT drifted by
	// firstDrift, which compares with >. The disagreement is one
	// nanosecond wide and is pinned so it stays deliberate.
	dist := newDriftDistribution(mkBucketTestDeltas(time.Second))
	if got := dist.BucketCount[bucketIndex(time.Second)]; got != 1 {
		t.Fatalf("bucket count for exactly 1s = %d, want 1", got)
	}
	if driftBucketLabels[bucketIndex(time.Second)] != "1-5s" {
		t.Errorf("exactly 1s bucketed as %q, want \"1-5s\"", driftBucketLabels[bucketIndex(time.Second)])
	}
	if _, drifted := firstDrift(mkBucketTestDeltas(time.Second)); drifted {
		t.Error("exactly 1s reported as drifted; firstDrift must compare with >, not >=")
	}
}

func testDriftBucket_SignIsIgnored(t *testing.T) {
	neg := newDriftDistribution(mkBucketTestDeltas(-30 * time.Second))
	pos := newDriftDistribution(mkBucketTestDeltas(30 * time.Second))
	if !reflect.DeepEqual(neg.BucketCount, pos.BucketCount) {
		t.Errorf("negative and positive deltas of equal magnitude bucketed differently: %v vs %v",
			neg.BucketCount, pos.BucketCount)
	}
}

func testDriftBucket_Percentiles(t *testing.T) {
	// 1..10 seconds: p50 interpolates between the 5th and 6th value.
	var ds []time.Duration
	for i := 1; i <= 10; i++ {
		ds = append(ds, time.Duration(i)*time.Second)
	}
	dist := newDriftDistribution(mkBucketTestDeltas(ds...))
	for _, tc := range []struct {
		p    int
		want time.Duration
	}{
		{50, 5500 * time.Millisecond},
		{100, 10 * time.Second},
	} {
		if got := dist.Percentiles[tc.p]; got != tc.want {
			t.Errorf("p%d = %v, want %v", tc.p, got, tc.want)
		}
	}
}

func testDriftBucket_DegenerateInputsDoNotPanic(t *testing.T) {
	if got := newDriftDistribution(nil); got.N != 0 {
		t.Errorf("empty input: N = %d, want 0", got.N)
	}
	if got := newDriftDistribution(nil).String(); !strings.Contains(got, "vacuous") {
		t.Errorf("empty distribution must say so, got %q", got)
	}
	single := newDriftDistribution(mkBucketTestDeltas(7 * time.Second))
	if single.Percentiles[50] != 7*time.Second || single.Percentiles[100] != 7*time.Second {
		t.Errorf("single-element percentiles = %v", single.Percentiles)
	}
}

// TestDriftSummary_FormatIsTheShellContract pins the exact string
// tools/replay-fixtures.sh parses out of the replay binary's stderr.
//
// Without this, the two sides are coupled by nothing: the sweep's sed would
// silently stop matching, its counter would stay 0, its reporting block would
// never print, and the sweep would exit 0 — which is precisely the dead-counter
// failure this PR is fixing one level up, reintroduced at the new layer. The
// regexp below is the one in replay-fixtures.sh; if this test is updated, that
// script must be updated in the same commit.
func TestDriftSummary_FormatIsTheShellContract(t *testing.T) {
	sweepRE := regexp.MustCompile(`timing ([0-9]+) pairs worst ([+-][0-9.]+)s@([0-9]+)`)

	t.Run("the sweep's regexp matches, with the right captures", func(t *testing.T) {
		got := driftSummary([]timeDelta{
			{Index: 0, Kind: "ready→working", Delta: -30976480 * time.Microsecond},
			{Index: 1, Kind: "working→ready", Delta: 12 * time.Millisecond},
		})
		m := sweepRE.FindStringSubmatch(got)
		if m == nil {
			t.Fatalf("replay-fixtures.sh would not parse %q — the sweep's timing report goes silent", got)
		}
		if m[1] != "2" || m[2] != "-30.976" || m[3] != "0" {
			t.Errorf("captures = pairs %q, worst %q, index %q; want \"2\", \"-30.976\", \"0\"", m[1], m[2], m[3])
		}
	})

	t.Run("a positive delta keeps its explicit + sign", func(t *testing.T) {
		// The sweep's character class requires a leading sign, so dropping
		// the %+ verb would make every late-firing recording unparseable
		// while every early-firing one still matched.
		got := driftSummary([]timeDelta{{Index: 3, Delta: 2004 * time.Millisecond}})
		if !sweepRE.MatchString(got) {
			t.Fatalf("positive delta unparseable by the sweep: %q", got)
		}
		if !strings.Contains(got, "+2.004") {
			t.Errorf("got %q, want a +2.004 figure", got)
		}
	})

	t.Run("no pairs reports n/a rather than a zero figure", func(t *testing.T) {
		// Dozens of the catalog's recordings produce no kind-matched pair at
		// all — the figure this comment used to carry by hand said 39 and was
		// left behind by #1478, which rescued three of them. The live count is
		// reported by TestSidecarReplayTransitionTimesMatchTheDaemonsOwnLog's
		// closing aggregate line ("... (N produced none)"), which is a
		// different test in this file, not this one (#1503). So this is the
		// common case, not an edge one. Reporting "worst +0.000s"
		// would make "nothing was measured" indistinguishable from "measured,
		// and perfect" — in the sweep's output and in the counter.
		got := driftSummary(nil)
		if sweepRE.MatchString(got) {
			t.Errorf("empty input produced a parseable timing figure %q — "+
				"unmeasured must not read as measured-and-zero", got)
		}
		if got != "timing n/a" {
			t.Errorf("got %q, want \"timing n/a\"", got)
		}
	})
}
