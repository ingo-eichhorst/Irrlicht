package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents"
)

// TestDaemonStartupSmoke boots a real irrlichd in a child process and verifies
// the socket protocol survives every commit. It is fully hermetic and parallel-
// safe: HOME and IRRLICHT_HOME both point at fresh temp dirs (so it neither
// reads nor mutates the production install or the user's ~/.claude config), and
// IRRLICHT_BIND_ADDR=127.0.0.1:0 binds an OS-assigned port (so N worktrees can
// run it at once). IRRLICHT_DEMO_MODE=1 keeps the file/process watchers off so
// the daemon serves only what's wired at startup — exactly the surface we want
// to smoke-test.
//
// What it asserts:
//   - the daemon publishes its resolved port to IRRLICHT_HOME/irrlichd.addr,
//   - GET /api/v1/agents over TCP returns the full adapter registry,
//   - the same endpoint works over the unix socket,
//   - SIGTERM shuts it down cleanly and removes the addr file.
func TestDaemonStartupSmoke(t *testing.T) {
	homeDir := t.TempDir()  // isolates ~/.claude hooks + Application Support logs
	stateDir := t.TempDir() // IRRLICHT_HOME: socket, addr file, recordings, history

	// Build the daemon binary. Done lazily here (not in TestMain) so unrelated
	// `go test -run …` invocations don't pay the build cost.
	bin := filepath.Join(t.TempDir(), "irrlichd")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build irrlichd: %v\n%s", err, out)
	}

	cmd := exec.Command(bin)
	cmd.Env = append(os.Environ(),
		"HOME="+homeDir,
		"IRRLICHT_HOME="+stateDir,
		"IRRLICHT_BIND_ADDR=127.0.0.1:0",
		"IRRLICHT_DEMO_MODE=1",
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	// Backstop: kill the child if anything below fails before SIGTERM.
	killed := false
	defer func() {
		if !killed {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}()
	dumpLogsOnFail(t, homeDir)

	// The daemon writes its resolved address here once listening.
	addrPath := filepath.Join(stateDir, "irrlichd.addr")
	addr := waitForAddr(t, addrPath, 5*time.Second)

	// 1. TCP round-trip.
	assertAgentsEndpoint(t, http.DefaultClient, "http://"+addr+"/api/v1/agents")

	// 2. Unix socket round-trip.
	sockPath := filepath.Join(stateDir, "irrlichd.sock")
	unixClient := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
		},
	}}
	assertAgentsEndpoint(t, unixClient, "http://unix/api/v1/agents")

	// 3. Clean shutdown on SIGTERM.
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	waitExit(t, cmd, 3*time.Second)
	killed = true
	if _, err := os.Stat(addrPath); !os.IsNotExist(err) {
		t.Errorf("addr file %s should be removed after shutdown, stat err = %v", addrPath, err)
	}
}

// waitForAddr polls until the addr file exists and is non-empty, returning its
// contents (host:port). Fails the test if it never appears.
func waitForAddr(t *testing.T, path string, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if addr := strings.TrimSpace(string(b)); addr != "" {
				return addr
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("daemon never wrote addr file %s within %s", path, timeout)
	return ""
}

// assertAgentsEndpoint GETs /api/v1/agents through the given client and checks
// the returned adapter names exactly match agents.All() — the registry the
// dashboard and Swift app rely on.
func assertAgentsEndpoint(t *testing.T, client *http.Client, url string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", url, resp.StatusCode)
	}
	var entries []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&entries); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}

	got := make([]string, len(entries))
	for i, e := range entries {
		got[i] = e.Name
	}
	want := make([]string, 0)
	for _, a := range agents.All() {
		want = append(want, a.Identity.Name)
	}
	if len(want) == 0 {
		t.Fatal("agents.All() is empty — test cannot verify the registry")
	}
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("GET %s returned %d agents %v, want %d %v", url, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("GET %s agent names = %v, want %v", url, got, want)
		}
	}
}

// waitExit fails the test if the daemon doesn't exit within timeout.
func waitExit(t *testing.T, cmd *exec.Cmd, timeout time.Duration) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
		// Exited (a non-nil error from the signal-terminated process is fine).
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		t.Fatalf("daemon did not exit within %s of SIGTERM", timeout)
	}
}

// dumpLogsOnFail registers a cleanup that prints the daemon's event log when
// the test fails, so CI failures aren't silent.
func dumpLogsOnFail(t *testing.T, homeDir string) {
	t.Helper()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		logPath := filepath.Join(homeDir, "Library", "Application Support", "Irrlicht", "logs", "events.log")
		if b, err := os.ReadFile(logPath); err == nil {
			t.Logf("daemon events.log:\n%s", b)
		}
	})
}
