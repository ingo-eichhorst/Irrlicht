package services

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/config"
	"irrlicht/core/domain/session"
)

// --- fakes -------------------------------------------------------------------

type diagFakeRepo struct{ sessions []*session.SessionState }

func (f *diagFakeRepo) Load(string) (*session.SessionState, error) { return nil, nil }
func (f *diagFakeRepo) Save(*session.SessionState) error           { return nil }
func (f *diagFakeRepo) Delete(string) error                        { return nil }
func (f *diagFakeRepo) ListAll() ([]*session.SessionState, error)  { return f.sessions, nil }

type diagFakeObserver struct {
	argv   map[int][]string
	cwd    map[int]string
	byName map[string][]int
}

func (f *diagFakeObserver) FindByName(n string) ([]int, error)   { return f.byName[n], nil }
func (f *diagFakeObserver) FindByCmdline(string) ([]int, error)  { return nil, nil }
func (f *diagFakeObserver) ArgvOf(pid int) ([]string, error)     { return f.argv[pid], nil }
func (f *diagFakeObserver) CWDOf(pid int) (string, error)        { return f.cwd[pid], nil }
func (f *diagFakeObserver) WriterOf(string) (int, error)         { return 0, nil }
func (f *diagFakeObserver) EnvOf(int) (map[string]string, error) { return nil, nil }

// excludeBgSpare mimics claudecode.IsInfraArgv: a "--bg-spare" element marks an
// infra process that must never be bound as a session.
func excludeBgSpare(argv []string) bool {
	for _, a := range argv {
		if a == "--bg-spare" {
			return true
		}
	}
	return false
}

func untar(t *testing.T, b []byte) map[string][]byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip.NewReader: %v", err)
	}
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar.Next: %v", err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("read %s: %v", hdr.Name, err)
		}
		out[hdr.Name] = data
	}
	return out
}

// buildTestService wires a DiagnosticsService over a temp dir populated with one
// instance file, one ledger, a log, and a permissions file — all containing a
// home path and a token to prove redaction is applied.
func buildTestService(t *testing.T) *DiagnosticsService {
	t.Helper()
	return buildTestServiceWithHooks(t, nil)
}

// buildTestServiceWithHooks is buildTestService with an explicit hook-health
// source, so a test can drive hooks.json's two collection modes: nil is the
// --diagnose CLI (a process that served no hooks), non-nil is the daemon.
func buildTestServiceWithHooks(t *testing.T, hookHealth func() HookHealthSnapshot) *DiagnosticsService {
	t.Helper()
	home := "/Users/test"
	dir := t.TempDir()
	instancesDir := filepath.Join(dir, "instances")
	ledgerDir := filepath.Join(dir, "sessions")
	logsDir := filepath.Join(dir, "logs")
	for _, d := range []string{instancesDir, ledgerDir, logsDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(p, content string) {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(instancesDir, "sess-a.json"), `{"cwd":"/Users/test/proj","token":"sk-abcdef0123456789ABCDEF"}`)
	write(filepath.Join(instancesDir, "tmp.tmp.json"), `should be skipped`)
	write(filepath.Join(ledgerDir, "abc.ledger.json"), `{"last":"/Users/test/x"}`)
	write(filepath.Join(logsDir, "events.log"), "line in /Users/test with ghp_0123456789abcdefABCDEF0123456789abcd\n")
	permFile := filepath.Join(dir, "permissions.json")
	write(permFile, `{"version":1,"agents":{}}`)

	sessions := []*session.SessionState{
		{SessionID: "a", State: session.StateWorking, Adapter: "claude-code", PID: 100, CWD: "/Users/test/proj"},
		{SessionID: "b", State: session.StateReady, Adapter: "claude-code", PID: 200},
		{SessionID: "c", State: session.StateWaiting, Adapter: "claude-code", PID: 0}, // no PID → not in liveness
	}
	obs := &diagFakeObserver{
		argv: map[int][]string{
			100: {"claude", "--bg-spare"}, // ghost: infra bound as a session
			200: {"claude"},               // healthy session
			300: {"claude", "--bg-spare"}, // unbound infra (landscape only)
		},
		cwd:    map[int]string{100: "/Users/test/proj", 200: "/Users/test/p2"},
		byName: map[string][]int{"claude": {100, 200, 300}},
	}
	isAlive := func(pid int) bool { return pid == 100 || pid == 200 || pid == 300 }
	agents := []agent.Agent{{
		Identity: agent.Identity{Name: "claude-code"},
		Process:  agent.Process{Match: agent.ExactName{Name: "claude"}, ExcludeArgv: excludeBgSpare},
	}}
	cfg := config.Config{MaxSessionAge: 5 * 24 * time.Hour, ReadySessionTTL: 30 * time.Minute, PermissionMode: "ask"}

	return NewDiagnosticsService(DiagnosticsServiceDeps{
		Repo:           &diagFakeRepo{sessions},
		Obs:            obs,
		IsAlive:        isAlive,
		Agents:         agents,
		DefaultAdapter: "claude-code",
		Cfg:            cfg,
		Version:        "9.9.9+test",
		HookHealth:     hookHealth,
		Paths: DiagnosticsPaths{
			Home:            home,
			InstancesDir:    instancesDir,
			LedgerDir:       ledgerDir,
			LogsDir:         logsDir,
			PermissionsFile: permFile,
		},
	})
}

func TestWriteBundleContents(t *testing.T) {
	var buf bytes.Buffer
	if err := buildTestService(t).WriteBundle(&buf); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	files := untar(t, buf.Bytes())

	for _, want := range []string{
		"version.txt", "system.txt", "config.json", "permissions.json",
		"state.json", "sessions.json", "liveness.json", "processes.json",
		"hooks.json", "events.log", "instances/sess-a.json", "ledgers/abc.ledger.json",
	} {
		if _, ok := files[want]; !ok {
			t.Errorf("bundle missing %s (have %v)", want, keys(files))
		}
	}
	if _, ok := files["instances/tmp.tmp.json"]; ok {
		t.Error("bundle included a .tmp instance file")
	}
	if _, ok := files["collection-errors.txt"]; ok {
		t.Errorf("unexpected collection errors:\n%s", files["collection-errors.txt"])
	}
}

func TestWriteBundleRedaction(t *testing.T) {
	var buf bytes.Buffer
	if err := buildTestService(t).WriteBundle(&buf); err != nil {
		t.Fatal(err)
	}
	files := untar(t, buf.Bytes())
	for name, data := range files {
		s := string(data)
		if strings.Contains(s, "/Users/test") {
			t.Errorf("%s leaked home path: %s", name, s)
		}
		if strings.Contains(s, "sk-abcdef") || strings.Contains(s, "ghp_0123") {
			t.Errorf("%s leaked a token: %s", name, s)
		}
	}
}

func TestLivenessFlagsGhostBinding(t *testing.T) {
	var buf bytes.Buffer
	if err := buildTestService(t).WriteBundle(&buf); err != nil {
		t.Fatal(err)
	}
	files := untar(t, buf.Bytes())

	var entries []livenessEntry
	if err := json.Unmarshal(files["liveness.json"], &entries); err != nil {
		t.Fatalf("liveness.json: %v", err)
	}
	byID := map[string]livenessEntry{}
	for _, e := range entries {
		byID[e.SessionID] = e
	}
	if len(entries) != 2 {
		t.Fatalf("liveness has %d entries, want 2 (PID-bound only): %+v", len(entries), entries)
	}
	if _, ok := byID["c"]; ok {
		t.Error("session c (PID 0) should not appear in liveness")
	}
	// Ghost: infra argv bound as a session.
	a := byID["a"]
	if !a.Alive || !a.IsInfraArgv || a.MatchesAdapterPattern {
		t.Errorf("session a should be alive+infra+!matches, got %+v", a)
	}
	// Healthy session.
	b := byID["b"]
	if !b.Alive || b.IsInfraArgv || !b.MatchesAdapterPattern {
		t.Errorf("session b should be alive+!infra+matches, got %+v", b)
	}
}

func TestProcessesCatchesUnboundInfra(t *testing.T) {
	var buf bytes.Buffer
	if err := buildTestService(t).WriteBundle(&buf); err != nil {
		t.Fatal(err)
	}
	files := untar(t, buf.Bytes())

	var groups []adapterProcesses
	if err := json.Unmarshal(files["processes.json"], &groups); err != nil {
		t.Fatalf("processes.json: %v", err)
	}
	if len(groups) != 1 || groups[0].Adapter != "claude-code" {
		t.Fatalf("want one claude-code group, got %+v", groups)
	}
	if len(groups[0].Processes) != 3 {
		t.Fatalf("landscape should list all 3 matched PIDs (incl. unbound 300), got %d", len(groups[0].Processes))
	}
	// PID 300 is unbound infra — present in the landscape but no session claims it.
	var found300 bool
	for _, p := range groups[0].Processes {
		if p.PID == 300 {
			found300 = true
			if !p.IsInfraArgv {
				t.Errorf("PID 300 should be flagged infra: %+v", p)
			}
		}
	}
	if !found300 {
		t.Error("unbound infra PID 300 missing from processes.json")
	}
}

func TestGatherTrimmedLog(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("events.log", "newA\nnewB\nnewC\n") // 15 bytes, newest
	write("events.log.1", "old1\nold2\n")     // 10 bytes, older rotation

	// Ample budget: every rotation, concatenated oldest-first.
	if got := string(gatherTrimmedLog(dir, 1000)); got != "old1\nold2\nnewA\nnewB\nnewC\n" {
		t.Errorf("full gather = %q", got)
	}
	// Budget smaller than the newest file: bounded to its tail, trimmed to a
	// line boundary (no partial leading line), newest wins the budget.
	if got := string(gatherTrimmedLog(dir, 10)); got != "newC\n" {
		t.Errorf("trimmed gather = %q, want %q", got, "newC\n")
	}
	// Missing dir and unset dir both yield nil (absent logs are not an error).
	if gatherTrimmedLog(t.TempDir(), 1000) != nil {
		t.Error("empty dir should yield nil")
	}
	if gatherTrimmedLog("", 1000) != nil {
		t.Error("empty logsDir should yield nil")
	}
}

func TestTailFromLineBoundary(t *testing.T) {
	// Shorter than n → returned whole.
	if got := string(tailFromLineBoundary([]byte("abc"), 10)); got != "abc" {
		t.Errorf("short data = %q, want abc", got)
	}
	// A single line longer than n with no newline → returned as-is (best effort).
	if got := string(tailFromLineBoundary([]byte("abcdefghij"), 4)); got != "ghij" {
		t.Errorf("no-newline tail = %q, want ghij", got)
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestHooksBundleReportsUnknownEvents covers the daemon-collected form of
// hooks.json (issue #1364): the per-(adapter, name) counts a hook receiver
// accumulated, plus the saturation total, are in the bundle a bug report
// carries.
func TestHooksBundleReportsUnknownEvents(t *testing.T) {
	svc := buildTestServiceWithHooks(t, func() HookHealthSnapshot {
		return HookHealthSnapshot{
			// Deliberately out of order: the view sorts, so two captures of the
			// same daemon diff cleanly instead of churning on map iteration.
			UnknownEvents: []UnknownHookEvent{
				{Adapter: "codex", Event: "TurnComplete", Count: 1},
				{Adapter: "claude-code", Event: "PostToolUseV2", Count: 412},
			},
			UnknownNamesDropped: map[string]uint64{"kiro-cli": 7},
			UnknownEventsTotal:  420,
		}
	})

	got := hooksJSON(t, svc)

	if got["collected_from"] != "daemon" {
		t.Errorf("collected_from = %v, want \"daemon\"", got["collected_from"])
	}
	if _, ok := got["note"]; ok {
		t.Errorf("daemon-collected hooks.json carries a note it does not need: %v", got["note"])
	}
	dropped, _ := got["unknown_event_names_dropped_by_adapter"].(map[string]any)
	if dropped["kiro-cli"] != float64(7) {
		t.Errorf("unknown_event_names_dropped_by_adapter = %v, want kiro-cli:7 — a saturated name table must not read as a complete one, and the drops must still name the adapter", got["unknown_event_names_dropped_by_adapter"])
	}
	if got["unknown_events_total"] != float64(420) {
		t.Errorf("unknown_events_total = %v, want 420 — the total is published, not left for a reader to reconstruct", got["unknown_events_total"])
	}
	events, _ := got["unknown_events"].([]any)
	if len(events) != 2 {
		t.Fatalf("unknown_events has %d entries, want 2: %v", len(events), got["unknown_events"])
	}
	first, _ := events[0].(map[string]any)
	if first["adapter"] != "claude-code" || first["event"] != "PostToolUseV2" || first["count"] != float64(412) {
		t.Errorf("first row = %v, want claude-code/PostToolUseV2/412 (sorted by adapter then event)", first)
	}
}

// TestHooksBundleOmitsCountsWhenNotCollectedInDaemon is the honesty obligation.
// `irrlichd --diagnose` builds its bundle in a process that never served a hook,
// so its counters are structurally zero; publishing them as counts would tell a
// bug reporter "no unrecognized events" on evidence that could not exist.
func TestHooksBundleOmitsCountsWhenNotCollectedInDaemon(t *testing.T) {
	got := hooksJSON(t, buildTestServiceWithHooks(t, nil))

	if got["collected_from"] != "cli" {
		t.Errorf("collected_from = %v, want \"cli\"", got["collected_from"])
	}
	// Not just the list: ANY count from a process that served no hooks is a
	// number nobody observed, and a zero reads as an all-clear.
	// The #1368 fields inherit the same obligation and are listed here, not
	// only in their own test: the two shapes differ by a handful of fields and
	// are an obvious candidate for being unified later, and the moment they are
	// this loop is the only thing standing between that refactor and publishing
	// `"hook_requests_received": 0` from a process that served none.
	for _, field := range []string{
		"unknown_events", "unknown_events_total", "unknown_event_names_dropped_by_adapter",
		"hook_requests_received", "channels", "silent_channel_note",
		// The #1372 DATA fields, on the same footing. Its note is deliberately
		// not in this list: like liveness_note, the CLI form publishes an
		// explanation of the absence, which is the opposite of publishing a
		// number nobody observed.
		"entry_reverification", "entry_reverification_outcomes",
	} {
		if v, ok := got[field]; ok {
			t.Errorf("%s is present without a daemon to read it from: %v", field, v)
		}
	}
	note, _ := got["note"].(string)
	if !strings.Contains(note, "events.log") || !strings.Contains(note, "/debug/bundle") {
		t.Errorf("note does not say where the real evidence is: %q", note)
	}
}

// TestHooksBundleReportsChannelLiveness covers the #1368 half of hooks.json:
// which hook channels are being watched, what each has delivered, and which are
// currently treated as dead.
func TestHooksBundleReportsChannelLiveness(t *testing.T) {
	svc := buildTestServiceWithHooks(t, func() HookHealthSnapshot {
		return HookHealthSnapshot{
			ReceiptsTotal: 91,
			Channels: []HookChannelHealth{
				{Adapter: "claude-code", Armed: true, Receipts: 0, TurnsSinceReceipt: 9, Silent: true},
				{Adapter: "codex", Armed: true, Receipts: 91, TurnsSinceReceipt: 0},
				{Adapter: "kiro-cli"},
			},
		}
	})

	got := hooksJSON(t, svc)

	if got["hook_requests_received"] != float64(91) {
		t.Errorf("hook_requests_received = %v, want 91", got["hook_requests_received"])
	}
	channels, _ := got["channels"].([]any)
	if len(channels) != 3 {
		t.Fatalf("channels has %d entries, want 3: %v", len(channels), got["channels"])
	}

	dead, _ := channels[0].(map[string]any)
	if dead["adapter"] != "claude-code" || dead["silent"] != true || dead["turns_since_receipt"] != float64(9) {
		t.Errorf("dead channel row = %v, want claude-code silent after 9 turns", dead)
	}
	if dead["armed"] != true {
		t.Errorf("a silent row must also say it was armed: %v — otherwise a reader cannot tell a dead channel from an uninstalled one", dead)
	}
	unwatched, _ := channels[2].(map[string]any)
	if v, ok := unwatched["armed"]; ok && v == true {
		t.Errorf("an adapter with no consent must not read as armed: %v", unwatched)
	}

	// The three-way disambiguation is the point of the section. Collapsing this
	// ticket's diagnosis with #1372's or #1362's is the failure mode the note
	// exists to prevent, so it is asserted rather than trusted.
	note, _ := got["silent_channel_note"].(string)
	if note == "" {
		t.Fatal("a silent channel must carry an explanation; hooks.json is what a bug reporter pastes")
	}
	for _, want := range []string{"claude-code", "#1368", "#1372", "#1362", "effect_error"} {
		if !strings.Contains(note, want) {
			t.Errorf("silent_channel_note must mention %q so the reader is not sent to the wrong fix; got:\n%s", want, note)
		}
	}
}

// TestHooksBundleOmitsTheLivenessNoteWhenHealthy keeps the healthy bundle
// terse: a paragraph of failure prose printed on every bundle would train
// readers to skip the one that matters.
func TestHooksBundleOmitsTheLivenessNoteWhenHealthy(t *testing.T) {
	got := hooksJSON(t, buildTestServiceWithHooks(t, func() HookHealthSnapshot {
		return HookHealthSnapshot{
			ReceiptsTotal: 12,
			Channels:      []HookChannelHealth{{Adapter: "codex", Armed: true, Receipts: 12}},
		}
	}))
	if v, ok := got["silent_channel_note"]; ok {
		t.Errorf("a healthy bundle carries the silent-channel explanation: %v", v)
	}
}

// TestHooksBundleOmitsLivenessWhenNotCollectedInDaemon extends #1364's honesty
// obligation to this feature, and the obligation is strictly stronger here.
// `--diagnose` served no hooks AND observed no turns, so BOTH sides of the
// ratio are structurally absent: a channel published from that process would
// read as silent with zero turns behind it, which is an accusation rather than
// a measurement.
func TestHooksBundleOmitsLivenessWhenNotCollectedInDaemon(t *testing.T) {
	got := hooksJSON(t, buildTestServiceWithHooks(t, nil))

	for _, field := range []string{"channels", "hook_requests_received", "silent_channel_note"} {
		if v, ok := got[field]; ok {
			t.Errorf("%s is present without a daemon to read it from: %v", field, v)
		}
	}
	note, _ := got["liveness_note"].(string)
	if !strings.Contains(note, "no turns") || !strings.Contains(note, "/debug/bundle") {
		t.Errorf("liveness_note must say why it is absent and where the real view is: %q", note)
	}
}

// hooksJSON pulls hooks.json out of a freshly written bundle.
func hooksJSON(t *testing.T, svc *DiagnosticsService) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	if err := svc.WriteBundle(&buf); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	raw, ok := untar(t, buf.Bytes())["hooks.json"]
	if !ok {
		t.Fatal("bundle has no hooks.json")
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("hooks.json is not valid JSON: %v\n%s", err, raw)
	}
	return out
}

// --- #1372: the entry re-verification half of hooks.json ------------------

// TestHooksBundleReportsEntryReverification covers what a bug reporter needs to
// see when the agent's own settings UI has been eating our entries: which file,
// how many times it has been put back, and whether the loop is currently
// backing off from a fight it is losing.
func TestHooksBundleReportsEntryReverification(t *testing.T) {
	svc := buildTestServiceWithHooks(t, func() HookHealthSnapshot {
		return HookHealthSnapshot{
			ReceiptsTotal: 4,
			EntryReverification: HookEntryReverifySnapshot{
				Outcomes: map[ReverifyOutcome]uint64{
					ReverifyIntact: 40, ReverifyRepaired: 7, ReverifyBackoff: 12,
				},
				Targets: []HookEntryHealth{
					{
						Adapter: "claude-code", Permission: "hooks",
						ConfigPath: "/Users/x/.claude/settings.json", Watched: true,
						Missing: []string{"Stop"}, Repairs: 7, ConsecutiveRepairs: 5,
						LastOutcome: string(ReverifyRepaired), BackoffSeconds: 1800,
					},
					{Adapter: "codex", Permission: "hooks", Watched: false},
				},
			},
		}
	})

	got := hooksJSON(t, svc)

	rows, _ := got["entry_reverification"].([]any)
	if len(rows) != 2 {
		t.Fatalf("entry_reverification has %d rows, want 2: %v", len(rows), got["entry_reverification"])
	}
	damaged, _ := rows[0].(map[string]any)
	if damaged["adapter"] != "claude-code" || damaged["repairs"] != float64(7) {
		t.Errorf("damaged row = %v, want claude-code with 7 repairs", damaged)
	}
	if damaged["config_path"] == nil || damaged["config_path"] == "" {
		t.Errorf("row carries no config_path; the reader cannot go look: %v", damaged)
	}
	if damaged["backoff_seconds"] != float64(1800) {
		t.Errorf("backoff_seconds = %v, want 1800 — the deferral in force is how a reader "+
			"tells a one-off clobber from a repair storm", damaged["backoff_seconds"])
	}
	unwatched, _ := rows[1].(map[string]any)
	if v, ok := unwatched["watched"]; ok && v == true {
		t.Errorf("a row with no consent must not read as watched: %v", unwatched)
	}

	outcomes, _ := got["entry_reverification_outcomes"].(map[string]any)
	if outcomes[string(ReverifyRepaired)] != float64(7) {
		t.Errorf("outcomes[%s] = %v, want 7", ReverifyRepaired, outcomes[string(ReverifyRepaired)])
	}
	if _, present := outcomes[string(ReverifyRepairFailed)]; present {
		t.Errorf("a zero outcome is published: %v — zeros read as an all-clear", outcomes)
	}

	// Same obligation the silent-channel note carries, and for the same reason:
	// three causes, three first moves, and a reader who should not have to know
	// which issue owns which.
	note, _ := got["entry_reverification_note"].(string)
	if note == "" {
		t.Fatal("repaired entries must carry an explanation; hooks.json is what a bug reporter pastes")
	}
	for _, want := range []string{"claude-code", "#1372", "#1368", "#1362", "effect_error", "watched:false"} {
		if !strings.Contains(note, want) {
			t.Errorf("entry_reverification_note must mention %q so the reader is not sent to the "+
				"wrong fix; got:\n%s", want, note)
		}
	}
}

// A healthy machine gets rows and no essay — the same terseness rule the
// silent-channel note follows.
func TestHooksBundleOmitsTheReverificationNoteWhenHealthy(t *testing.T) {
	got := hooksJSON(t, buildTestServiceWithHooks(t, func() HookHealthSnapshot {
		return HookHealthSnapshot{
			EntryReverification: HookEntryReverifySnapshot{
				Outcomes: map[ReverifyOutcome]uint64{ReverifyIntact: 99},
				Targets: []HookEntryHealth{
					{Adapter: "codex", Permission: "hooks", Watched: true, LastOutcome: string(ReverifyIntact)},
				},
			},
		}
	}))
	if v, ok := got["entry_reverification_note"]; ok {
		t.Errorf("a healthy bundle carries the re-verification essay: %v", v)
	}
	if rows, _ := got["entry_reverification"].([]any); len(rows) != 1 {
		t.Errorf("the ROWS must still be published when healthy (got %v) — an absent row is "+
			"indistinguishable from a bundle collected before the feature existed", got["entry_reverification"])
	}
}

// The --diagnose process runs no passes, so publishing "entries verified
// present" from it would be a measurement nobody took. Same honesty obligation
// as #1364's counters and #1368's channels.
func TestHooksBundleOmitsReverificationWhenNotCollectedInDaemon(t *testing.T) {
	got := hooksJSON(t, buildTestServiceWithHooks(t, nil))

	for _, field := range []string{"entry_reverification", "entry_reverification_outcomes"} {
		if v, ok := got[field]; ok {
			t.Errorf("%s is present without a daemon to read it from: %v", field, v)
		}
	}
	note, _ := got["entry_reverification_note"].(string)
	if !strings.Contains(note, "#1372") || !strings.Contains(note, "/debug/bundle") {
		t.Errorf("entry_reverification_note must say why it is absent and where the real view "+
			"is: %q", note)
	}
	if !strings.Contains(note, "effect_error") {
		t.Errorf("the CLI note must still point at permissions.json for a failed repair, since "+
			"that IS readable from this process: %q", note)
	}
}
