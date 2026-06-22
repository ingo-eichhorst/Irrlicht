package control

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// captureRunner executes a command and returns its stdout. Overridable in
// tests; the default shells out with a bounded context. Shared by both the read
// path (which keeps stdout) and the write Controller (which discards it).
type captureRunner func(ctx context.Context, c command) ([]byte, error)

// Reader implements outbound.TerminalReader: it captures the rendered terminal
// screen of a discovered agent session by scripting the same backend the write
// path (Controller) targets, keyed off the session's already-captured
// session.Launcher. This is the read counterpart to the backchannel (issue
// #732, Phase 3 of #724).
//
// Read-back is snapshot-only and multiplexer/kitty-only: tmux capture-pane and
// kitty get-text render the screen for us, so the daemon needs no VT100
// emulator. AppleScript hosts (iTerm2/Terminal.app) have no daemon-reachable
// read path, so a session hosted only by such a backend reports not-readable.
type Reader struct {
	repo    outbound.SessionRepository
	logger  outbound.Logger
	capture captureRunner
}

// NewReader constructs a Reader.
func NewReader(repo outbound.SessionRepository, logger outbound.Logger) *Reader {
	return &Reader{repo: repo, logger: logger, capture: captureRunnerExec}
}

// CaptureScreen returns the session's rendered terminal screen.
func (r *Reader) CaptureScreen(sessionID string) ([]byte, error) {
	state, err := r.repo.Load(sessionID)
	if err != nil {
		return nil, fmt.Errorf("control: load session %s: %w", sessionID, err)
	}
	l := state.Launcher
	switch resolveBackend(l) {
	case backendTmux:
		return r.run(tmuxCapture(l))
	case backendKitty:
		return r.run(kittyCapture(l))
	default:
		return nil, outbound.ErrNotReadable
	}
}

// Readable reports whether the session's backend supports read-back.
func (r *Reader) Readable(sessionID string) bool {
	state, err := r.repo.Load(sessionID)
	if err != nil {
		return false
	}
	switch resolveBackend(state.Launcher) {
	case backendTmux, backendKitty:
		return true
	default:
		return false
	}
}

func (r *Reader) run(cmd command) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	return r.capture(ctx, cmd)
}

// captureRunnerExec is the production runner: a bounded shell-out that returns
// stdout and surfaces stderr in the error so a misconfigured backend (e.g.
// kitty remote control off) is diagnosable.
func captureRunnerExec(ctx context.Context, c command) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.name, c.args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return nil, fmt.Errorf("%s: %w: %s", c.name, err, stderr.String())
		}
		return nil, fmt.Errorf("%s: %w", c.name, err)
	}
	return stdout.Bytes(), nil
}

// tmuxCapture builds the capture-pane command that prints the pane's rendered
// screen to stdout. `-p` prints to stdout; tmux does the VT100 emulation, so we
// get a clean snapshot without parsing ANSI ourselves.
func tmuxCapture(l *session.Launcher) command {
	args := tmuxBase(l)
	args = append(args, "capture-pane", "-t", l.TmuxPane, "-p")
	return command{name: "tmux", args: args}
}

// kittyCapture builds the get-text command that returns the window's rendered
// screen over kitty's remote-control socket. Requires the user's kitty to have
// remote control enabled; otherwise the command fails and surfaces as an error.
func kittyCapture(l *session.Launcher) command {
	return command{name: "kitten", args: []string{
		"@", "--to", l.KittyListenOn,
		"get-text", "--match", "id:" + l.KittyWindowID,
	}}
}
