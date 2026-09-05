package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
)

// Classifying a reconstructed run as top-level or subagent (#1905 subagents).
//
// The live daemon reads this off the session state's ParentSessionID. A
// back-fill has no session states, so it reads the daemon's own event log —
// and the patterns it reads with are DERIVED from the format strings the daemon
// logs with, never retyped.

// THE DERIVATION TRIPWIRE. The patterns are built from
// services.NewSessionInfoFormat / SubagentOrphanedInfoFormat /
// SubagentCompletedInfoFormat, so this feeds the REAL formats through
// fmt.Sprintf — exactly as the daemon does — and requires each pattern to match
// its own message and to capture the right field.
//
// Without it, a reworded daemon message would leave the tool matching nothing
// and classifying every child as top-level. That failure is silent by
// construction: fewer subagent runs and a machine that ran fewer subagents
// produce identical output.
func TestSubagentPatternsMatchTheDaemonsOwnMessages(t *testing.T) {
	t.Run("the birth line", func(t *testing.T) {
		msg := fmt.Sprintf(services.NewSessionInfoFormat, services.SubagentDirName, "claude-code")
		m := newSessionPattern.FindStringSubmatch(msg)
		if m == nil {
			t.Fatalf("newSessionPattern does not match the daemon's own message %q", msg)
		}
		if m[1] != services.SubagentDirName {
			t.Fatalf("captured project dir = %q, want %q", m[1], services.SubagentDirName)
		}
		if !isSubagentProjectDir(m[1]) {
			t.Fatalf("%q is not recognised as a subagent directory", m[1])
		}
		// …and a top-level session's birth line captures its own directory,
		// which is NOT a subagent directory.
		top := fmt.Sprintf(services.NewSessionInfoFormat, "-Users-ingo-projects-irrlicht", "claude-code")
		mt := newSessionPattern.FindStringSubmatch(top)
		if mt == nil {
			t.Fatalf("newSessionPattern does not match a top-level birth line %q", top)
		}
		if isSubagentProjectDir(mt[1]) {
			t.Fatalf("a project directory %q was read as a subagent directory", mt[1])
		}
	})

	for _, tc := range []struct {
		name    string
		format  string
		pattern func(string) []string
	}{
		{"the orphaned-subagent line", services.SubagentOrphanedInfoFormat,
			func(s string) []string { return orphanedPattern.FindStringSubmatch(s) }},
		{"the task-notification line", services.SubagentCompletedInfoFormat,
			func(s string) []string { return completedPattern.FindStringSubmatch(s) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			msg := fmt.Sprintf(tc.format, session.StateWorking, "parent-abc")
			m := tc.pattern(msg)
			if m == nil {
				t.Fatalf("the pattern does not match the daemon's own message %q", msg)
			}
			if m[2] != "parent-abc" {
				t.Fatalf("captured parent = %q, want %q (from %q)", m[2], "parent-abc", msg)
			}
		})
	}
}

// patternFromFormat escapes the literal halves rather than splicing them raw:
// a project directory holding a regexp metacharacter must not turn the pattern
// into a different one.
func TestPatternFromFormat_EscapesLiterals(t *testing.T) {
	re := patternFromFormat("a (b) c %s d.e", `(\S+)`)
	if m := re.FindStringSubmatch("a (b) c VALUE d.e"); m == nil || m[1] != "VALUE" {
		t.Fatalf("pattern did not match its own format's output: %v", m)
	}
	// `.` must be a literal dot, not "any character".
	if re.MatchString("a (b) c VALUE dXe") {
		t.Fatal("the pattern treated a literal `.` as a wildcard")
	}
}

// THE THIRD STATE. A session the retained log never saw start is UNKNOWN, not
// top-level. Every cost-era run is in this bucket — the cost log reaches back
// months further than the event log — and calling them top-level would put
// months of runs into the default view under a claim nothing measured.
func TestSubagentIndex_ClassifiesThreeWays(t *testing.T) {
	ix := newSubagentIndex()
	ix.observe("child-with-parent", fmt.Sprintf(services.SubagentCompletedInfoFormat, session.StateWorking, "parent-1"))
	ix.observe("child-by-dir", fmt.Sprintf(services.NewSessionInfoFormat, services.SubagentDirName, "claude-code"))
	ix.observe("top", fmt.Sprintf(services.NewSessionInfoFormat, "-Users-ingo-projects-irrlicht", "claude-code"))

	cases := []struct {
		session    string
		wantKind   string
		wantParent string
	}{
		{"child-with-parent", session.AutonomyKindSubagent, "parent-1"},
		// A child the log never named a parent for is STILL a subagent run: the
		// exclusion turns on being a child, not on which parent.
		{"child-by-dir", session.AutonomyKindSubagent, ""},
		{"top", session.AutonomyKindTopLevel, ""},
		{"never-seen", session.AutonomyKindUnknown, ""},
	}
	for _, tc := range cases {
		t.Run(tc.session, func(t *testing.T) {
			kind, parent := ix.classify(tc.session)
			if kind != tc.wantKind {
				t.Fatalf("kind = %q, want %q", kind, tc.wantKind)
			}
			if parent != tc.wantParent {
				t.Fatalf("parent = %q, want %q", parent, tc.wantParent)
			}
		})
	}
}

// A birth line under a subagent directory wins over the absence of a
// parent-naming message, and a later parent-naming message fills the parent in.
// Order must not matter: the log interleaves both shapes across rotated files.
func TestSubagentIndex_MarkersCombineInEitherOrder(t *testing.T) {
	for _, reversed := range []bool{false, true} {
		birth := fmt.Sprintf(services.NewSessionInfoFormat, services.SubagentDirName, "claude-code")
		done := fmt.Sprintf(services.SubagentOrphanedInfoFormat, session.StateWorking, "parent-9")
		ix := newSubagentIndex()
		if reversed {
			ix.observe("c", done)
			ix.observe("c", birth)
		} else {
			ix.observe("c", birth)
			ix.observe("c", done)
		}
		kind, parent := ix.classify("c")
		if kind != session.AutonomyKindSubagent || parent != "parent-9" {
			t.Fatalf("reversed=%v: kind=%q parent=%q, want sub/parent-9", reversed, kind, parent)
		}
	}
}

// The index is built on the SAME pass that parses transitions, off real log
// lines — so a wiring change that stopped feeding it shows up here rather than
// as a quietly all-unknown reconstruction.
func TestReadEventLog_BuildsTheSubagentIndex(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	line := func(ts, sid, msg string) string {
		return fmt.Sprintf(`{"timestamp":%q,"event_type":%q,"session_id":%q,"message":%q}`,
			ts, detectorEventType, sid, msg)
	}
	body := line("2026-08-22T14:06:00Z", "kid",
		fmt.Sprintf(services.NewSessionInfoFormat, services.SubagentDirName, "claude-code")) + "\n" +
		line("2026-08-22T14:06:01Z", "boss",
			fmt.Sprintf(services.NewSessionInfoFormat, "-Users-ingo-projects-irrlicht", "claude-code")) + "\n" +
		line("2026-08-22T14:07:00Z", "kid", "transcript activity (ready → working)") + "\n" +
		line("2026-08-22T14:08:00Z", "kid",
			fmt.Sprintf(services.SubagentOrphanedInfoFormat, session.StateWorking, "boss")) + "\n"
	if err := os.WriteFile(filepath.Join(logDir, "events.log"), []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	log, err := readEventLog(dir)
	if err != nil {
		t.Fatalf("readEventLog: %v", err)
	}
	if log.Subagents == nil {
		t.Fatal("readEventLog built no subagent index — every run would reconstruct as unknown")
	}
	if kind, parent := log.Subagents.classify("kid"); kind != session.AutonomyKindSubagent || parent != "boss" {
		t.Fatalf("kid classified as %q/%q, want sub/boss", kind, parent)
	}
	if kind, _ := log.Subagents.classify("boss"); kind != session.AutonomyKindTopLevel {
		t.Fatalf("boss classified as %q, want %q", kind, session.AutonomyKindTopLevel)
	}
	// The parentage lines did not cost the transition parse anything: the
	// child's ready→working and its completion are both still transitions.
	if len(log.Transitions) != 2 {
		t.Fatalf("parsed %d transitions, want 2: %+v", len(log.Transitions), log.Transitions)
	}
	// Each line is counted ONCE however many questions it answered — the
	// completion line is both a parentage marker and a transition.
	if log.Stats.Relevant != 4 {
		t.Fatalf("Relevant = %d, want 4 (one per used line, never twice for one line)", log.Stats.Relevant)
	}
}
