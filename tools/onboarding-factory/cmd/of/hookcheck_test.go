package main

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

// TestHookCheckDeclaresAndHasEvent pins the four-cell decision table
// promote-recording.sh's prompt (#1754) is built on: declares_hooks joins the
// daemon's adapter registry (hookcov.Declared, real code, not fixture data —
// codex and claudecode declare hooks, aider does not); has_hook_event reads
// the ONE staged events.jsonl this invocation names, via the same
// session-attribution logic hookcov.Coverage uses for the committed corpus
// (hookcov.HasOwnHookEvent), so a co-resident adapter's hook event cannot be
// mistaken for this adapter's own.
func TestHookCheckDeclaresAndHasEvent(t *testing.T) {
	root := t.TempDir()

	withHook := filepath.Join(root, "with-hook", "events.jsonl")
	write(t, withHook, `{"seq":1,"ts":"2026-05-01T00:00:00Z","kind":"transcript_new","session_id":"s","adapter":"codex"}`+"\n"+
		`{"seq":2,"ts":"2026-05-01T00:00:01Z","kind":"hook_received","session_id":"s","hook_name":"Stop"}`+"\n")

	noHook := filepath.Join(root, "no-hook", "events.jsonl")
	write(t, noHook, `{"seq":1,"ts":"2026-05-01T00:00:00Z","kind":"transcript_new","session_id":"s","adapter":"codex"}`+"\n")

	// hook_received present, but attributed to a DIFFERENT adapter's session —
	// must read as has_hook_event=false for codex, the same distinction
	// hookcov's own attribution guards against (#1768).
	othersHook := filepath.Join(root, "others-hook", "events.jsonl")
	write(t, othersHook, `{"seq":1,"ts":"2026-05-01T00:00:00Z","kind":"transcript_new","session_id":"s","adapter":"claudecode"}`+"\n"+
		`{"seq":2,"ts":"2026-05-01T00:00:01Z","kind":"hook_received","session_id":"s","hook_name":"Stop"}`+"\n")

	cases := []struct {
		name         string
		agent        string
		events       string
		wantDeclares bool
		wantHasHook  bool
	}{
		{"declares hooks, has one", "codex", withHook, true, true},
		{"declares hooks, has none — the GAP promote must ask about", "codex", noHook, true, false},
		{"declares hooks, hook belongs to a co-resident adapter", "codex", othersHook, true, false},
		{"declares no hooks, has none", "aider", noHook, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errs := runOf("hookcheck", "--agent", c.agent, "--events", c.events)
			if code != exitOK {
				t.Fatalf("exit=%d stderr=%s", code, errs)
			}
			var res hookCheckResult
			if err := json.Unmarshal([]byte(out), &res); err != nil {
				t.Fatalf("bad json: %v\n%s", err, out)
			}
			if res.Agent != c.agent {
				t.Errorf("agent: got %q want %q", res.Agent, c.agent)
			}
			if res.DeclaresHooks != c.wantDeclares {
				t.Errorf("declares_hooks: got %v want %v", res.DeclaresHooks, c.wantDeclares)
			}
			if res.HasHookEvent != c.wantHasHook {
				t.Errorf("has_hook_event: got %v want %v", res.HasHookEvent, c.wantHasHook)
			}
		})
	}
}

// TestHookCheckMissingEventsFile is the never-recorded / not-yet-flushed
// case: an unreadable or absent events.jsonl must read as has_hook_event
// false rather than erroring the whole check out — hasOwnHookEvent already
// treats a missing sidecar this way; this pins that `of hookcheck` inherits
// it rather than surfacing a bare Go error to the bash caller.
func TestHookCheckMissingEventsFile(t *testing.T) {
	code, out, errs := runOf("hookcheck", "--agent", "codex", "--events", "/nonexistent/events.jsonl")
	if code != exitOK {
		t.Fatalf("exit=%d stderr=%s", code, errs)
	}
	var res hookCheckResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	if res.HasHookEvent {
		t.Errorf("has_hook_event: got true for a missing file, want false")
	}
	if !res.DeclaresHooks {
		t.Errorf("declares_hooks: codex should still declare hooks regardless of the events file")
	}
}

// TestHookCheckRequiresFlags locks the usage error — a script that forgot a
// flag must get exitUsage, not a silent all-false answer that would read as
// "no problem here".
func TestHookCheckRequiresFlags(t *testing.T) {
	if code, _, _ := runOf("hookcheck", "--agent", "codex"); code != exitUsage {
		t.Errorf("missing --events: got exit %d, want %d", code, exitUsage)
	}
	if code, _, _ := runOf("hookcheck", "--events", "/tmp/x"); code != exitUsage {
		t.Errorf("missing --agent: got exit %d, want %d", code, exitUsage)
	}
}
