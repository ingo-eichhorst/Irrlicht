package viewer

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// StartIsolatedDaemon spawns a SECOND viewer process on a free port,
// running with --auto-play set to the chosen scenario. The user opens
// its dashboard directly. The child process is fully isolated from the
// parent (separate state, separate broadcaster) so a hung playback
// doesn't take the maintainer's inspector down with it.
//
// This is intentionally not an `irrlichd --replay` flow — that would
// require deep surgery in `core/` (replacing the fswatcher/process
// detector with a synthetic events feed). Spawning the viewer reuses
// the exact same dashboard + WebSocket + PushBroadcaster machinery
// we already have in Mode A, but in a child process with a separate
// port. The user gets identical UI fidelity in both modes; what
// differs is process isolation, not behavior.
func (m *PlaybackManager) StartIsolatedDaemon(agent, subtree, scenario string, speed float64) (*Playback, error) {
	if !slugRE.MatchString(agent) || !slugRE.MatchString(scenario) {
		return nil, fmt.Errorf("invalid agent or scenario id")
	}
	if subtree != "scenarios" && subtree != "regression" {
		return nil, fmt.Errorf("subtree must be 'scenarios' or 'regression'")
	}
	eventsPath := filepath.Join(m.repoRoot, "replaydata", "agents", agent, subtree, scenario, "events.jsonl")
	if _, err := os.Stat(eventsPath); err != nil {
		return nil, fmt.Errorf("events.jsonl not found: %w", err)
	}
	port, err := pickFreePort()
	if err != nil {
		return nil, fmt.Errorf("pick port: %w", err)
	}

	binPath, err := findViewerBinary(m.repoRoot)
	if err != nil {
		return nil, err
	}

	autoPlay := fmt.Sprintf("%s/%s/%s", agent, subtree, scenario)
	cmd := exec.Command(binPath,
		"--repo-root", m.repoRoot,
		"--addr", "127.0.0.1:"+strconv.Itoa(port),
		"--auto-play", autoPlay,
		"--speed", strconv.FormatFloat(speed, 'f', -1, 64),
	)
	// Inherit stderr so the child's logs surface in the parent's terminal.
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start viewer subprocess: %w", err)
	}

	// Wait up to 5s for the child to bind its port.
	if err := waitForPort(port, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
		return nil, fmt.Errorf("isolated viewer did not come up on :%d within 5s: %w", port, err)
	}

	m.stopCurrent()

	pb := &Playback{
		ID:           newPlaybackID(),
		Agent:        agent,
		Subtree:      subtree,
		Scenario:     scenario,
		Mode:         "isolated-daemon",
		Speed:        speed,
		StartedAt:    time.Now().UTC(),
		broadcaster:  m.broadcaster,
		DaemonPort:   port,
		DashboardURL: fmt.Sprintf("http://127.0.0.1:%d/dashboard", port),
	}
	ctx, cancel := context.WithCancel(context.Background())
	pb.cancel = cancel
	go func() {
		<-ctx.Done()
		_ = cmd.Process.Signal(os.Interrupt)
		done := make(chan struct{})
		go func() { _, _ = cmd.Process.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	}()

	m.mu.Lock()
	m.current = pb
	m.mu.Unlock()
	return pb, nil
}

// pickFreePort asks the OS for an ephemeral port. Race window between
// release and the daemon binding is tiny; if it bites we'll retry.
func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitForPort polls every 100ms until a TCP connection succeeds.
func waitForPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			c.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("timeout")
}

// findViewerBinary locates the agent-onboard viewer binary the
// daemon-launcher spawns. Resolution order:
//
//  1. $AGENT_ONBOARD_VIEWER (explicit override for tests)
//  2. os.Executable() — when the running process IS the viewer, the
//     subprocess can be the same binary. This is the common case.
//  3. .build/agent-viewer in the repo
//  4. go build on demand into .build/agent-viewer
//
// The subprocess flow is identical between (2) and (3); reusing the
// running binary avoids a rebuild when nothing changed.
func findViewerBinary(repoRoot string) (string, error) {
	if p := os.Getenv("AGENT_ONBOARD_VIEWER"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	if exe, err := os.Executable(); err == nil {
		if _, err := os.Stat(exe); err == nil {
			return exe, nil
		}
	}
	built := filepath.Join(repoRoot, ".build", "agent-viewer")
	if _, err := os.Stat(built); err == nil {
		return built, nil
	}
	buildCmd := exec.Command("go", "build", "-o", built, "./tools/agent-onboarding/cmd/viewer")
	buildCmd.Dir = repoRoot
	buildCmd.Stderr = os.Stderr
	if err := buildCmd.Run(); err != nil {
		return "", fmt.Errorf("build viewer: %w", err)
	}
	return built, nil
}

// _ helper alias so the http import is exercised even if no handler in
// this file uses it (some refactors temporarily strip references).
var _ = http.StatusOK
