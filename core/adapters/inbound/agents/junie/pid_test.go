package junie

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// deadPID is above any real macOS/Linux PID range (macOS pid_max is 99999;
// Linux defaults to 4194304 = 2^22), so kill(pid, 0) reports ESRCH and
// IsAlive is deterministically false.
const deadPID = 99999999

// writeSidecar drops a ~/.junie/processes/-shaped sidecar into dir using the
// real filename scheme <pid>-<session-id>-<hash>.json.
func writeSidecar(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSidecarIn_ParsesRealShape(t *testing.T) {
	dir := t.TempDir()
	// Captured from a live install (2026-08), PID swapped for a dead one.
	writeSidecar(t, dir, "99999999-session-260824-172925-13tp-7fd7cf992cf8.json",
		`{"pid":99999999,"sessionId":"session-260824-172925-13tp","projectPath":"/Users/x/Workspace/proj","startedAt":1787659627600}`)

	sc := sidecarIn(dir, "session-260824-172925-13tp")
	if sc == nil {
		t.Fatal("sidecarIn() = nil, want parsed sidecar")
	}
	if sc.PID != deadPID {
		t.Errorf("PID = %d, want %d", sc.PID, deadPID)
	}
	if sc.ProjectPath != "/Users/x/Workspace/proj" {
		t.Errorf("ProjectPath = %q", sc.ProjectPath)
	}
}

func TestSidecarIn_SkipsForeignAndBrokenFiles(t *testing.T) {
	dir := t.TempDir()
	// Another session's sidecar must never answer for this one.
	writeSidecar(t, dir, "1234-session-260824-174140-oolz-fffebedb55ca.json",
		`{"pid":1234,"sessionId":"session-260824-174140-oolz","projectPath":"/other"}`)
	// Filename names the session but the authoritative JSON body disagrees.
	writeSidecar(t, dir, "5678-session-260824-172925-13tp-aaaaaaaaaaaa.json",
		`{"pid":5678,"sessionId":"session-999999-999999-zzzz","projectPath":"/mismatch"}`)
	// Malformed JSON is skipped, not fatal.
	writeSidecar(t, dir, "9999-session-260824-172925-13tp-bbbbbbbbbbbb.json", `{"pid":`)
	// A non-positive PID is not a binding.
	writeSidecar(t, dir, "0-session-260824-172925-13tp-cccccccccccc.json",
		`{"pid":0,"sessionId":"session-260824-172925-13tp","projectPath":"/zero"}`)

	if sc := sidecarIn(dir, "session-260824-172925-13tp"); sc != nil {
		t.Errorf("sidecarIn() = %+v, want nil (no valid sidecar for this session)", sc)
	}
}

func TestSidecarIn_EmptySessionOrMissingDir(t *testing.T) {
	if sc := sidecarIn(t.TempDir(), ""); sc != nil {
		t.Errorf("empty session ID: got %+v, want nil", sc)
	}
	if sc := sidecarIn(filepath.Join(t.TempDir(), "does-not-exist"), "session-x"); sc != nil {
		t.Errorf("missing dir: got %+v, want nil", sc)
	}
}

// TestLiveJuniePID_GatesStalePIDs pins the two halves of the stale-sidecar
// gate: a dead PID fails liveness, and a LIVE PID whose command line is not a
// junie process (here: this test binary's own PID, guaranteed alive) fails
// the command-pattern check — the PID-reuse case a sidecar alone can't rule
// out.
func TestLiveJuniePID_GatesStalePIDs(t *testing.T) {
	if liveJuniePID(0) || liveJuniePID(-1) {
		t.Error("non-positive PIDs must never be live")
	}
	if liveJuniePID(deadPID) {
		t.Errorf("liveJuniePID(%d) = true, want false (no such process)", deadPID)
	}
	if liveJuniePID(os.Getpid()) {
		t.Error("liveJuniePID(own test PID) = true, want false (alive but not a junie command line)")
	}
}

// fakeJunieProcess starts a live process whose argv[0] path token matches
// processCmdPattern — the system `sleep` binary behind a symlink named
// `junie` (a symlink, not a copy, so macOS code-signing still holds) — so
// liveJuniePID accepts it exactly like a real IDE-spawned junie process
// (alive AND command-pattern match), with workDir as its working directory.
func fakeJunieProcess(t *testing.T, workDir string) int {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "junie")
	if err := os.Symlink("/bin/sleep", bin); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "60")
	cmd.Dir = workDir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	pid := cmd.Process.Pid
	if !liveJuniePID(pid) {
		t.Fatalf("fake junie process %d does not pass liveJuniePID; fix the fixture", pid)
	}
	return pid
}

// One junie IDE process serves EVERY session of its window over its lifetime
// and writes one sidecar per session, ALL naming its own PID (captured live:
// three ~/.junie/processes/ sidecars for three sequential sessions of one
// project, every one naming the same live pid 47990). Discovery must bind
// that PID only for the CURRENT session — the one whose sidecar carries the
// newest startedAt. Binding it for a finished sibling too made the sessions
// delete each other through the core's one-session-per-PID reconciliation in
// an endless loop, resetting each session's model to "unknown" and
// double-counting cost on every lap.
func TestDiscoverPID_SharedProcessBindsOnlyNewestSession(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	procDir := filepath.Join(home, ".junie", "processes")
	if err := os.MkdirAll(procDir, 0o700); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	pid := fakeJunieProcess(t, project)
	pidStr := strconv.Itoa(pid)
	writeSidecar(t, procDir, pidStr+"-session-260825-151320-19jp-c95cdd197f46.json",
		`{"pid":`+pidStr+`,"sessionId":"session-260825-151320-19jp","projectPath":"`+project+`","startedAt":1787000000000}`)
	writeSidecar(t, procDir, pidStr+"-session-260826-145443-jo2p-1a81776c70f7.json",
		`{"pid":`+pidStr+`,"sessionId":"session-260826-145443-jo2p","projectPath":"`+project+`","startedAt":1787748883685}`)

	current := filepath.Join(home, ".junie", "sessions", "session-260826-145443-jo2p", "events.jsonl")
	if got, err := DiscoverPID("", current, nil); err != nil || got != pid {
		t.Errorf("current session: DiscoverPID() = (%d, %v), want (%d, nil)", got, err, pid)
	}
	old := filepath.Join(home, ".junie", "sessions", "session-260825-151320-19jp", "events.jsonl")
	if got, err := DiscoverPID("", old, nil); err != nil || got != 0 {
		t.Errorf("finished sibling: DiscoverPID() = (%d, %v), want (0, nil) — binding a newer session's process", got, err)
	}
}

// The CWD-scan fallback needs the same ownership gate: a session whose own
// sidecar went stale (dead PID) must not steal the live process that a NEWER
// session's sidecar names — the scan finds it by shared project directory.
func TestDiscoverPID_FallbackRespectsSidecarOwnership(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	procDir := filepath.Join(home, ".junie", "processes")
	if err := os.MkdirAll(procDir, 0o700); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	pid := fakeJunieProcess(t, project)
	pidStr := strconv.Itoa(pid)
	// The old session's own sidecar names a dead PID (its process exited)...
	writeSidecar(t, procDir, "99999999-session-260825-151320-19jp-c95cdd197f46.json",
		`{"pid":99999999,"sessionId":"session-260825-151320-19jp","projectPath":"`+project+`","startedAt":1787000000000}`)
	// ...while the live process in the same project belongs to a newer session.
	writeSidecar(t, procDir, pidStr+"-session-260826-145443-jo2p-1a81776c70f7.json",
		`{"pid":`+pidStr+`,"sessionId":"session-260826-145443-jo2p","projectPath":"`+project+`","startedAt":1787748883685}`)

	old := filepath.Join(home, ".junie", "sessions", "session-260825-151320-19jp", "events.jsonl")
	if got, err := DiscoverPID("", old, nil); err != nil || got != 0 {
		t.Errorf("DiscoverPID() = (%d, %v), want (0, nil) — CWD fallback stole a sibling's process", got, err)
	}
}

// currentSessionOnPID's election over one shared PID: newest startedAt wins,
// a startedAt tie stays deterministic on the lexically-first file, malformed
// bodies are skipped, and a pid no sidecar names is unowned ("").
func TestCurrentSessionOnPID(t *testing.T) {
	dir := t.TempDir()
	writeSidecar(t, dir, "47990-session-260825-151320-19jp-c95cdd197f46.json",
		`{"pid":47990,"sessionId":"session-260825-151320-19jp","projectPath":"/p","startedAt":1787000000000}`)
	writeSidecar(t, dir, "47990-session-260826-145443-jo2p-1a81776c70f7.json",
		`{"pid":47990,"sessionId":"session-260826-145443-jo2p","projectPath":"/p","startedAt":1787748883685}`)
	writeSidecar(t, dir, "47990-session-260826-999999-zzzz-dddddddddddd.json", `{"pid":`)
	if got := currentSessionOnPID(dir, 47990); got != "session-260826-145443-jo2p" {
		t.Errorf("owner = %q, want the newest-startedAt session", got)
	}
	if got := currentSessionOnPID(dir, 12345); got != "" {
		t.Errorf("unnamed pid: owner = %q, want \"\" (unowned)", got)
	}

	tie := t.TempDir()
	writeSidecar(t, tie, "1-session-aaaa-bbbb.json",
		`{"pid":1,"sessionId":"session-a","projectPath":"/p","startedAt":5}`)
	writeSidecar(t, tie, "1-session-cccc-dddd.json",
		`{"pid":1,"sessionId":"session-c","projectPath":"/p","startedAt":5}`)
	if got := currentSessionOnPID(tie, 1); got != "session-a" {
		t.Errorf("tie: owner = %q, want session-a (lexically-first file)", got)
	}
}

// TestDiscoverPID_StaleSidecarFallsBack pins that a sidecar naming a dead PID
// is not returned: discovery falls through to the CWD/cmdline scan (which
// finds nothing for a scratch project path) instead of trusting the file.
func TestDiscoverPID_StaleSidecarFallsBack(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	procDir := filepath.Join(home, ".junie", "processes")
	if err := os.MkdirAll(procDir, 0o700); err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	writeSidecar(t, procDir, "99999999-session-260825-151320-19jp-c95cdd197f46.json",
		`{"pid":99999999,"sessionId":"session-260825-151320-19jp","projectPath":"`+project+`"}`)

	transcript := filepath.Join(home, ".junie", "sessions", "session-260825-151320-19jp", "events.jsonl")
	pid, err := DiscoverPID("", transcript, nil)
	if err != nil {
		t.Fatalf("DiscoverPID() error: %v", err)
	}
	if pid == deadPID {
		t.Fatal("DiscoverPID returned the stale sidecar PID without a liveness check")
	}
	if pid != 0 {
		t.Errorf("DiscoverPID() = %d, want 0 (no live junie process owns %s)", pid, project)
	}
}
