// hooktranscript_test.go pins the one way installing hooks changes what the
// adapter READS, as opposed to what it receives.
//
// Copilot logs its own hook execution into the session transcript: every
// delivery appends a hook.start / hook.end pair. So the moment a user grants
// the hooks permission, the file the tailer is reading grows a class of line
// that no existing recording contains. That is exactly the shape of change
// that silently moves state detection, and it is invisible to the replay
// goldens — every frozen copilot recording predates hook installation, so a
// byte-identical replay proves the parser is unchanged, NOT that it handles
// these lines.
//
// The lines below are verbatim from a real 1.0.78 session driven with this
// adapter's own hook file installed.
package copilot

import (
	"encoding/json"
	"testing"
)

const (
	realHookStartLine = `{"id":"h1","parentId":null,"timestamp":"2026-08-09T22:57:36.167Z","type":"hook.start","data":{"hookInvocationId":"ae08178b-e3be-426f-b007-61a8763b651c","hookType":"agentStop","input":{"transcriptPath":"/tmp/x/events.jsonl","stopReason":"end_turn","stop_hook_active":false,"sessionId":"309e2571-49c6-4344-bb26-2f1d019e72e3"}}}`
	realHookEndLine   = `{"id":"h2","parentId":"h1","timestamp":"2026-08-09T22:57:36.192Z","type":"hook.end","data":{"hookInvocationId":"ae08178b-e3be-426f-b007-61a8763b651c","hookType":"agentStop","success":true}}`
)

// TestHookLifecycleLinesAreInert pins that Copilot's own hook.start/hook.end
// bookkeeping is skipped rather than mistaken for agent activity. A hook.start
// treated as activity would flip a finished session back to working on every
// turn end — and it would do so ONLY for users who granted the permission,
// which is the hardest kind of bug to reproduce.
func TestHookLifecycleLinesAreInert(t *testing.T) {
	for _, tc := range []struct{ name, line string }{
		{"hook.start", realHookStartLine},
		{"hook.end", realHookEndLine},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var raw map[string]any
			if err := json.Unmarshal([]byte(tc.line), &raw); err != nil {
				t.Fatalf("fixture does not parse: %v", err)
			}
			p := &Parser{}
			ev := p.ParseLine(raw)
			if ev == nil {
				t.Fatal("ParseLine returned nil")
			}
			if !ev.Skip {
				t.Errorf("%s was not skipped (EventType=%q) — Copilot writes this line for "+
					"every hook delivery, so treating it as activity would keep a session "+
					"working forever once hooks are granted", tc.name, ev.EventType)
			}
		})
	}
}
