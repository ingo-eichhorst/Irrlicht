package processlifecycle

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"irrlicht/core/domain/session"
)

// fakeHerdr serves herdr's newline-delimited JSON-RPC on a unix socket, so
// these tests exercise the real dial/encode/decode path rather than a stub of
// it. The wire format is copied from a live herdr 0.8.0: a request is
// {"id","method","params"} and a reply is {"id","result":{...}}.
type fakeHerdr struct {
	t *testing.T

	panes map[string]fakePane // pane id -> its processes

	mu    sync.Mutex
	calls []string // methods served, in order, so a test can assert cost
}

type fakePane struct {
	cwd           string
	foregroundCWD string
	shellPID      int
	foreground    []int
}

// start listens on <dir>/<session>/herdr.sock and points herdrSessionsDirFn at
// dir for the duration of the test.
func (f *fakeHerdr) start(dir, sessionName string) string {
	f.t.Helper()
	sessionDir := filepath.Join(dir, sessionName)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		f.t.Fatalf("mkdir session dir: %v", err)
	}
	sock := filepath.Join(sessionDir, "herdr.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		f.t.Fatalf("listen on %s: %v", sock, err)
	}
	f.t.Cleanup(func() { _ = ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(conn)
		}
	}()
	return sock
}

func (f *fakeHerdr) serve(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var req struct {
			ID     string         `json:"id"`
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			return
		}
		f.mu.Lock()
		f.calls = append(f.calls, req.Method)
		f.mu.Unlock()

		result, ok := f.result(req.Method, req.Params)
		if !ok {
			return
		}
		body, err := json.Marshal(map[string]any{"id": req.ID, "result": result})
		if err != nil {
			return
		}
		if _, err := conn.Write(append(body, '\n')); err != nil {
			return
		}
	}
}

func (f *fakeHerdr) result(method string, params map[string]any) (any, bool) {
	switch method {
	case "pane.list":
		panes := make([]map[string]any, 0, len(f.panes))
		for _, id := range f.sortedPaneIDs() {
			p := f.panes[id]
			panes = append(panes, map[string]any{
				"pane_id":        id,
				"cwd":            p.cwd,
				"foreground_cwd": p.foregroundCWD,
			})
		}
		return map[string]any{"type": "pane_list", "panes": panes}, true
	case "pane.process_info":
		id, _ := params["pane_id"].(string)
		p, ok := f.panes[id]
		if !ok {
			return nil, false
		}
		procs := make([]map[string]any, 0, len(p.foreground))
		for _, pid := range p.foreground {
			procs = append(procs, map[string]any{"pid": pid, "name": "node", "argv0": "pi"})
		}
		return map[string]any{
			"type": "pane_process_info",
			"process_info": map[string]any{
				"pane_id":              id,
				"shell_pid":            p.shellPID,
				"foreground_processes": procs,
			},
		}, true
	default:
		return nil, false
	}
}

// sortedPaneIDs keeps pane.list deterministic, so a test asserting which pane
// was asked about first is asserting the code's ordering rather than Go's map
// iteration.
func (f *fakeHerdr) sortedPaneIDs() []string {
	ids := make([]string, 0, len(f.panes))
	for id := range f.panes {
		ids = append(ids, id)
	}
	for i := range ids {
		for j := i + 1; j < len(ids); j++ {
			if ids[j] < ids[i] {
				ids[i], ids[j] = ids[j], ids[i]
			}
		}
	}
	return ids
}

func (f *fakeHerdr) methodCalls(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, m := range f.calls {
		if m == method {
			n++
		}
	}
	return n
}

// shortTempDir returns a directory short enough to hold a unix socket path.
//
// Not t.TempDir(): a sockaddr_un's sun_path is 104 bytes on darwin, and
// t.TempDir() builds its path from the test's own name under a long
// /var/folders/... prefix, so a socket inside it fails to bind with EINVAL.
// The test name is exactly what makes it too long, so the limit is reached
// sooner the more descriptive the test is named.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "irrherdr")
	if err != nil {
		t.Fatalf("mkdtemp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// useSessionsDir points the production socket lookup at a fixture tree.
func useSessionsDir(t *testing.T, dir string) {
	t.Helper()
	prev := herdrSessionsDirFn
	herdrSessionsDirFn = func() string { return dir }
	t.Cleanup(func() { herdrSessionsDirFn = prev })
}

// #1934's defect test. Before adoptHerdrPane existed this returned no pane for
// a process whose environment could not be read, which is the whole bug.
func TestHerdrPaneForPIDResolvesByExactPID(t *testing.T) {
	dir := shortTempDir(t)
	f := &fakeHerdr{t: t, panes: map[string]fakePane{
		"w1:p1": {cwd: "/work/a", foregroundCWD: "/work/a", shellPID: 100, foreground: []int{101}},
		"w1:p2": {cwd: "/work/b", foregroundCWD: "/work/b", shellPID: 200, foreground: []int{201}},
	}}
	sock := f.start(dir, "factory")
	useSessionsDir(t, dir)

	pane, gotSock, probed := herdrPaneForPID(context.Background(), 201, "/work/b")
	if !probed {
		t.Fatal("probed=false, but the fake socket answered")
	}
	if pane != "w1:p2" {
		t.Errorf("pane = %q, want w1:p2", pane)
	}
	if gotSock != sock {
		t.Errorf("socket = %q, want %q", gotSock, sock)
	}
}

// The pane must be decided by pid, never by the working directory used to
// narrow candidates. Two sessions in one directory is the case that made
// cwd-based correlation unusable in the first place.
func TestHerdrPaneForPIDDoesNotGuessFromCWD(t *testing.T) {
	dir := shortTempDir(t)
	f := &fakeHerdr{t: t, panes: map[string]fakePane{
		"w2:p1": {cwd: "/shared", foregroundCWD: "/shared", shellPID: 300, foreground: []int{301}},
		"w2:p2": {cwd: "/shared", foregroundCWD: "/shared", shellPID: 400, foreground: []int{401}},
	}}
	f.start(dir, "factory")
	useSessionsDir(t, dir)

	for pid, want := range map[int]string{301: "w2:p1", 401: "w2:p2"} {
		pane, _, probed := herdrPaneForPID(context.Background(), pid, "/shared")
		if !probed {
			t.Fatalf("pid %d: probed=false", pid)
		}
		if pane != want {
			t.Errorf("pid %d resolved to %q, want %q", pid, pane, want)
		}
	}
}

// A pane whose agent has exited still belongs to the session observed in that
// instant, so the shell pid counts as a match.
func TestHerdrPaneForPIDMatchesTheShell(t *testing.T) {
	dir := shortTempDir(t)
	f := &fakeHerdr{t: t, panes: map[string]fakePane{
		"w3:p1": {cwd: "/work", foregroundCWD: "/work", shellPID: 500, foreground: nil},
	}}
	f.start(dir, "factory")
	useSessionsDir(t, dir)

	pane, _, probed := herdrPaneForPID(context.Background(), 500, "/work")
	if !probed || pane != "w3:p1" {
		t.Errorf("pane=%q probed=%v, want w3:p1 true", pane, probed)
	}
}

// "Nothing answered" and "answered, no match" must not look alike: the first is
// not evidence that the process has no pane. This is #1485's distinction, which
// this file inherits because the same consumers overwrite a known launcher.
func TestHerdrPaneForPIDUnansweredSocketIsNotEvidence(t *testing.T) {
	dir := shortTempDir(t)
	sessionDir := filepath.Join(dir, "dead")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// A socket file with nothing listening — what a stopped herdr leaves.
	if err := os.WriteFile(filepath.Join(sessionDir, "herdr.sock"), nil, 0o600); err != nil {
		t.Fatalf("write stale socket: %v", err)
	}
	useSessionsDir(t, dir)

	pane, _, probed := herdrPaneForPID(context.Background(), 999, "/work")
	if pane != "" {
		t.Errorf("pane = %q, want empty", pane)
	}
	if probed {
		t.Error("probed = true, but no socket answered — a failed probe is not evidence of absence")
	}
}

// A session that answered and genuinely has no pane must report probed=true,
// because that IS evidence, unlike the case above.
func TestHerdrPaneForPIDAnsweredWithNoMatchIsEvidence(t *testing.T) {
	dir := shortTempDir(t)
	f := &fakeHerdr{t: t, panes: map[string]fakePane{
		"w4:p1": {cwd: "/work", foregroundCWD: "/work", shellPID: 600, foreground: []int{601}},
	}}
	f.start(dir, "factory")
	useSessionsDir(t, dir)

	pane, _, probed := herdrPaneForPID(context.Background(), 77777, "/work")
	if pane != "" {
		t.Errorf("pane = %q, want empty", pane)
	}
	if !probed {
		t.Error("probed = false, but the socket answered — that is evidence of absence")
	}
}

// The cwd filter is what keeps the cost of this bounded. Without it every pane
// on the server would be asked for its process list.
func TestHerdrPaneForPIDAsksOnlyAboutMatchingDirectories(t *testing.T) {
	dir := shortTempDir(t)
	panes := map[string]fakePane{}
	for i := range 12 {
		id := string(rune('a'+i)) + ":p1"
		panes[id] = fakePane{cwd: "/elsewhere", foregroundCWD: "/elsewhere", shellPID: 1000 + i, foreground: []int{2000 + i}}
	}
	panes["z:p1"] = fakePane{cwd: "/target", foregroundCWD: "/target", shellPID: 900, foreground: []int{901}}
	f := &fakeHerdr{t: t, panes: panes}
	f.start(dir, "factory")
	useSessionsDir(t, dir)

	pane, _, _ := herdrPaneForPID(context.Background(), 901, "/target")
	if pane != "z:p1" {
		t.Fatalf("pane = %q, want z:p1", pane)
	}
	if got := f.methodCalls("pane.process_info"); got != 1 {
		t.Errorf("pane.process_info called %d times, want 1 — the cwd filter should have excluded the other 12 panes", got)
	}
}

func TestCandidatePanesCapsUnfilterableInput(t *testing.T) {
	var panes []herdrPane
	for i := range maxPaneCandidates + 5 {
		panes = append(panes, herdrPane{PaneID: string(rune('a' + i)), CWD: "/same", ForegroundCWD: "/same"})
	}
	if got := len(candidatePanes(panes, "/same")); got != maxPaneCandidates {
		t.Errorf("candidates = %d, want the cap %d", got, maxPaneCandidates)
	}
	// An unknown cwd disables the filter but not the cap.
	if got := len(candidatePanes(panes, "")); got != maxPaneCandidates {
		t.Errorf("candidates with empty cwd = %d, want the cap %d", got, maxPaneCandidates)
	}
}

// The environment is the more direct evidence, so a launcher that already
// carries a pane must not be second-guessed — and must cost nothing.
func TestAdoptHerdrPaneLeavesAKnownPaneAlone(t *testing.T) {
	dir := shortTempDir(t)
	f := &fakeHerdr{t: t, panes: map[string]fakePane{
		"w5:p1": {cwd: "/work", foregroundCWD: "/work", shellPID: 700, foreground: []int{701}},
	}}
	f.start(dir, "factory")
	useSessionsDir(t, dir)

	l := &session.Launcher{HerdrPaneID: "from:env", HerdrSocketPath: "/env/sock"}
	adoptHerdrPane(l, 701)

	if l.HerdrPaneID != "from:env" || l.HerdrSocketPath != "/env/sock" {
		t.Errorf("launcher was overwritten: %+v", l)
	}
	if got := f.methodCalls("pane.list"); got != 0 {
		t.Errorf("pane.list called %d times for a launcher that already had a pane, want 0", got)
	}
}

func TestAdoptHerdrPaneTolerNilLauncher(t *testing.T) {
	useSessionsDir(t, shortTempDir(t))
	adoptHerdrPane(nil, 1) // must not panic
}
