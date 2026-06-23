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

	return NewDiagnosticsService(&diagFakeRepo{sessions}, obs, isAlive, agents, cfg, "9.9.9+test", DiagnosticsPaths{
		Home:            home,
		InstancesDir:    instancesDir,
		LedgerDir:       ledgerDir,
		LogsDir:         logsDir,
		PermissionsFile: permFile,
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
		"events.log", "instances/sess-a.json", "ledgers/abc.ledger.json",
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

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
