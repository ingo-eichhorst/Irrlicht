package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/pkg/tailer"
)

// --- Parser-level: Bash run_in_background signal extraction (issue #445) ---

// bashToolUse builds an assistant event carrying one Bash-family tool_use.
func bashToolUse(id, name string, input map[string]interface{}) map[string]interface{} {
	return map[string]interface{}{
		"type": "assistant",
		"message": map[string]interface{}{
			"stop_reason": "tool_use",
			"content": []interface{}{
				map[string]interface{}{"type": "tool_use", "id": id, "name": name, "input": input},
			},
		},
	}
}

// toolResult builds a user event carrying one tool_result with string content.
func toolResult(toolUseID, content string) map[string]interface{} {
	return map[string]interface{}{
		"type": "user",
		"message": map[string]interface{}{
			"content": []interface{}{
				map[string]interface{}{"type": "tool_result", "tool_use_id": toolUseID, "content": content},
			},
		},
	}
}

// bgSpawnResult builds the user event Claude Code writes for a Bash
// run_in_background launch: a tool_result with the launch text plus the
// authoritative top-level toolUseResult.backgroundTaskId that gates spawn
// detection.
func bgSpawnResult(toolUseID, bashID, content string) map[string]interface{} {
	ev := toolResult(toolUseID, content)
	ev["toolUseResult"] = map[string]interface{}{"backgroundTaskId": bashID}
	return ev
}

func TestParser_BackgroundSpawn_FromResultText(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(bgSpawnResult("toolu_1", "bc1h56v8v",
		"Command running in background with ID: bc1h56v8v. Output is being written to: /private/tmp/claude-501/x/tasks/bc1h56v8v.output"))
	if len(ev.BackgroundSpawns) != 1 {
		t.Fatalf("BackgroundSpawns = %d, want 1", len(ev.BackgroundSpawns))
	}
	sp := ev.BackgroundSpawns[0]
	if sp.BashID != "bc1h56v8v" {
		t.Errorf("BashID = %q, want bc1h56v8v", sp.BashID)
	}
	if sp.OutputPath != "/private/tmp/claude-501/x/tasks/bc1h56v8v.output" {
		t.Errorf("OutputPath = %q", sp.OutputPath)
	}
}

func TestParser_NoPhantomSpawnFromArbitraryText(t *testing.T) {
	p := &Parser{}
	// The same launch phrase, but with NO structured toolUseResult.backgroundTaskId
	// (e.g. a Read/Grep over a log that echoes it). Must not fabricate a process.
	ev := p.ParseLine(toolResult("toolu_x",
		"Command running in background with ID: bc1h56v8v. Output is being written to: /tmp/x/tasks/bc1h56v8v.output"))
	if len(ev.BackgroundSpawns) != 0 {
		t.Errorf("phantom spawn from un-gated text: %+v", ev.BackgroundSpawns)
	}
}

func TestParser_BashOutputPoll_AndTerminatedStatus(t *testing.T) {
	p := &Parser{}

	poll := p.ParseLine(bashToolUse("toolu_poll", "BashOutput", map[string]interface{}{"bash_id": "bc1h56v8v"}))
	if len(poll.BashOutputPolls) != 1 || poll.BashOutputPolls[0].BashID != "bc1h56v8v" ||
		poll.BashOutputPolls[0].ToolUseID != "toolu_poll" {
		t.Fatalf("BashOutputPolls = %+v, want one {toolu_poll, bc1h56v8v}", poll.BashOutputPolls)
	}

	// running → not terminated
	running := p.ParseLine(toolResult("toolu_poll", "<status>running</status>\npartial output"))
	if len(running.TerminatedBashOutputIDs) != 0 {
		t.Errorf("running status should not be terminated, got %v", running.TerminatedBashOutputIDs)
	}

	// completed → terminated, attributed to the poll's tool_use id
	done := p.ParseLine(toolResult("toolu_poll", "<status>completed</status>\nfinal output"))
	if len(done.TerminatedBashOutputIDs) != 1 || done.TerminatedBashOutputIDs[0] != "toolu_poll" {
		t.Errorf("TerminatedBashOutputIDs = %v, want [toolu_poll]", done.TerminatedBashOutputIDs)
	}
}

func TestParser_KillShell(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(bashToolUse("toolu_kill", "KillShell", map[string]interface{}{"shell_id": "bc1h56v8v"}))
	if len(ev.KilledShellIDs) != 1 || ev.KilledShellIDs[0] != "bc1h56v8v" {
		t.Fatalf("KilledShellIDs = %v, want [bc1h56v8v]", ev.KilledShellIDs)
	}
}

// --- Parser + tailer end-to-end: open-background-process accounting ---

func writeBgTranscript(t *testing.T, lines []map[string]interface{}) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create transcript: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, ln := range lines {
		if err := enc.Encode(ln); err != nil {
			t.Fatalf("encode line: %v", err)
		}
	}
	return path
}

func TestTailer_BackgroundProcessCount_SpawnAndTerminate(t *testing.T) {
	spawnResult := "Command running in background with ID: bc1h56v8v. Output is being written to: /tmp/x/tasks/bc1h56v8v.output"

	t.Run("alive while only spawned", func(t *testing.T) {
		path := writeBgTranscript(t, []map[string]interface{}{
			bashToolUse("toolu_1", "Bash", map[string]interface{}{"command": "sleep 100", "run_in_background": true}),
			bgSpawnResult("toolu_1", "bc1h56v8v", spawnResult),
		})
		tl := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
		m, err := tl.TailAndProcess()
		if err != nil {
			t.Fatalf("TailAndProcess: %v", err)
		}
		if m.BackgroundProcessCount != 1 {
			t.Fatalf("BackgroundProcessCount = %d, want 1", m.BackgroundProcessCount)
		}
		if len(m.BackgroundProcessOutputs) != 1 || m.BackgroundProcessOutputs[0] != "/tmp/x/tasks/bc1h56v8v.output" {
			t.Errorf("BackgroundProcessOutputs = %v", m.BackgroundProcessOutputs)
		}
	})

	t.Run("cleared after observed termination", func(t *testing.T) {
		path := writeBgTranscript(t, []map[string]interface{}{
			bashToolUse("toolu_1", "Bash", map[string]interface{}{"command": "sleep 100", "run_in_background": true}),
			bgSpawnResult("toolu_1", "bc1h56v8v", spawnResult),
			bashToolUse("toolu_poll", "BashOutput", map[string]interface{}{"bash_id": "bc1h56v8v"}),
			toolResult("toolu_poll", "<status>completed</status>\nfinal output"),
		})
		tl := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
		m, err := tl.TailAndProcess()
		if err != nil {
			t.Fatalf("TailAndProcess: %v", err)
		}
		if m.BackgroundProcessCount != 0 {
			t.Fatalf("BackgroundProcessCount = %d, want 0 after termination", m.BackgroundProcessCount)
		}
		if len(m.BackgroundProcessOutputs) != 0 {
			t.Errorf("BackgroundProcessOutputs = %v, want empty", m.BackgroundProcessOutputs)
		}
	})

	t.Run("open set survives a ledger round-trip (daemon restart)", func(t *testing.T) {
		path := writeBgTranscript(t, []map[string]interface{}{
			bashToolUse("toolu_1", "Bash", map[string]interface{}{"command": "sleep 100", "run_in_background": true}),
			bgSpawnResult("toolu_1", "bc1h56v8v", spawnResult),
		})
		tl1 := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
		if _, err := tl1.TailAndProcess(); err != nil {
			t.Fatalf("first pass: %v", err)
		}
		ledger := tl1.GetLedgerState()
		if len(ledger.BackgroundProcs) != 1 {
			t.Fatalf("ledger BackgroundProcs = %v, want one entry", ledger.BackgroundProcs)
		}

		// Restart: a fresh tailer rehydrated from the ledger reports the open
		// background process even though it reads no new transcript lines.
		tl2 := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
		tl2.SetLedgerState(ledger)
		m, err := tl2.TailAndProcess()
		if err != nil {
			t.Fatalf("post-restart pass: %v", err)
		}
		if m.BackgroundProcessCount != 1 {
			t.Errorf("BackgroundProcessCount after restart = %d, want 1", m.BackgroundProcessCount)
		}
		if len(m.BackgroundProcessOutputs) != 1 {
			t.Errorf("BackgroundProcessOutputs after restart = %v, want one path", m.BackgroundProcessOutputs)
		}
	})

	t.Run("cleared after KillShell", func(t *testing.T) {
		path := writeBgTranscript(t, []map[string]interface{}{
			bashToolUse("toolu_1", "Bash", map[string]interface{}{"command": "sleep 100", "run_in_background": true}),
			bgSpawnResult("toolu_1", "bc1h56v8v", spawnResult),
			bashToolUse("toolu_kill", "KillShell", map[string]interface{}{"shell_id": "bc1h56v8v"}),
		})
		tl := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
		m, err := tl.TailAndProcess()
		if err != nil {
			t.Fatalf("TailAndProcess: %v", err)
		}
		if m.BackgroundProcessCount != 0 {
			t.Fatalf("BackgroundProcessCount = %d, want 0 after KillShell", m.BackgroundProcessCount)
		}
	})
}
