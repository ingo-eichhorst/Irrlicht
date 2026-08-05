package copilot

import (
	"testing"

	"irrlicht/core/pkg/tailer"
)

// realSession is the exact event sequence a live CLI 1.0.77 session produced
// for a one-turn prompt, with the payloads trimmed to the fields this adapter
// reads. Notably it carries NO session.usage_checkpoint — a short session
// emits none, and the usage block arrives only on session.shutdown.
const realSession = `{"type":"session.start","timestamp":"2026-08-02T17:20:00.000Z","data":{"sessionId":"f447df2a-06bf-441b-bc33-970693513198","copilotVersion":"1.0.77","producer":"cli","context":{"cwd":"/private/tmp/proj"}}}
{"type":"session.model_change","timestamp":"2026-08-02T17:20:00.100Z","data":{"newModel":"auto"}}
{"type":"session.auto_mode_resolved","timestamp":"2026-08-02T17:20:00.200Z","data":{"chosenModel":"gpt-5-mini","candidateModels":["gpt-5-mini","claude-haiku-4.5"]}}
{"type":"system.message","timestamp":"2026-08-02T17:20:00.300Z","data":{"role":"system","content":"you are copilot"}}
{"type":"user.message","timestamp":"2026-08-02T17:20:01.000Z","data":{"content":"reply with the single word: ok"}}
{"type":"assistant.turn_start","timestamp":"2026-08-02T17:20:01.100Z","data":{"turnId":"0"}}
{"type":"assistant.message","timestamp":"2026-08-02T17:20:08.000Z","data":{"messageId":"408dcf3d","model":"gpt-5-mini","content":"ok","toolRequests":[],"turnId":"0","outputTokens":156}}
{"type":"assistant.turn_end","timestamp":"2026-08-02T17:20:08.100Z","data":{"turnId":"0"}}
{"type":"session.shutdown","timestamp":"2026-08-02T17:20:09.000Z","data":{"shutdownType":"normal","totalNanoAiu":383150000,"totalPremiumRequests":0,"currentTokens":15144,"currentModel":"gpt-5-mini","tokenDetails":{"input":{"tokenCount":14078},"cache_read":{"tokenCount":0},"output":{"tokenCount":156}},"modelMetrics":{"gpt-5-mini":{"requests":{"count":1,"cost":0},"usage":{"inputTokens":14078,"outputTokens":156,"cacheReadTokens":0,"cacheWriteTokens":0,"reasoningTokens":128},"totalNanoAiu":383150000}}}}
`

// TestTokenAccounting_EndToEnd drives the real session through the PRODUCTION
// tailer and asserts Copilot's own per-model cumulative usage lands as the
// session's token totals. Cost itself is left to the shared capacity price map
// (the #1256 decision: general token pricing, no vendor billing math), so this
// pins the token half, which is price-independent.
func TestTokenAccounting_EndToEnd(t *testing.T) {
	path := writeTranscript(t, realSession)

	tl := tailer.NewTranscriptTailer(path, &Parser{}, AdapterName)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}

	if m.CumInputTokens != 14078 {
		t.Errorf("CumInputTokens = %d, want 14078", m.CumInputTokens)
	}
	if m.CumOutputTokens != 156 {
		t.Errorf("CumOutputTokens = %d, want 156", m.CumOutputTokens)
	}
	if m.ModelName != "gpt-5-mini" {
		t.Errorf("ModelName = %q, want %q", m.ModelName, "gpt-5-mini")
	}
	if m.AgentVersion != "1.0.77" {
		t.Errorf("AgentVersion = %q, want %q", m.AgentVersion, "1.0.77")
	}
	if m.LastCWD != "/private/tmp/proj" {
		t.Errorf("LastCWD = %q, want %q", m.LastCWD, "/private/tmp/proj")
	}
	// The turn ended, and shutdown must NOT have reopened it.
	if m.LastEventType != "turn_done" {
		t.Errorf("LastEventType = %q, want %q — shutdown/usage events must not register as activity",
			m.LastEventType, "turn_done")
	}
}

// TestUsageIsIdempotentAcrossRereads pins the high-water-mark contract: the
// tailer re-reads a transcript from byte 0 on a fresh scan, and Copilot's
// usage block is CUMULATIVE, so a naive implementation would double the
// session's tokens every pass.
func TestUsageIsIdempotentAcrossRereads(t *testing.T) {
	path := writeTranscript(t, realSession)

	p := &Parser{}
	tl := tailer.NewTranscriptTailer(path, p, AdapterName)
	if _, err := tl.TailAndProcess(); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}

	if m.CumInputTokens != 14078 {
		t.Errorf("CumInputTokens = %d after a second pass, want 14078 (cumulative usage double-counted)", m.CumInputTokens)
	}
}

// TestLiveSessionHasOutputTokensBeforeShutdown pins the live-cost path.
//
// This is a regression test for a bug found by watching a real running
// session rather than replaying a finished one: session.usage_checkpoint
// carries ONLY modelCacheState/totalNanoAiu/totalPremiumRequests — no
// modelMetrics, no tokenDetails, no currentTokens. Reading the cumulative
// block alone left an interactive session reporting no tokens and no cost for
// its entire lifetime, however long it ran.
//
// The transcript below is the real shape of a running session: two checkpoints
// fired, and there is no shutdown because the agent is still at the prompt.
func TestLiveSessionHasOutputTokensBeforeShutdown(t *testing.T) {
	lines := `{"type":"session.start","timestamp":"2026-08-02T20:38:00.000Z","data":{"sessionId":"51ffced3","copilotVersion":"1.0.77","context":{"cwd":"/private/tmp"}}}
{"type":"user.message","timestamp":"2026-08-02T20:38:01.000Z","data":{"content":"reply with exactly one word: pong"}}
{"type":"assistant.turn_start","timestamp":"2026-08-02T20:38:02.000Z","data":{"turnId":"0"}}
{"type":"assistant.message","timestamp":"2026-08-02T20:38:03.000Z","data":{"model":"claude-haiku-4.5","content":"pong","turnId":"0","outputTokens":46}}
{"type":"session.usage_checkpoint","timestamp":"2026-08-02T20:38:04.000Z","data":{"modelCacheState":{},"totalNanoAiu":120000000,"totalPremiumRequests":0}}
{"type":"assistant.message","timestamp":"2026-08-02T20:38:05.000Z","data":{"model":"claude-haiku-4.5","content":"done","turnId":"0","outputTokens":109}}
{"type":"assistant.turn_end","timestamp":"2026-08-02T20:38:06.000Z","data":{"turnId":"0"}}
`
	tl := tailer.NewTranscriptTailer(writeTranscript(t, lines), &Parser{}, AdapterName)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}

	if m.CumOutputTokens != 155 {
		t.Errorf("CumOutputTokens = %d, want 155 (46+109) — a running session must not "+
			"report zero tokens just because it has not shut down yet", m.CumOutputTokens)
	}
	if m.ModelName != "claude-haiku-4.5" {
		t.Errorf("ModelName = %q, want claude-haiku-4.5 (the auto-router switched mid-session)", m.ModelName)
	}
}

// TestShutdownDoesNotDoubleCountOutput pins the other half: once the session
// ends, the cumulative block must reconcile the INPUT tokens without
// re-counting output already contributed per assistant message.
func TestShutdownDoesNotDoubleCountOutput(t *testing.T) {
	lines := `{"type":"session.start","timestamp":"2026-08-02T20:38:00.000Z","data":{"context":{"cwd":"/tmp/p"}}}
{"type":"user.message","timestamp":"2026-08-02T20:38:01.000Z","data":{"content":"go"}}
{"type":"assistant.message","timestamp":"2026-08-02T20:38:03.000Z","data":{"model":"gpt-5-mini","content":"a","outputTokens":100}}
{"type":"assistant.message","timestamp":"2026-08-02T20:38:04.000Z","data":{"model":"gpt-5-mini","content":"b","outputTokens":56}}
{"type":"assistant.turn_end","timestamp":"2026-08-02T20:38:05.000Z","data":{"turnId":"0"}}
{"type":"session.shutdown","timestamp":"2026-08-02T20:38:09.000Z","data":{"currentTokens":15144,"currentModel":"gpt-5-mini","modelMetrics":{"gpt-5-mini":{"usage":{"inputTokens":14078,"outputTokens":156,"cacheReadTokens":0,"cacheWriteTokens":0}}}}}
`
	tl := tailer.NewTranscriptTailer(writeTranscript(t, lines), &Parser{}, AdapterName)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}

	if m.CumOutputTokens != 156 {
		t.Errorf("CumOutputTokens = %d, want 156 — the per-message increments (100+56) and the "+
			"shutdown cumulative (156) describe the SAME tokens and must not both be added", m.CumOutputTokens)
	}
	if m.CumInputTokens != 14078 {
		t.Errorf("CumInputTokens = %d, want 14078 — input has no live signal and must land at shutdown", m.CumInputTokens)
	}
}

// TestToolCallPairing asserts an open tool call is tracked and closed by its
// toolCallId, which is what HasOpenToolCall (and the stalled-tool fallback)
// rest on.
func TestToolCallPairing(t *testing.T) {
	open := `{"type":"session.start","timestamp":"2026-08-02T17:20:00.000Z","data":{"context":{"cwd":"/tmp/p"}}}
{"type":"user.message","timestamp":"2026-08-02T17:20:01.000Z","data":{"content":"run it"}}
{"type":"tool.execution_start","timestamp":"2026-08-02T17:20:02.000Z","data":{"toolCallId":"call_a","toolName":"bash"}}
`
	closed := open + `{"type":"tool.execution_complete","timestamp":"2026-08-02T17:20:03.000Z","data":{"toolCallId":"call_a","success":true}}` + "\n"

	for _, tc := range []struct {
		name     string
		lines    string
		wantOpen bool
	}{
		{"unclosed tool call stays open", open, true},
		{"completion closes it", closed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tl := tailer.NewTranscriptTailer(writeTranscript(t, tc.lines), &Parser{}, AdapterName)
			m, err := tl.TailAndProcess()
			if err != nil {
				t.Fatalf("TailAndProcess: %v", err)
			}
			if m.HasOpenToolCall != tc.wantOpen {
				t.Errorf("HasOpenToolCall = %v, want %v", m.HasOpenToolCall, tc.wantOpen)
			}
		})
	}
}

// TestOpenSubagents counts in-process children. Copilot's subagents write no
// session directory, so the parser's own paired set is the only source.
func TestOpenSubagents(t *testing.T) {
	started := `{"type":"subagent.started","timestamp":"2026-08-02T17:20:02.000Z","data":{"toolCallId":"call_s1","agentName":"task"}}` + "\n"
	done := `{"type":"subagent.completed","timestamp":"2026-08-02T17:20:05.000Z","data":{"toolCallId":"call_s1","agentName":"task"}}` + "\n"

	for _, tc := range []struct {
		name  string
		lines string
		want  int
	}{
		{"one running", started, 1},
		{"completed", started + done, 0},
		{"completion of an unknown id is a no-op", done, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Parser{}
			tl := tailer.NewTranscriptTailer(writeTranscript(t, tc.lines), p, AdapterName)
			m, err := tl.TailAndProcess()
			if err != nil {
				t.Fatalf("TailAndProcess: %v", err)
			}
			if got := p.OpenSubagents(m); got != tc.want {
				t.Errorf("OpenSubagents = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestMultiModelUsageAllContributes pins the queue: Copilot reports usage for
// every model a session touched, but ParsedEvent carries a single
// Contribution. Both models' tokens must reach the session total — a parser
// that emitted only one would silently under-report a session that switched
// models.
func TestMultiModelUsageAllContributes(t *testing.T) {
	lines := `{"type":"session.start","timestamp":"2026-08-02T17:20:00.000Z","data":{"context":{"cwd":"/tmp/p"}}}
{"type":"user.message","timestamp":"2026-08-02T17:20:01.000Z","data":{"content":"go"}}
{"type":"session.usage_checkpoint","timestamp":"2026-08-02T17:20:05.000Z","data":{"currentModel":"gpt-5-mini","modelMetrics":{"gpt-5-mini":{"usage":{"inputTokens":100,"outputTokens":10}},"claude-haiku-4.5":{"usage":{"inputTokens":200,"outputTokens":20}}}}}
{"type":"assistant.message","timestamp":"2026-08-02T17:20:06.000Z","data":{"model":"gpt-5-mini","content":"done"}}
{"type":"assistant.turn_end","timestamp":"2026-08-02T17:20:06.100Z","data":{"turnId":"0"}}
`
	tl := tailer.NewTranscriptTailer(writeTranscript(t, lines), &Parser{}, AdapterName)
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}

	if m.CumInputTokens != 300 {
		t.Errorf("CumInputTokens = %d, want 300 (100 + 200 across both models)", m.CumInputTokens)
	}
	if m.CumOutputTokens != 30 {
		t.Errorf("CumOutputTokens = %d, want 30 (10 + 20 across both models)", m.CumOutputTokens)
	}
}
