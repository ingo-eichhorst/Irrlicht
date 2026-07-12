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
	// Claude Code's real launch message is a full sentence: the path is
	// followed by a period and more prose ("…output. You will be notified…").
	// The captured path must NOT absorb that trailing period — otherwise the
	// daemon's lsof liveness probe dereferences a non-existent "…output." file,
	// finds no writer, and wrongly settles a still-running background session to
	// `ready` (the live working↔ready flapping this guards against).
	ev := p.ParseLine(bgSpawnResult("toolu_1", "bc1h56v8v",
		"Command running in background with ID: bc1h56v8v. Output is being written to: /private/tmp/claude-501/x/tasks/bc1h56v8v.output. You will be notified when it completes. To check interim output, use Read on that file path."))
	if len(ev.BackgroundSpawns) != 1 {
		t.Fatalf("BackgroundSpawns = %d, want 1", len(ev.BackgroundSpawns))
	}
	sp := ev.BackgroundSpawns[0]
	if sp.BashID != "bc1h56v8v" {
		t.Errorf("BashID = %q, want bc1h56v8v", sp.BashID)
	}
	if sp.OutputPath != "/private/tmp/claude-501/x/tasks/bc1h56v8v.output" {
		t.Errorf("OutputPath = %q (must not include the sentence-ending period)", sp.OutputPath)
	}

	// Detection must not depend on the file ending in ".output": the trailing
	// period is stripped from whatever single-token path Claude reports, so a
	// differently-named output file is still captured cleanly.
	ev2 := p.ParseLine(bgSpawnResult("toolu_2", "bd2m99zzz",
		"Command running in background with ID: bd2m99zzz. Output is being written to: /private/tmp/claude-501/x/tasks/bd2m99zzz.log. You will be notified when it completes."))
	if len(ev2.BackgroundSpawns) != 1 {
		t.Fatalf("non-.output spawn: BackgroundSpawns = %d, want 1", len(ev2.BackgroundSpawns))
	}
	if got := ev2.BackgroundSpawns[0].OutputPath; got != "/private/tmp/claude-501/x/tasks/bd2m99zzz.log" {
		t.Errorf("non-.output OutputPath = %q (must not include the sentence-ending period)", got)
	}
}

// TestParser_BackgroundSpawn_OutputPathIsStatable closes the parser↔probe
// contract that the trailing-period bug broke: the path extracted from Claude's
// launch text must be the real on-disk file, because the daemon's lsof liveness
// probe (anyLiveOutputWriter) checks that exact path. A corrupted "…output."
// path is silently un-stat-able, so lsof finds no writer and a still-running
// background session flips to `ready`. This asserts a real file embedded in the
// full launch sentence round-trips back to a path os.Stat can resolve — the
// property the probe relies on, expressed without a live process. See #445.
func TestParser_BackgroundSpawn_OutputPathIsStatable(t *testing.T) {
	out := filepath.Join(t.TempDir(), "tasks", "bc1h56v8v.output")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(out, []byte("partial output\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The exact shape Claude Code writes: the path mid-sentence, trailing prose.
	text := "Command running in background with ID: bc1h56v8v. Output is being written to: " +
		out + ". You will be notified when it completes. To check interim output, use Read on that file path."
	ev := (&Parser{}).ParseLine(bgSpawnResult("toolu_1", "bc1h56v8v", text))
	if len(ev.BackgroundSpawns) != 1 {
		t.Fatalf("BackgroundSpawns = %d, want 1", len(ev.BackgroundSpawns))
	}

	got := ev.BackgroundSpawns[0].OutputPath
	if got != out {
		t.Errorf("OutputPath = %q, want %q", got, out)
	}
	// The decisive assertion: the recorded path must resolve to the real file.
	// With the trailing period left in ("…output."), this os.Stat fails — which
	// is exactly how lsof misses the live writer in production.
	if _, err := os.Stat(got); err != nil {
		t.Errorf("recorded path is not stat-able (lsof probe would miss the live writer): %v", err)
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

// runBgTailer runs a fresh tailer to completion over lines and returns the
// resulting metrics, failing the test on any TailAndProcess error.
func runBgTailer(t *testing.T, lines []map[string]interface{}) *tailer.SessionMetrics {
	t.Helper()
	path := writeBgTranscript(t, lines)
	m, err := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code").TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	return m
}

// assertBackgroundState checks the tailer's open-background-process
// accounting: the count plus the exact set of live output paths (in order).
func assertBackgroundState(t *testing.T, m *tailer.SessionMetrics, wantCount int, wantOutputs []string) {
	t.Helper()
	if m.BackgroundProcessCount != wantCount {
		t.Fatalf("BackgroundProcessCount = %d, want %d", m.BackgroundProcessCount, wantCount)
	}
	if len(m.BackgroundProcessOutputs) != len(wantOutputs) {
		t.Fatalf("BackgroundProcessOutputs = %v, want %v", m.BackgroundProcessOutputs, wantOutputs)
	}
	for i, want := range wantOutputs {
		if m.BackgroundProcessOutputs[i] != want {
			t.Errorf("BackgroundProcessOutputs[%d] = %q, want %q", i, m.BackgroundProcessOutputs[i], want)
		}
	}
}

func TestTailer_BackgroundProcessCount_SpawnAndTerminate(t *testing.T) {
	spawnResult := "Command running in background with ID: bc1h56v8v. Output is being written to: /tmp/x/tasks/bc1h56v8v.output. You will be notified when it completes. To check interim output, use Read on that file path."

	t.Run("alive while only spawned", func(t *testing.T) {
		m := runBgTailer(t, []map[string]interface{}{
			bashToolUse("toolu_1", "Bash", map[string]interface{}{"command": "sleep 100", "run_in_background": true}),
			bgSpawnResult("toolu_1", "bc1h56v8v", spawnResult),
		})
		assertBackgroundState(t, m, 1, []string{"/tmp/x/tasks/bc1h56v8v.output"})
	})

	t.Run("cleared after observed termination", func(t *testing.T) {
		m := runBgTailer(t, []map[string]interface{}{
			bashToolUse("toolu_1", "Bash", map[string]interface{}{"command": "sleep 100", "run_in_background": true}),
			bgSpawnResult("toolu_1", "bc1h56v8v", spawnResult),
			bashToolUse("toolu_poll", "BashOutput", map[string]interface{}{"bash_id": "bc1h56v8v"}),
			toolResult("toolu_poll", "<status>completed</status>\nfinal output"),
		})
		assertBackgroundState(t, m, 0, nil)
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
		assertBackgroundState(t, m, 1, []string{"/tmp/x/tasks/bc1h56v8v.output"})
	})

	t.Run("cleared after KillShell", func(t *testing.T) {
		m := runBgTailer(t, []map[string]interface{}{
			bashToolUse("toolu_1", "Bash", map[string]interface{}{"command": "sleep 100", "run_in_background": true}),
			bgSpawnResult("toolu_1", "bc1h56v8v", spawnResult),
			bashToolUse("toolu_kill", "KillShell", map[string]interface{}{"shell_id": "bc1h56v8v"}),
		})
		assertBackgroundState(t, m, 0, nil)
	})
}

// --- Task-notification completion (orchestrated / SDK-harnessed claude) ---
// A claude launched under the Agent SDK suppresses BashOutput/KillShell and
// reports background completion via TaskOutput + a <task-notification> whose
// <task-id> is the backgroundTaskId. Two on-disk shapes carry it. See #445.

func taskNotifOriginEvent(taskID, status string) map[string]interface{} {
	return map[string]interface{}{
		"type":   "user",
		"origin": map[string]interface{}{"kind": "task-notification"},
		"message": map[string]interface{}{
			"role":    "user",
			"content": "<task-notification><task-id>" + taskID + "</task-id><tool-use-id>toolu_1</tool-use-id><status>" + status + "</status></task-notification>",
		},
	}
}

func taskNotifAttachmentEvent(taskID, status string) map[string]interface{} {
	return map[string]interface{}{
		"type": "attachment",
		"attachment": map[string]interface{}{
			"type":        "queued_command",
			"commandMode": "task-notification",
			"prompt":      "<task-notification><task-id>" + taskID + "</task-id><status>" + status + "</status></task-notification>",
		},
	}
}

func TestParser_TaskNotification_TerminatesBackgroundProcess(t *testing.T) {
	p := &Parser{}
	// origin.kind shape, terminal → emits the bg task-id
	done := p.ParseLine(taskNotifOriginEvent("bc1h56v8v", "completed"))
	if len(done.TerminatedBackgroundTaskIDs) != 1 || done.TerminatedBackgroundTaskIDs[0] != "bc1h56v8v" {
		t.Errorf("origin shape: TerminatedBackgroundTaskIDs = %v, want [bc1h56v8v]", done.TerminatedBackgroundTaskIDs)
	}
	// running → not terminal
	running := p.ParseLine(taskNotifOriginEvent("bc1h56v8v", "running"))
	if len(running.TerminatedBackgroundTaskIDs) != 0 {
		t.Errorf("running status must not terminate, got %v", running.TerminatedBackgroundTaskIDs)
	}
	// queued_command attachment shape, terminal → emits the bg task-id
	att := p.ParseLine(taskNotifAttachmentEvent("bc1h56v8v", "completed"))
	if len(att.TerminatedBackgroundTaskIDs) != 1 || att.TerminatedBackgroundTaskIDs[0] != "bc1h56v8v" {
		t.Errorf("attachment shape: TerminatedBackgroundTaskIDs = %v, want [bc1h56v8v]", att.TerminatedBackgroundTaskIDs)
	}
}

func TestTailer_BackgroundProcessCount_ClearedByTaskNotification(t *testing.T) {
	spawnResult := "Command running in background with ID: bc1h56v8v. Output is being written to: /tmp/x/tasks/bc1h56v8v.output. You will be notified when it completes. To check interim output, use Read on that file path."
	for _, tc := range []struct {
		name      string
		completed map[string]interface{}
	}{
		{"origin.kind shape", taskNotifOriginEvent("bc1h56v8v", "completed")},
		{"queued_command attachment shape", taskNotifAttachmentEvent("bc1h56v8v", "completed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeBgTranscript(t, []map[string]interface{}{
				bashToolUse("toolu_1", "Bash", map[string]interface{}{"command": "sleep 100", "run_in_background": true}),
				bgSpawnResult("toolu_1", "bc1h56v8v", spawnResult),
				tc.completed,
			})
			m, err := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code").TailAndProcess()
			if err != nil {
				t.Fatalf("TailAndProcess: %v", err)
			}
			if m.BackgroundProcessCount != 0 {
				t.Fatalf("BackgroundProcessCount = %d, want 0 after task-notification completion", m.BackgroundProcessCount)
			}
		})
	}
}
