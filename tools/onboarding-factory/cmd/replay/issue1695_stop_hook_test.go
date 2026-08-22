package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"irrlicht/core/domain/session"
)

// Issue #1695: before this file, a `hook_received{Stop}` in a committed sidecar
// changed no byte of any golden. HookStop was absent from session's
// hookSignalEffects, so applyHookEvent returned at its first line — and even
// once a row was added the transition was still swallowed, by a stale per-pass
// NoSubstantiveActivity carried in from the last transcript batch. Both are
// fixed; what is easy to lose again is the KNOWLEDGE that the fix reaches
// anything, because the shape of losing it is silence: a catalog whose only
// gradeable Stop is retired, or a harness that stops reaching applyHookEvent,
// looks exactly like a catalog that is fine.
//
// So the population is measured every run rather than described. Two things
// come out of it:
//
//   - stopHookCensus, machine-printed and committed, the idiom #1503 and #1480
//     use. It moves when a Stop-bearing recording is added or retired, and the
//     failure names what moved.
//   - a hard floor: at least one Stop in the catalog must be REPRODUCED, and a
//     catalog carrying no Stop at all is a refusal rather than a pass.
//
// A Stop is counted as reproduced only when a replayed transition sits at the
// hook's OWN virtual time. That is exact rather than approximate: applyHookEvent
// stamps transitionCtx.virtTime with hookEv.Timestamp, so a transition at that
// instant came from that hook and from nothing else. Matching on `cause:
// "hook"` alone would not attribute it — 5 of the catalog's hook-bearing
// sidecars carry a PermissionRequest or a PreToolUse, which produce the same
// cause.

// stopRecording is one committed recording's Stop standing.
type stopRecording struct {
	// Stops is how many hook_received{Stop} events the sidecar carries, for
	// ANY session — including sessions the replay does not drive.
	Stops int
	// Reproduced is how many of them produced a replayed transition at the
	// hook's own virtual time.
	Reproduced int
	// Why records, for a recording where Reproduced < Stops, what the walk
	// could tell about the shortfall. It is prose, but it is DERIVED prose:
	// the walk writes it from what it observed, so it cannot describe a
	// recording that has changed underneath it.
	Why string
}

// stopHookCensus is every recording in replaydata/ whose sidecar carries a
// Stop hook, with how many of those Stops the replay reproduces.
//
// The two zero rows are the finding this file exists to keep visible, and they
// correct the claim #1695 was filed with ("affects both hook adapters equally —
// claudecode's Stop-bearing recordings have the same property"). Measured at
// #1695: replaydata/agents/claudecode/ carried NO Stop-bearing sidecar at all —
// its 15 hook-bearing recordings were PermissionRequest / PreToolUse /
// PostToolUse / PostToolUseFailure only. The catalog's two zero rows are
// co-resident claude-code sessions inside another adapter's multi-agent
// recording, and neither is gradeable for a reason that has nothing to do with
// Stop handling.
//
// #1699 closed the claudecode gap by RECORDING one, which is the only thing
// that could: those two zero rows carry a real, correctly-handled claudecode
// Stop and are ungradeable for reasons a harness change cannot reach. Note what
// the two Reproduced:1 rows do NOT have in common, because it is the reason the
// claudecode row is worth its own recording rather than a duplicate of codex's:
// in codex's, the DAEMON flipped 0.8ms after the POST, so replay and daemon
// agree; in claudecode's, the daemon's own flip landed 2.1s later at the next
// debounce boundary (still decided_by_tier "hook", sidecar seq 187) while replay
// flips at the hook's own timestamp. Same channel, opposite side of the debounce
// — see knownFirstTransitionDrift's neighbours in issue1480_timing_test.go.
//
// Regenerate by pasting the literal the test prints.
var stopHookCensus = map[string]stopRecording{
	"claudecode/scenarios/2-13_turn-end-terminal-text/recordings/2026-08-19-19-59-30_irrlichd-0.5.10+ae85182/transcript.jsonl": {
		Stops: 1, Reproduced: 1,
	},
	"codex/scenarios/2-13_turn-end-terminal-text/recordings/2026-08-18-00-42-27_irrlichd-0.5.10+1869727/transcript.jsonl": {
		Stops: 1, Reproduced: 1,
	},
	"copilot/scenarios/4-2_multiple-agents-same-workspace/recordings/2026-08-05-17-26-52_irrlichd-0.5.9+5b580ea/transcript.jsonl": {
		Stops: 1, Reproduced: 0,
		Why: "every Stop names a session the replay does not drive",
	},
	"hermes/scenarios/4-2_multiple-agents-same-workspace/recordings/2026-08-03-01-11-06_irrlichd-0.5.9+aef737b/transcript.jsonl": {
		Stops: 1, Reproduced: 0,
		Why: "the sidecar could not drive this replay: sidecar cannot drive a replay: no transcript_activity events with file_size for primary session 20260803_011105_d40f63",
	},
	"kiro-cli/scenarios/2-2_auto-executed-tool-call/recordings/2026-08-22-03-52-01_irrlichd-0.5.10+1f26512/transcript.jsonl": {
		Stops: 1, Reproduced: 1,
	},
	"mistral-vibe/scenarios/2-1_basic-turn/recordings/2026-08-22-03-50-34_irrlichd-0.5.10+763e5ca/transcript.jsonl": {
		Stops: 1, Reproduced: 0,
		Why: "the sidecar could not drive this replay: sidecar cannot drive a replay: no transcript_activity events with file_size for primary session session_20260822_015033_156d37d3",
	},
}

// sidecarHookEvent is the sliver of lifecycle.Event this walk reads. Decoded
// locally rather than through lifecycle.Event so a future field addition there
// cannot change what this measures.
type sidecarHookEvent struct {
	Kind      string    `json:"kind"`
	SessionID string    `json:"session_id"`
	HookName  string    `json:"hook_name"`
	TS        time.Time `json:"ts"`
}

func TestStopHookIsGradedByTheCommittedCatalog(t *testing.T) {
	root := replaydataRoot(t)
	measured := map[string]stopRecording{}

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Base(path) != eventsSidecarName {
			return nil
		}
		if name, rec, ok := measureStopsIn(t, root, path); ok {
			measured[name] = rec
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk replaydata/agents: %v", err)
	}

	// Fail loudly rather than pass quietly. An empty walk and a healthy one
	// must not produce the same output: with no Stop in the catalog every
	// assertion below is vacuously satisfied, which is precisely the state
	// #1695 was filed about (a Stop channel exercised at record time and
	// graded by nothing).
	if len(measured) == 0 {
		t.Fatal("no committed sidecar carries a hook_received{Stop} — this check cannot run, " +
			"and a Stop-handling regression is once again invisible to every golden (#1695). " +
			"Record one, or delete this file and say in the PR that the coverage went with it")
	}

	t.Logf("#1695 Stop census, measured over %d Stop-bearing recording(s):\n\n%s",
		len(measured), stopCensusLiteral(measured))

	assertSomeStopIsReproduced(t, measured)
	assertCensusMatches(t, measured)
}

// assertSomeStopIsReproduced is the floor: whatever the census says about
// individual recordings, at least one Stop in the catalog must produce a
// transition, or the channel is once again exercised at record time and graded
// by nothing.
func assertSomeStopIsReproduced(t *testing.T, measured map[string]stopRecording) {
	t.Helper()
	var total int
	for _, r := range measured {
		total += r.Reproduced
	}
	if total > 0 {
		return
	}
	t.Errorf("the catalog carries %d Stop-bearing recording(s) and the replay reproduces "+
		"NONE of their Stops. Either hookSignalEffects lost its HookStop row, or "+
		"applyHookEvent stopped reaching the classifier — both leave every golden "+
		"byte-identical and both are #1695 returning", len(measured))
}

// assertCensusMatches compares the measurement to the committed literal in BOTH
// directions. Both matter and they catch opposite things: an unlisted recording
// is a change in what this repo can grade arriving unannounced, and a listed one
// the walk no longer reaches is either a retirement or a walk that stopped
// pairing — the #1517 blindness, which reads as health from the inside.
func assertCensusMatches(t *testing.T, measured map[string]stopRecording) {
	t.Helper()
	for _, name := range sortedKeys(measured) {
		got, want := measured[name], stopHookCensus[name]
		if _, known := stopHookCensus[name]; !known {
			t.Errorf("%s carries %d Stop(s) and is not in stopHookCensus — a new Stop-bearing "+
				"recording is a change in what this repo can grade, so it lands in the census "+
				"deliberately rather than by nobody noticing", name, got.Stops)
			continue
		}
		if got.Stops != want.Stops || got.Reproduced != want.Reproduced {
			t.Errorf("%s: measured {Stops:%d Reproduced:%d}, census says {Stops:%d Reproduced:%d}",
				name, got.Stops, got.Reproduced, want.Stops, want.Reproduced)
		}
	}
	for _, name := range sortedKeys(stopHookCensus) {
		if _, ok := measured[name]; !ok {
			t.Errorf("stopHookCensus names %s, which the walk no longer reaches — a retired or "+
				"renamed recording, or a walk that stopped pairing", name)
		}
	}
}

// measureStopsIn grades one sidecar's Stops, returning the name it is filed
// under in the census and ok=false for a sidecar carrying no Stop at all.
//
// A recording that cannot be replayed is REPORTED with Why rather than skipped:
// "this sidecar has no gradeable Stop" and "this sidecar was never looked at"
// must not produce the same census row, which is the whole shape #1695 is about.
func measureStopsIn(t *testing.T, root, sidecar string) (string, stopRecording, bool) {
	t.Helper()
	stops := stopTimestamps(t, sidecar)
	if len(stops) == 0 {
		return "", stopRecording{}, false
	}
	transcript, paired := pairedTranscript(filepath.Dir(sidecar))
	if !paired {
		return rel(root, sidecar), stopRecording{
			Stops: len(stops), Why: "no transcript is paired with this sidecar",
		}, true
	}
	name := rel(root, transcript)

	tp, sp, useSidecar := resolveInputPaths(transcript)
	if !useSidecar {
		return name, stopRecording{
			Stops: len(stops),
			Why:   "the sidecar cannot drive a replay at all (transcript-only fallback)",
		}, true
	}
	report, err := runReplay(tp, sp, useSidecar, replaySettingsForTest(t, tp))
	if err != nil {
		t.Errorf("runReplay(%s): %v", name, err)
		return name, stopRecording{Stops: len(stops), Why: "runReplay failed"}, true
	}
	return name, gradeStops(stops, report), true
}

// stopTimestamps returns the ts of every hook_received{Stop} in a sidecar.
func stopTimestamps(t *testing.T, sidecar string) []time.Time {
	t.Helper()
	f, err := os.Open(sidecar)
	if err != nil {
		t.Fatalf("open sidecar %s: %v", sidecar, err)
	}
	defer f.Close()

	var out []time.Time
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev sidecarHookEvent
		if json.Unmarshal(line, &ev) != nil {
			continue // a sidecar line this walk cannot read carries no Stop it can attribute
		}
		if ev.Kind == "hook_received" && ev.HookName == session.HookStop {
			out = append(out, ev.TS)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan sidecar %s: %v", sidecar, err)
	}
	return out
}

// gradeStops counts how many of a recording's Stops produced a replayed
// transition at the hook's own virtual time.
func gradeStops(stops []time.Time, report *replayReport) stopRecording {
	at := map[int64]bool{}
	for _, tr := range report.Transitions {
		at[tr.VirtualTime.UnixNano()] = true
	}
	r := stopRecording{Stops: len(stops)}
	for _, ts := range stops {
		if at[ts.UnixNano()] {
			r.Reproduced++
		}
	}
	switch {
	case r.Reproduced == r.Stops:
	case report.SidecarFallback != "":
		// The report says so itself, so quote it rather than infer: a
		// transcript-only replay never reaches applyHookEvent at all, which is
		// a different shortfall from a Stop naming a foreign session.
		r.Why = "the sidecar could not drive this replay: " + report.SidecarFallback
	case r.Reproduced == 0:
		r.Why = "every Stop names a session the replay does not drive"
	default:
		r.Why = "some Stops name a session the replay does not drive"
	}
	return r
}

// stopCensusLiteral renders the measured census as the Go source to paste over
// stopHookCensus. Rendered from the measurement rather than reported in prose,
// because the transcription step is where a stale figure enters (#1503).
func stopCensusLiteral(m map[string]stopRecording) string {
	var b strings.Builder
	b.WriteString("var stopHookCensus = map[string]stopRecording{\n")
	for _, name := range sortedKeys(m) {
		r := m[name]
		fmt.Fprintf(&b, "\t%q: {\n\t\tStops: %d, Reproduced: %d,\n", name, r.Stops, r.Reproduced)
		if r.Why != "" {
			fmt.Fprintf(&b, "\t\tWhy: %q,\n", r.Why)
		}
		b.WriteString("\t},\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func sortedKeys(m map[string]stopRecording) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
