package desktopdriver

import (
	"context"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// A run must never be the thing that starts Claude Desktop.
//
// The rig activates the app before every run. When the app is already running
// that is harmless; when it is NOT, the call launches it — from the driving
// shell, with the driving shell's environment. Claude Desktop then hands that
// environment to every Claude Code process it spawns, Irrlicht reads
// TERM_PROGRAM out of it, and the session is attributed to a terminal instead
// of to Claude Desktop. The driver refuses such a session, correctly, twelve
// minutes later.
//
// Measured 2026-09-07 while recording cell 2-8: three runs drove their whole
// recipe and then timed out, against a Claude Desktop the rig itself had
// started, carrying CLAUDECODE=1, CLAUDE_CODE_SSE_PORT and TERM_PROGRAM=vscode.
// The daemon reported launcher={"term_program":"vscode"}, host_bundle_id=null.
// Full write-up: replaydata/agents/claudecode/desktop-evidence/launcher-attribution.md
//
// This check turns those twelve wasted minutes into a refusal before the first
// keystroke, and it names what to do about it.
var contaminatingVariables = []string{
	// Irrlicht's launcher attribution reads this one. It is the variable that
	// actually breaks a run.
	"TERM_PROGRAM",
	// These two prove the environment came from a Claude Code session rather
	// than from a terminal the operator happened to use. They are checked so
	// the refusal can say WHOSE environment it found, which is the difference
	// between "relaunch the app" and "the rig did this to itself".
	"CLAUDECODE",
	"CLAUDE_CODE_SSE_PORT",
}

// desktopProcessID returns the PID of the running Claude Desktop application.
func desktopProcessID(ctx context.Context) (int, error) {
	output, err := exec.CommandContext(ctx, "/usr/bin/pgrep", "-x", "Claude").Output()
	if err != nil {
		return 0, fmt.Errorf("find the Claude Desktop process: %w", err)
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		return 0, fmt.Errorf("no Claude Desktop process is running")
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, fmt.Errorf("read the Claude Desktop process id: %w", err)
	}
	return pid, nil
}

// desktopEnvironmentReader returns the environment of one process, in the
// `KEY=value` form `ps -E` prints.
type desktopEnvironmentReader func(ctx context.Context, pid int) (string, error)

func readProcessEnvironment(ctx context.Context, pid int) (string, error) {
	output, err := exec.CommandContext(
		ctx, "/bin/ps", "-p", strconv.Itoa(pid), "-Eo", "command=").Output()
	if err != nil {
		return "", fmt.Errorf("read the environment of PID %d: %w", pid, err)
	}
	return string(output), nil
}

// requireUncontaminatedDesktop refuses to record against a Claude Desktop that
// carries the driving session's environment.
//
// An environment it cannot READ is a refusal too. A check that cannot look must
// not report the same thing as a check that looked and found nothing.
func requireUncontaminatedDesktop(
	ctx context.Context, pid int, read desktopEnvironmentReader,
) error {
	if pid <= 0 {
		return fmt.Errorf("cannot check the Claude Desktop environment: no process id (%d)", pid)
	}
	environment, err := read(ctx, pid)
	if err != nil {
		return fmt.Errorf("cannot check whether Claude Desktop carries this session's environment: %w", err)
	}
	if strings.TrimSpace(environment) == "" {
		return fmt.Errorf(
			"cannot check whether Claude Desktop carries this session's environment: PID %d reported nothing", pid)
	}
	found := contaminationFound(environment)
	if len(found) == 0 {
		return nil
	}
	return fmt.Errorf(
		"Claude Desktop (PID %d) carries this session's environment (%s), so every Claude Code "+
			"session it spawns inherits it and Irrlicht attributes them to a terminal rather than "+
			"to Claude Desktop. The run would drive its whole recipe and then time out. A run must "+
			"never be the thing that starts Claude Desktop. Quit it and relaunch it with a scrubbed "+
			"environment, then re-run:\n"+
			"  osascript -e 'tell application \"Claude\" to quit'\n"+
			"  env -i HOME=\"$HOME\" PATH=/usr/bin:/bin:/usr/sbin:/sbin USER=\"$USER\" open -a Claude\n"+
			"Plain `open -a Claude` is NOT enough — measured 2026-09-07, it forwards the calling "+
			"shell's environment. See desktop-evidence/launcher-attribution.md",
		pid, strings.Join(found, ", "))
}

func contaminationFound(environment string) []string {
	present := map[string]struct{}{}
	for _, field := range strings.Fields(environment) {
		name, _, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		for _, watched := range contaminatingVariables {
			if name == watched {
				present[watched] = struct{}{}
			}
		}
	}
	found := make([]string, 0, len(present))
	for name := range present {
		found = append(found, name)
	}
	sort.Strings(found)
	return found
}
