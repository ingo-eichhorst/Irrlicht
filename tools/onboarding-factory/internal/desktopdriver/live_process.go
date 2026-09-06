package desktopdriver

// Process observation: the census that proves a PID is ours, and the bounded
// wait for the owned process to exit.

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// validateOwnedProcessBaseline is the process-identity guard used before an
// Irrlicht observation can be attributed to the newly created Desktop session.
func validateOwnedProcessBaseline(baseline map[int]struct{}, candidate SessionObservation) error {
	if candidate.PID <= 0 {
		return errors.New("Irrlicht session has no live PID")
	}
	if _, existed := baseline[candidate.PID]; existed {
		return fmt.Errorf("Irrlicht session reused baseline process PID %d", candidate.PID)
	}
	return nil
}

func (runtime *LiveRuntime) WaitProcessExit(ctx context.Context, owned OwnedSession) error {
	pid := runtime.processes[owned.Registry.SessionID]
	sessionID := owned.Transcript.SessionID
	if sessionID == "" {
		sessionID = owned.Registry.CLISessionID
	}
	if pid == 0 {
		if sessionID == "" {
			return nil
		}
		return fmt.Errorf("owned Claude process for session %q was never observed", sessionID)
	}
	return poll(ctx, "owned Claude process exit", func() (bool, error) {
		exists, err := runtime.processExists(pid)
		return !exists, err
	})
}

func liveProcessExists(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if err == nil || errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return false, err
}

func processCommand(ctx context.Context, pid int) (string, error) {
	output, err := exec.CommandContext(ctx, "/bin/ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return "", fmt.Errorf("read command for PID %d: %w", pid, err)
	}
	command := strings.TrimSpace(string(output))
	if command == "" {
		return "", fmt.Errorf("PID %d has an empty command", pid)
	}
	return command, nil
}

func readProcessCensus(ctx context.Context) (map[int]struct{}, error) {
	command := exec.CommandContext(ctx, "/bin/ps", "-axo", "pid=")
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(stdout, maxProcessCensusBytes+1))
	if readErr != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("read process census: %w", readErr)
	}
	if len(data) > maxProcessCensusBytes {
		_ = command.Process.Kill()
		_ = command.Wait()
		return nil, fmt.Errorf("process census exceeded %d bytes", maxProcessCensusBytes)
	}
	if err := command.Wait(); err != nil {
		return nil, fmt.Errorf("run process census: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	processes := map[int]struct{}{}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		if len(processes) >= maxProcessCensusRows {
			return nil, fmt.Errorf("process census exceeded %d rows", maxProcessCensusRows)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if err != nil || pid <= 0 {
			return nil, fmt.Errorf("process census contains invalid PID %q", scanner.Text())
		}
		processes[pid] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return processes, nil
}
