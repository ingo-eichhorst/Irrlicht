// Package userblocking pins the canonical user-blocking tool list that
// core/pkg/tailer and core/domain/session each hardcode independently:
// tailer.isUserBlockingToolName and session.isUserBlockingTool.
//
// The duplication is DELIBERATE and must not be "fixed" by extracting a
// shared production constant — the tailer keeps its copy local precisely to
// avoid importing the domain package (see the comment on
// isUserBlockingToolName in core/pkg/tailer/tailer_config.go). What the
// duplication does not defend against is drift: nothing makes the two lists
// fail when only one of them changes. This package is the shared table both
// sides' in-package tests probe, so a one-sided edit turns one of them red.
//
// Both predicates are unexported functions with inline switch/|| bodies —
// there is no enumerable var to compare — so agreement is asserted
// behaviorally, by probing each predicate with the same inputs rather than
// by diffing two lists.
//
// It lives in its own leaf package rather than in contracttesting itself
// because contracttesting imports core/domain/session; an in-package test in
// package session could not import that without an import cycle. It
// therefore imports nothing outside the standard library — keep it that way.
package userblocking

import "testing"

// Canonical is the exact set of tools that always block the agent until the
// user responds. Both predicates must return true for every entry.
//
// ask_user_question is vibe's snake_case spelling of the same concept as
// AskUserQuestion (#1087); exit_plan_mode/ask_user are GitHub Copilot's
// (#1256); request_user_input is muse's own dedicated ask-tool (issue #1960
// stage 3 — its system prompt tells the model to "Prefer `request_user_input`
// when available", verified via `strings` on the live muse-bin-1.2.1-R2847.1
// binary; see session.isUserBlockingTool's doc comment for the full
// evidence). This list was missing all four of these until #1960 — it had
// only the first three PascalCase/lowercase entries, so a tool using one of
// the missing spellings could be added to only one predicate's switch/||
// chain and this contract would never have noticed the other was untouched.
var Canonical = []string{
	"AskUserQuestion", "ExitPlanMode", "question",
	"ask_user_question", "exit_plan_mode", "ask_user", "request_user_input",
}

// NonBlocking are tools both predicates must reject. "Agent" and
// "SendMessage" are the load-bearing entries: tailer.surviveTurnDone (a
// different, overlapping list in the same file) does include them, so a
// predicate that accidentally consults the wrong list fails here. The rest
// guard against a predicate that returns true unconditionally, which would
// otherwise satisfy the Canonical half of this assertion vacuously.
var NonBlocking = []string{"Agent", "SendMessage", "Bash", "Read", "Edit", "Task", "AskUser", ""}

// AssertMatchesCanonical probes pred with the canonical and non-blocking
// tables. name identifies the predicate under test in failure output.
func AssertMatchesCanonical(t *testing.T, name string, pred func(string) bool) {
	t.Helper()
	for _, tool := range Canonical {
		if !pred(tool) {
			t.Errorf("%s(%q) = false, want true — the user-blocking list drifted; "+
				"the canonical list is %v and is duplicated in core/pkg/tailer/tailer_config.go "+
				"and core/domain/session/metrics.go, which must agree", name, tool, Canonical)
		}
	}
	for _, tool := range NonBlocking {
		if pred(tool) {
			t.Errorf("%s(%q) = true, want false — %q is not a user-blocking tool; "+
				"the canonical list is exactly %v", name, tool, tool, Canonical)
		}
	}
}
