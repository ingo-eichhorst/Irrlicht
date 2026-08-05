package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/domain/lifecycle"
)

// TestResolveInputPaths_PairsSiblingEventsJSONL is the unit-level half of
// issue #1326: every sidecar in replaydata/ is named plain "events.jsonl"
// beside its transcript, but resolveInputPaths only ever probed the legacy
// "<transcript-stem>.events.jsonl" spelling. os.Stat on that name always
// failed, so useSidecar stayed false for the entire catalog and the sidecar
// replay path — applyHookEvent included — was unreachable from both fixture
// drivers.
func TestResolveInputPaths_PairsSiblingEventsJSONL(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "transcript.jsonl")
	sidecar := filepath.Join(dir, eventsSidecarName)
	writeFile(t, transcript, "{}\n")
	writeFile(t, sidecar, "{}\n")

	gotTranscript, gotSidecar, useSidecar := resolveInputPaths(transcript)
	if !useSidecar {
		t.Fatalf("resolveInputPaths(%q): useSidecar = false, want true (sibling events.jsonl present)", transcript)
	}
	if gotTranscript != transcript {
		t.Errorf("transcript path: got %q, want %q", gotTranscript, transcript)
	}
	if gotSidecar != sidecar {
		t.Errorf("sidecar path: got %q, want %q", gotSidecar, sidecar)
	}
}

// TestResolveInputPaths_LegacySpellingWins pins the precedence: when both the
// legacy per-transcript sidecar and a plain sibling exist, the legacy name is
// the more specific match and must still be chosen. Lock — passes before the
// #1326 fix by construction.
func TestResolveInputPaths_LegacySpellingWins(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "transcript.jsonl")
	legacy := filepath.Join(dir, "transcript.events.jsonl")
	sibling := filepath.Join(dir, eventsSidecarName)
	writeFile(t, transcript, "{}\n")
	writeFile(t, legacy, "{}\n")
	writeFile(t, sibling, "{}\n")

	_, gotSidecar, useSidecar := resolveInputPaths(transcript)
	if !useSidecar {
		t.Fatal("useSidecar = false, want true")
	}
	if gotSidecar != legacy {
		t.Errorf("sidecar path: got %q, want the legacy spelling %q", gotSidecar, legacy)
	}
}

// TestResolveInputPaths_SidecarArgumentIsNotItsOwnTranscript guards the one
// way the sibling probe could feed a sidecar to itself: the fixture drivers
// skip events.jsonl, but the CLI accepts any positional path. A bare
// "events.jsonl" argument must not resolve to (events.jsonl, events.jsonl).
//
// The non-canonical spellings matter and are not hypothetical: t.TempDir()
// hands back an already-cleaned path, so a version of this test that only used
// the canonical form passed while `./events.jsonl` still self-paired —
// filepath.Join cleans its result, so comparing whole paths never matched.
// Asserting useSidecar==false rather than just transcript!=sidecar is the
// other half: the `./` form satisfies string inequality while still pairing
// the file with itself.
func TestResolveInputPaths_SidecarArgumentIsNotItsOwnTranscript(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, eventsSidecarName), "{}\n")

	for _, src := range []string{
		filepath.Join(dir, eventsSidecarName),
		dir + "/./" + eventsSidecarName,
		dir + "//" + eventsSidecarName,
	} {
		gotTranscript, gotSidecar, useSidecar := resolveInputPaths(src)
		if useSidecar {
			t.Errorf("resolveInputPaths(%q) paired the sidecar with itself: transcript=%q sidecar=%q useSidecar=true",
				src, gotTranscript, gotSidecar)
		}
	}
}

// TestResolveInputPaths_RelativeTranscriptStillPairs guards the flip side of
// the basename check: a relative or bare-filename transcript must still find
// its sibling. filepath.Dir("transcript.jsonl") is ".", so the probe becomes
// "events.jsonl" — correct, but only if nothing rejects the empty directory.
func TestResolveInputPaths_RelativeTranscriptStillPairs(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "transcript.jsonl"), "{}\n")
	writeFile(t, filepath.Join(dir, eventsSidecarName), "{}\n")

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	if _, gotSidecar, useSidecar := resolveInputPaths("transcript.jsonl"); !useSidecar || gotSidecar != "events.jsonl" {
		t.Errorf("bare relative transcript: got (sidecar=%q, useSidecar=%v), want (%q, true)",
			gotSidecar, useSidecar, "events.jsonl")
	}
}

// TestFixtureReplay_HookReachesApplyHookEvent is the fixture-grounded half of
// issue #1326 and the coverage the harness lacked entirely: a committed
// recording that carries hook_received events must, when replayed through the
// same resolveInputPaths → runReplay path main() and the byte-identity golden
// use, actually enter applyHookEvent — observable as a transition whose cause
// is "hook".
//
// Before the fix this recording replayed transcript-only and produced causes
// {init, debounce_coalesce} with no hook anywhere, which is what let #1320's
// drift (replay silently ignoring PreToolUse, a hook the daemon holds
// SignalPermissionPrompt on) sit undetected behind a green suite.
func TestFixtureReplay_HookReachesApplyHookEvent(t *testing.T) {
	transcript := fixturePath(t, "claudecode/2-18_user-blocking-plan-mode-approval/transcript.jsonl")
	sidecar := filepath.Join(filepath.Dir(transcript), eventsSidecarName)
	if !sidecarHasHooks(t, sidecar) {
		t.Fatalf("%s carries no hook_received events — the fixture no longer exercises the hook path", sidecar)
	}

	resolvedTranscript, resolvedSidecar, useSidecar := resolveInputPaths(transcript)
	if !useSidecar {
		t.Fatalf("resolveInputPaths did not pair %s with its sibling events.jsonl", transcript)
	}
	adapter, err := detectAdapter(resolvedTranscript)
	if err != nil {
		t.Fatalf("detectAdapter: %v", err)
	}
	report, err := runReplay(resolvedTranscript, resolvedSidecar, useSidecar, reportSettings{
		Adapter:            adapter,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("runReplay: %v", err)
	}

	var causes []string
	hooked := false
	for _, tr := range report.Transitions {
		causes = append(causes, string(tr.Cause))
		if tr.Cause == causeHook {
			hooked = true
		}
	}
	if !hooked {
		t.Fatalf("no transition with cause %q — applyHookEvent was never reached over a hook-carrying recording; causes were %v",
			causeHook, causes)
	}
}

// TestEveryHookCarryingRecordingReplaysWithSidecar is the catalog-wide lock
// that keeps the gap from reopening one recording at a time. Every recording
// in replaydata/ whose events.jsonl contains a hook_received record must
// resolve to sidecar mode; a future layout change that breaks the pairing
// fails here rather than silently degrading the whole harness to
// transcript-only again.
func TestEveryHookCarryingRecordingReplaysWithSidecar(t *testing.T) {
	root := mustAbs(t, filepath.Join("..", "..", "..", "..", "replaydata", "agents"))
	var checked int
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Base(path) != eventsSidecarName {
			return nil
		}
		if assertSidecarPairsWithTranscript(t, path) {
			checked++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk replaydata: %v", err)
	}
	if checked == 0 {
		t.Fatal("no hook-carrying recording found under replaydata/agents — the lock is vacuous")
	}
	t.Logf("checked %d hook-carrying recordings", checked)
}

// TestFixtureReplay_StoreAdapterFallsBackToTranscriptOnly covers the other
// half of pairing the whole catalog with its sidecars: a process-owned-store
// adapter records no fswatcher fires, so its sidecar cannot drive a replay at
// all. Before the fallback, pairing these recordings turned 56 previously
// green fixtures into hard "sidecar has no transcript_activity events" errors.
//
// The report must come back transcript-only and say so — an empty
// SidecarFallback here would mean the degradation went unrecorded, which is
// the failure mode #1326 is about.
func TestFixtureReplay_StoreAdapterFallsBackToTranscriptOnly(t *testing.T) {
	transcript := fixturePath(t, "hermes/5-2_model-identification/transcript.jsonl")
	resolvedTranscript, resolvedSidecar, useSidecar := resolveInputPaths(transcript)
	if !useSidecar {
		t.Fatalf("resolveInputPaths did not pair %s with its sibling events.jsonl", transcript)
	}
	report, err := runReplay(resolvedTranscript, resolvedSidecar, useSidecar, reportSettings{
		Adapter:            "hermes",
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err != nil {
		t.Fatalf("runReplay: %v", err)
	}
	if report.SidecarFallback == "" {
		t.Error("SidecarFallback is empty — the transcript-only degradation was not recorded")
	}
	if strings.Contains(report.SidecarFallback, "/") {
		t.Errorf("SidecarFallback %q contains a path — goldens would differ between clones", report.SidecarFallback)
	}
	if report.ExtendedCheck != nil {
		t.Error("ExtendedCheck is populated on a transcript-only fallback report")
	}
}

// TestReplayWithSidecar_CorruptSidecarIsFatal pins the boundary of the
// not-drivable fallback. A corrupt sidecar reaches the same "no transcript_new
// event names a real session" condition as a benign aider recording, because
// loadAllLifecycleEvents skips unparseable lines — so without the malformed
// count the two are indistinguishable and a garbage file degrades quietly to
// transcript-only. For a newly added recording, whose golden is generated
// fresh, that would be committed as though it were the benign case.
func TestReplayWithSidecar_CorruptSidecarIsFatal(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "transcript.jsonl")
	sidecar := filepath.Join(dir, eventsSidecarName)
	writeFile(t, transcript, "{}\n")
	writeFile(t, sidecar, "NOT JSON AT ALL\n")

	_, err := replayWithSidecar(transcript, sidecar, reportSettings{
		Adapter:            claudecode.AdapterName,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	})
	if err == nil {
		t.Fatal("corrupt sidecar replayed without error")
	}
	var notDrivable *notDrivableError
	if errors.As(err, &notDrivable) {
		t.Errorf("corrupt sidecar returned the benign not-drivable fallback (%v) — it must be fatal", err)
	}
	if !strings.Contains(err.Error(), "malformed") {
		t.Errorf("error %q does not mention the malformed lines", err)
	}
}

// TestReplayWithSidecar_BlankLinesAreNotMalformed is a lock: a trailing
// newline is normal in a JSONL file and must not be counted as corruption,
// or every well-formed sidecar ending in "\n" would become fatal.
func TestReplayWithSidecar_BlankLinesAreNotMalformed(t *testing.T) {
	dir := t.TempDir()
	sidecar := filepath.Join(dir, eventsSidecarName)
	writeFile(t, sidecar, "{\"seq\":1,\"kind\":\"transcript_new\",\"session_id\":\"s1\"}\n\n")

	if _, malformed, err := loadLifecycleEventsCountingMalformed(sidecar); err != nil || malformed != 0 {
		t.Errorf("got (malformed=%d, err=%v), want (0, nil)", malformed, err)
	}
}

// assertSidecarPairsWithTranscript checks one sidecar, reporting whether it was
// in scope for the lock: it must carry a hook_received record and sit beside a
// transcript.jsonl. A hook-carrying sidecar next to a transcript.md (aider) or
// next to no transcript at all is skipped and reported as not checked, so the
// caller's "the lock is vacuous" guard stays honest.
func assertSidecarPairsWithTranscript(t *testing.T, sidecar string) bool {
	t.Helper()
	if !sidecarHasHooks(t, sidecar) {
		return false
	}
	transcript := filepath.Join(filepath.Dir(sidecar), "transcript.jsonl")
	if _, err := os.Stat(transcript); err != nil {
		return false
	}
	if _, gotSidecar, useSidecar := resolveInputPaths(transcript); !useSidecar || gotSidecar != sidecar {
		t.Errorf("%s: resolveInputPaths gave (sidecar=%q, useSidecar=%v), want (%q, true)",
			transcript, gotSidecar, useSidecar, sidecar)
	}
	return true
}

// sidecarHasHooks reports whether the sidecar carries a hook_received record,
// read through the same loader the code under test uses. That matters more
// than it looks: a hand-rolled scan can disagree with loadAllLifecycleEvents
// about line-length caps or whitespace, and the catalog-wide lock would then be
// measuring a different set of records than the one replay actually sees.
func sidecarHasHooks(t *testing.T, sidecar string) bool {
	t.Helper()
	events, err := loadAllLifecycleEvents(sidecar)
	if err != nil {
		t.Fatalf("read sidecar %s: %v", sidecar, err)
	}
	for _, ev := range events {
		if ev.Kind == lifecycle.KindHookReceived {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
