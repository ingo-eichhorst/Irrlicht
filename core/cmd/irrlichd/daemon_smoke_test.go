package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
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

// TestDaemonStartupSmoke boots real irrlichd daemons in child processes and
// verifies the startup surface survives every commit. It is fully hermetic
// and parallel-safe: HOME and IRRLICHT_HOME both point at fresh temp dirs
// (so it neither reads nor mutates the production install or the user's
// ~/.claude config), and IRRLICHT_BIND_ADDR=127.0.0.1:0 binds an OS-assigned
// port (so N worktrees can run it at once).
//
// Two boots:
//   - demo: IRRLICHT_DEMO_MODE=1 (watchers off; the daemon serves only
//     what's wired at startup). Asserts the addr file, GET /api/v1/agents
//     over TCP and the unix socket, and clean SIGTERM shutdown.
//   - ask: production permission mode WITHOUT demo. Asserts consent-first
//     (#570) on the path where effects could actually run — every declared
//     permission is pending and ~/.claude/settings.json was never created
//     (the pre-#570 daemon auto-created it at startup; demo mode skips the
//     install path entirely, so asserting there would prove nothing).
func TestDaemonStartupSmoke(t *testing.T) {
	// Build the daemon binary once for both boots. Done lazily here (not in
	// TestMain) so unrelated `go test -run …` invocations don't pay the cost.
	bin := filepath.Join(t.TempDir(), "irrlichd")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build irrlichd: %v\n%s", err, out)
	}

	t.Run("demo", func(t *testing.T) {
		d := bootSmokeDaemon(t, bin, "IRRLICHT_DEMO_MODE=1")

		// 1. TCP round-trip.
		assertAgentsEndpoint(t, http.DefaultClient, "http://"+d.addr+"/api/v1/agents")

		// 2. Unix socket round-trip.
		sockPath := filepath.Join(d.stateDir, "irrlichd.sock")
		unixClient := &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, "unix", sockPath)
			},
		}}
		assertAgentsEndpoint(t, unixClient, "http://unix/api/v1/agents")

		// 3. Diagnostics bundle (#736): localhost GET returns a valid .tar.gz.
		assertBundleEndpoint(t, http.DefaultClient, "http://"+d.addr+"/debug/bundle")

		unixClient.CloseIdleConnections()
		d.shutdown(t)
	})

	t.Run("ask", func(t *testing.T) {
		d := bootSmokeDaemon(t, bin)

		// Consent-first: a fresh install answers nothing, so every declared
		// permission is pending and no file under the user's home was
		// modified — in particular ~/.claude/settings.json must not exist.
		// This boot runs the REAL ask-mode startup (permService.Start), so a
		// regression re-introducing boot-time hook install is caught here.
		assertPermissionsAllPending(t, http.DefaultClient, "http://"+d.addr+"/api/v1/permissions")
		if _, err := os.Stat(filepath.Join(d.homeDir, ".claude", "settings.json")); !os.IsNotExist(err) {
			t.Errorf("~/.claude/settings.json exists on a fresh consent-first install (stat err = %v)", err)
		}

		d.shutdown(t)
	})
}

// smokeDaemon is one booted child daemon plus its teardown handles.
type smokeDaemon struct {
	cmd      *exec.Cmd
	exited   chan struct{}
	stopped  bool
	homeDir  string
	stateDir string
	addrPath string
	addr     string
}

// bootSmokeDaemon starts the built daemon with fresh HOME/IRRLICHT_HOME temp
// dirs, an ephemeral port, and any extra env entries, then waits for the
// addr file. A kill-backstop is registered via t.Cleanup.
func bootSmokeDaemon(t *testing.T, bin string, extraEnv ...string) *smokeDaemon {
	t.Helper()
	d := &smokeDaemon{
		homeDir:  t.TempDir(), // isolates ~/.claude hooks + Application Support logs
		stateDir: t.TempDir(), // IRRLICHT_HOME: socket, addr file, recordings, history
		exited:   make(chan struct{}),
	}
	d.cmd = exec.Command(bin)
	d.cmd.Env = append(os.Environ(),
		"HOME="+d.homeDir,
		"IRRLICHT_HOME="+d.stateDir,
		"IRRLICHT_BIND_ADDR=127.0.0.1:0",
	)
	d.cmd.Env = append(d.cmd.Env, extraEnv...)
	if err := d.cmd.Start(); err != nil {
		t.Fatalf("start daemon: %v", err)
	}
	// A single reaper goroutine owns cmd.Wait(); the backstop and the SIGTERM
	// path coordinate through `exited` rather than calling Wait themselves, so
	// Wait is never invoked twice (which `go test -race` flags as "Wait was
	// already called").
	go func() { _ = d.cmd.Wait(); close(d.exited) }()
	t.Cleanup(func() {
		if !d.stopped {
			_ = d.cmd.Process.Kill()
			<-d.exited
		}
	})
	dumpLogsOnFail(t, d.homeDir)

	// The daemon writes its resolved address here once listening.
	d.addrPath = filepath.Join(d.stateDir, "irrlichd.addr")
	d.addr = waitForAddr(t, d.addrPath, 5*time.Second)
	return d
}

// shutdown SIGTERMs the daemon, asserts a clean exit, and checks the addr
// file was removed. The timeout exceeds the daemon's own 5s graceful-
// shutdown budget so a clean (but not instant) shutdown isn't mistaken for
// a hang.
func (d *smokeDaemon) shutdown(t *testing.T) {
	t.Helper()
	// Drop client-side keep-alive conns so the daemon's graceful shutdown
	// isn't waiting on them.
	http.DefaultClient.CloseIdleConnections()
	if err := d.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	select {
	case <-d.exited:
	case <-time.After(6 * time.Second):
		_ = d.cmd.Process.Kill()
		<-d.exited
		d.stopped = true
		t.Fatalf("daemon did not exit within 6s of SIGTERM")
	}
	d.stopped = true
	if _, err := os.Stat(d.addrPath); !os.IsNotExist(err) {
		t.Errorf("addr file %s should be removed after shutdown, stat err = %v", d.addrPath, err)
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

// assertBundleEndpoint GETs /debug/bundle and verifies it streams a gzip+tar
// that decodes and contains version.txt — the diagnostics bundle (#736) over
// the loopback path the reporter's one-line curl uses.
func assertBundleEndpoint(t *testing.T, client *http.Client, url string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", url, resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/gzip" {
		t.Errorf("GET %s: Content-Type = %q, want application/gzip", url, ct)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatalf("bundle is not valid gzip: %v", err)
	}
	tr := tar.NewReader(gz)
	var hasVersion bool
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("bundle is not a valid tar: %v", err)
		}
		if hdr.Name == "version.txt" {
			hasVersion = true
		}
	}
	if !hasVersion {
		t.Errorf("bundle from %s missing version.txt", url)
	}
}

// assertPermissionsAllPending GETs /api/v1/permissions and checks that every
// agent with declared permissions appears and every permission is pending —
// the consent-first fresh-install state.
func assertPermissionsAllPending(t *testing.T, client *http.Client, url string) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d, want 200", url, resp.StatusCode)
	}
	var snap struct {
		Mode   string `json:"mode"`
		Agents []struct {
			Name        string `json:"name"`
			Permissions []struct {
				Key   string `json:"key"`
				State string `json:"state"`
			} `json:"permissions"`
		} `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	if snap.Mode != "ask" {
		t.Fatalf("permission mode = %q, want ask", snap.Mode)
	}
	// Every agent adapter with declared permissions, plus the three daemon-
	// wide entries wired in main.go outside agents.All(): the Gas Town
	// orchestrator, launcher-identity capture, and the kitty config patch.
	declared := 3
	for _, a := range agents.All() {
		if len(a.Permissions) > 0 {
			declared++
		}
	}
	if len(snap.Agents) != declared {
		t.Fatalf("GET %s returned %d agents, want %d", url, len(snap.Agents), declared)
	}
	for _, a := range snap.Agents {
		if len(a.Permissions) == 0 {
			t.Errorf("agent %s has no permissions in the snapshot", a.Name)
		}
		for _, p := range a.Permissions {
			if p.State != "pending" {
				t.Errorf("%s/%s state = %q, want pending on fresh install", a.Name, p.Key, p.State)
			}
		}
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
