package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

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

// TestParser_BackgroundSpawn_AutoBackgroundedOnTimeout covers the second
// launch shape Claude Code writes into a Bash tool_result: the harness's own
// 120s-timeout auto-background, not a `run_in_background: true` launch. The
// prose differs ("…was moved to the background (ID: X)…" vs "…running in
// background with ID: X…"), but the structured toolUseResult.backgroundTaskId
// is present exactly as it is for a real launch. Exact transcript text from
// issue #2028 (session ecca8475-a20b-4f58-98cf-4e303a6f91c7).
func TestParser_BackgroundSpawn_AutoBackgroundedOnTimeout(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(bgSpawnResult("toolu_1", "b5d7261fo",
		"Command did not complete within its 120s timeout and was moved to the background (ID: b5d7261fo). "+
			"Output is being written to: /private/tmp/claude-501/-Users-ingo-projects-irrlicht/ecca8475-a20b-4f58-98cf-4e303a6f91c7/tasks/b5d7261fo.output. "+
			"You will be notified when it completes. To check interim output, use Read on that file path."))
	if len(ev.BackgroundSpawns) != 1 {
		t.Fatalf("BackgroundSpawns = %d, want 1", len(ev.BackgroundSpawns))
	}
	sp := ev.BackgroundSpawns[0]
	if sp.BashID != "b5d7261fo" {
		t.Errorf("BashID = %q, want b5d7261fo", sp.BashID)
	}
	if sp.OutputPath != "/private/tmp/claude-501/-Users-ingo-projects-irrlicht/ecca8475-a20b-4f58-98cf-4e303a6f91c7/tasks/b5d7261fo.output" {
		t.Errorf("OutputPath = %q (must not include the sentence-ending period)", sp.OutputPath)
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

// TestTailer_BackgroundProcessCount_AutoBackgroundedSurvivesTurnEnd covers
// issue #2028: a Bash command the harness auto-backgrounds after its 120s
// foreground timeout must keep the session in `working` across the turn
// boundary, exactly like a real `run_in_background: true` launch. Before the
// fix, backgroundSpawnRe matched only the run_in_background launch prose, so
// this shape recorded no spawn and the tailer's open-background-process count
// stayed 0 straight through turn_duration — the wrong-ready bug from #2028.
func TestTailer_BackgroundProcessCount_AutoBackgroundedSurvivesTurnEnd(t *testing.T) {
	autoBgResult := "Command did not complete within its 120s timeout and was moved to the background (ID: b5d7261fo). " +
		"Output is being written to: /private/tmp/x/tasks/b5d7261fo.output. " +
		"You will be notified when it completes. To check interim output, use Read on that file path."

	path := writeBgTranscript(t, []map[string]interface{}{
		bashToolUse("toolu_1", "Bash", map[string]interface{}{"command": "tools/preflight.sh --only go"}),
		bgSpawnResult("toolu_1", "b5d7261fo", autoBgResult),
		{"type": "system", "subtype": "turn_duration"},
	})
	tl := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	assertBackgroundState(t, m, 1, []string{"/private/tmp/x/tasks/b5d7261fo.output"})

	// The terminal <task-notification> for the auto-backgrounded id clears the
	// ledger the same way it does for a real run_in_background launch.
	appendTranscript(t, path, []map[string]interface{}{
		taskNotifOriginEvent("b5d7261fo", "completed"),
	})
	m2, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess after task-notification: %v", err)
	}
	assertBackgroundState(t, m2, 0, nil)
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
	type result struct {
		Skip                   bool
		OriginTaskNotification bool
		TerminatedIDs          []string
	}
	tests := []struct {
		name  string
		event map[string]interface{}
		want  result
	}{
		{"terminal origin", taskNotifOriginEvent("bc1h56v8v", "completed"), result{true, true, []string{"bc1h56v8v"}}},
		{"running origin", taskNotifOriginEvent("bc1h56v8v", "running"), result{true, true, nil}},
		{"terminal attachment", taskNotifAttachmentEvent("bc1h56v8v", "completed"), result{true, false, []string{"bc1h56v8v"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ev := p.ParseLine(tc.event)
			got := result{ev.Skip, ev.OriginTaskNotification, ev.TerminatedBackgroundTaskIDs}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("task-notification result = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestTailer_BackgroundProcessCount_ClearedByTaskNotification(t *testing.T) {
	spawnResult := "Command running in background with ID: bc1h56v8v. Output is being written to: /tmp/x/tasks/bc1h56v8v.output. You will be notified when it completes. To check interim output, use Read on that file path."
	for _, tc := range []struct {
		name          string
		completed     map[string]interface{}
		wantLastEvent string
	}{
		{"origin.kind shape", taskNotifOriginEvent("bc1h56v8v", "completed"), "agent_continuation"},
		{"queued_command attachment shape", taskNotifAttachmentEvent("bc1h56v8v", "completed"), "turn_done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeBgTranscript(t, []map[string]interface{}{
				bashToolUse("toolu_1", "Bash", map[string]interface{}{"command": "sleep 100", "run_in_background": true}),
				bgSpawnResult("toolu_1", "bc1h56v8v", spawnResult),
				{"type": "system", "subtype": "turn_duration"},
				tc.completed,
			})
			m, err := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code").TailAndProcess()
			if err != nil {
				t.Fatalf("TailAndProcess: %v", err)
			}
			if m.BackgroundProcessCount != 0 {
				t.Fatalf("BackgroundProcessCount = %d, want 0 after task-notification completion", m.BackgroundProcessCount)
			}
			if m.LastEventType != tc.wantLastEvent {
				t.Errorf("LastEventType = %q, want %q", m.LastEventType, tc.wantLastEvent)
			}
		})
	}
}

// A task-notification for a subagent has no matching Bash background-process
// entry. It must preserve the preceding turn_done anchor. The background-agent
// scenario ends with killed subagent notifications and no resumed assistant
// event, so treating origin.kind alone as a continuation leaves it working
// forever. This is a lock for the structural ledger-match discriminator in
// issue #1899.
// --- Monitor task registration (issue #1982) ---
// A Claude Code `Monitor` task is a background task that wakes the agent on
// each event, the same way a Bash `run_in_background` process does — but it
// reports its id via `toolUseResult.taskId` (not `backgroundTaskId`) and its
// launch text is "Monitor started (task <id>, timeout <n>ms)." rather than
// the Bash "running in background with ID: <id>" shape. Registering it into
// the same #445 ledger is what lets IsAgentDone() hold the session `working`
// past the Stop hook that closes each Monitor-driven turn.

// monitorSpawnResult builds the user event Claude Code writes for a Monitor
// launch: a tool_result with the launch text plus the authoritative top-level
// toolUseResult.taskId/timeoutMs/persistent fields.
func monitorSpawnResult(toolUseID, taskID, content string, timeoutMs int64, persistent bool) map[string]interface{} {
	ev := toolResult(toolUseID, content)
	ev["toolUseResult"] = map[string]interface{}{
		"taskId":     taskID,
		"timeoutMs":  timeoutMs,
		"persistent": persistent,
	}
	return ev
}

// monitorSpawnResultAt is monitorSpawnResult plus an explicit top-level
// "timestamp", for a test that needs to control the transcript-derived
// launch time the tailer computes the Monitor deadline from (tailer.
// ParseTimestamp reads this field; a line without it falls back to
// time.Now(), which a deadline-expiry test cannot hold fixed).
func monitorSpawnResultAt(toolUseID, taskID, content string, timeoutMs int64, persistent bool, timestamp string) map[string]interface{} {
	ev := monitorSpawnResult(toolUseID, taskID, content, timeoutMs, persistent)
	ev["timestamp"] = timestamp
	return ev
}

// TestParser_NoPhantomMonitorSpawnFromArbitraryText is monitorLaunchOf's
// counterpart to TestParser_NoPhantomSpawnFromArbitraryText: the Monitor
// launch phrase alone (echoed by some other tool's output, e.g. a Read over
// a log) must not fabricate a Monitor entry without the structured
// toolUseResult.taskId gate. Mutation fixture — dropping collectToolResult's
// `monitorID != ""` gate, or monitorLaunchOf's own `if id == ""` early
// return, turns this red; verified by hand during development.
func TestParser_NoPhantomMonitorSpawnFromArbitraryText(t *testing.T) {
	p := &Parser{}
	ev := p.ParseLine(toolResult("toolu_x",
		"Monitor started (task bhqmawaqk, timeout 1500000ms). You will be notified on each event."))
	if len(ev.BackgroundSpawns) != 0 {
		t.Errorf("phantom Monitor spawn from un-gated text: %+v", ev.BackgroundSpawns)
	}
}

func TestTailer_BackgroundProcessCount_MonitorSpawn(t *testing.T) {
	content := "Monitor started (task bhqmawaqk, timeout 1500000ms). You will be notified on each event. " +
		"Keep working — do not poll or sleep. Events may arrive while you are waiting for the user — an event is not their reply."

	m := runBgTailer(t, []map[string]interface{}{
		bashToolUse("toolu_1", "Monitor", map[string]interface{}{"description": "PR #1981 CI checks completing"}),
		monitorSpawnResult("toolu_1", "bhqmawaqk", content, 1500000, false),
	})
	if m.BackgroundProcessCount != 1 {
		t.Fatalf("BackgroundProcessCount = %d, want 1 after a Monitor launch", m.BackgroundProcessCount)
	}
}

// TestTailer_MonitorTask_ReleasedByTaskNotification is the mutation fixture
// for the release path bullet 1 of #1982's design: "a terminal
// <task-notification> with a <status> (already parsed)". The triage comment
// verified this path already works for a TRACKED id — handleTaskNotification
// (parser.go) has turned a terminal <status> into TerminatedBackgroundTaskIDs
// since #445, and applyBackgroundProcessTerminations deletes any tracked id
// from openBackgroundProcs regardless of how it got there. What #1982 adds is
// registration; this test asserts the hold is placed FIRST (so a reverted
// registration is caught, not just a reverted release) and then confirms the
// existing release path actually retires it. A test that only checked the
// post-release count of 0 would also pass with registration never having
// happened at all.
func TestTailer_MonitorTask_ReleasedByTaskNotification(t *testing.T) {
	content := "Monitor started (task bhqmawaqk, timeout 1500000ms). You will be notified on each event."
	path := writeBgTranscript(t, []map[string]interface{}{
		bashToolUse("toolu_1", "Monitor", map[string]interface{}{"description": "PR #1981 CI checks completing"}),
		monitorSpawnResult("toolu_1", "bhqmawaqk", content, 1500000, false),
	})
	tl := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
	m, err := tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess (spawn): %v", err)
	}
	if m.BackgroundProcessCount != 1 {
		t.Fatalf("BackgroundProcessCount after spawn = %d, want 1 (hold not placed)", m.BackgroundProcessCount)
	}

	appendTranscriptLine(t, path, map[string]interface{}{"type": "system", "subtype": "turn_duration"})
	appendTranscriptLine(t, path, taskNotifOriginEvent("bhqmawaqk", "completed"))
	m, err = tl.TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess (release): %v", err)
	}
	if m.BackgroundProcessCount != 0 {
		t.Fatalf("BackgroundProcessCount after terminal notification = %d, want 0 (hold not released)", m.BackgroundProcessCount)
	}
	// A real task-notification line is Skip=true, so this release actually
	// runs through applySkippedEvent -> applyBackgroundProcessTerminations,
	// NOT applyBackgroundProcessDeltas's own TerminatedBackgroundTaskIDs loop
	// (that loop is dead for this exact path — see its comment). Asserting
	// BackgroundProcessClockBound here, not just BackgroundProcessCount, is
	// what catches openBackgroundDeadlines being cleared on the wrong side of
	// that split — exactly the gap PR #1984's code review found.
	if m.BackgroundProcessClockBound {
		t.Error("BackgroundProcessClockBound after terminal notification = true, want false (the deadline entry leaked)")
	}
	if m.LastEventType != "agent_continuation" {
		t.Errorf("LastEventType = %q, want agent_continuation (terminal notification for a tracked id starts the next inference turn)", m.LastEventType)
	}
}

// TestTailer_MonitorTask_DeadlineExpiry is the mutation fixture for #1982's
// second and third release conditions: "the timeoutMs deadline for a
// non-persistent Monitor" and "a hold ceiling for a persistent Monitor".
// Neither has a pre-fix "red" to run — there was no deadline concept on
// main — so this instead pins both a live and an expired case for each kind,
// which is what catches a broken/inverted expiry check: commenting out
// purgeExpiredBackgroundDeadlines's delete calls, or its `!now.Before`
// comparison, turns the two "elapsed" subtests red (BackgroundProcessCount
// stays 1) while leaving the two "not yet elapsed" subtests green — verified
// by hand during development (see the PR body for the captured output), and
// permanently pinned here as the regression fixture.
func TestTailer_MonitorTask_DeadlineExpiry(t *testing.T) {
	const content = "Monitor started (task bhqmawaqk, timeout 1500000ms)."
	longAgo := time.Now().Add(-24 * time.Hour).Format(time.RFC3339)
	past13h := time.Now().Add(-13 * time.Hour).Format(time.RFC3339)
	recent := time.Now().Format(time.RFC3339)

	tests := []struct {
		name       string
		timestamp  string
		timeoutMs  int64
		persistent bool
		wantCount  int
	}{
		{
			name:      "non-persistent, timeoutMs not yet elapsed, stays open",
			timestamp: recent,
			timeoutMs: 1_000_000_000, // ~11.5 days — nowhere near elapsed
			wantCount: 1,
		},
		{
			name:      "non-persistent, timeoutMs elapsed, purged",
			timestamp: longAgo,
			timeoutMs: 1000, // 1s past a launch 24h ago — long elapsed
			wantCount: 0,
		},
		{
			name:       "persistent, within the hold ceiling, stays open",
			timestamp:  recent,
			persistent: true,
			wantCount:  1,
		},
		{
			name:       "persistent, past the hold ceiling, purged",
			timestamp:  past13h,
			persistent: true, // ceiling is 12h (monitorPersistentHoldCeiling)
			wantCount:  0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := runBgTailer(t, []map[string]interface{}{
				bashToolUse("toolu_1", "Monitor", map[string]interface{}{"description": "long-running check"}),
				monitorSpawnResultAt("toolu_1", "bhqmawaqk", content, tc.timeoutMs, tc.persistent, tc.timestamp),
			})
			if m.BackgroundProcessCount != tc.wantCount {
				t.Errorf("BackgroundProcessCount = %d, want %d", m.BackgroundProcessCount, tc.wantCount)
			}
		})
	}
}

// TestTailer_MonitorTask_SurvivesLedgerRoundTrip mirrors
// TestTailer_BackgroundProcessCount_SpawnAndTerminate's "open set survives a
// ledger round-trip (daemon restart)" subtest for the clock-bound path: a
// daemon restart must not drop a still-live Monitor hold, and a persisted
// deadline must round-trip rather than being silently reset. See issue #1982.
func TestTailer_MonitorTask_SurvivesLedgerRoundTrip(t *testing.T) {
	content := "Monitor started (task bhqmawaqk, timeout 1500000ms)."
	path := writeBgTranscript(t, []map[string]interface{}{
		bashToolUse("toolu_1", "Monitor", map[string]interface{}{"description": "PR #1981 CI checks completing"}),
		// timeoutMs is 25 minutes, launched "now" — nowhere near its deadline,
		// so it must still read open both before and after the restart.
		monitorSpawnResultAt("toolu_1", "bhqmawaqk", content, 1_500_000, false, time.Now().Format(time.RFC3339)),
	})

	tl1 := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
	if _, err := tl1.TailAndProcess(); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	ledger := tl1.GetLedgerState()
	if len(ledger.BackgroundProcs) != 1 {
		t.Fatalf("ledger BackgroundProcs = %v, want one entry", ledger.BackgroundProcs)
	}
	if len(ledger.BackgroundDeadlines) != 1 {
		t.Fatalf("ledger BackgroundDeadlines = %v, want one entry (the Monitor task's persisted deadline)", ledger.BackgroundDeadlines)
	}

	// Restart: a fresh tailer rehydrated from the ledger reports the Monitor
	// task still open even though it reads no new transcript lines, and keeps
	// the SAME deadline rather than resetting the clock.
	tl2 := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code")
	tl2.SetLedgerState(ledger)
	m, err := tl2.TailAndProcess()
	if err != nil {
		t.Fatalf("post-restart pass: %v", err)
	}
	if m.BackgroundProcessCount != 1 {
		t.Fatalf("BackgroundProcessCount after restart = %d, want 1", m.BackgroundProcessCount)
	}
	if !m.BackgroundProcessClockBound {
		t.Error("BackgroundProcessClockBound after restart = false, want true")
	}

	ledger2 := tl2.GetLedgerState()
	if ledger2.BackgroundDeadlines["bhqmawaqk"] != ledger.BackgroundDeadlines["bhqmawaqk"] {
		t.Errorf("deadline after restart = %d, want the original %d (restart must not reset the clock)",
			ledger2.BackgroundDeadlines["bhqmawaqk"], ledger.BackgroundDeadlines["bhqmawaqk"])
	}
}

func TestTailer_UnmatchedTaskNotification_RemainsPassive(t *testing.T) {
	path := writeBgTranscript(t, []map[string]interface{}{
		{"type": "system", "subtype": "turn_duration"},
		taskNotifOriginEvent("subagent-aecb65d9", "killed"),
	})
	m, err := tailer.NewTranscriptTailer(path, &Parser{}, "claude-code").TailAndProcess()
	if err != nil {
		t.Fatalf("TailAndProcess: %v", err)
	}
	if m.LastEventType != "turn_done" {
		t.Errorf("LastEventType = %q, want turn_done for unmatched subagent notification", m.LastEventType)
	}
	if len(m.SubagentCompletions) != 1 {
		t.Fatalf("SubagentCompletions = %d, want 1", len(m.SubagentCompletions))
	}
}
