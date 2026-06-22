// Package control implements outbound.AgentController: it writes input and
// interrupts back into a discovered agent session by scripting whatever
// terminal backend owns the session's pty (issue #724, the "backchannel").
// The daemon never owns the agent process — it targets the backend using the
// session's already-captured session.Launcher (populated by the Focus feature).
//
// Phase 1 covers the CLI backends that need no GUI/TCC and work headless and
// over the relay: tmux (send-keys) and kitty (kitten @ send-text). AppleScript
// backends (iTerm2/Terminal.app) are reached via a push to the macOS app, which
// holds the Automation TCC grant — wired with the macOS UI work; until then a
// session hosted only by such a backend reports not-controllable.
package control

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"

	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// execTimeout bounds every backend shell-out, matching the process observer's
// 2-second ceiling (process_darwin.go).
const execTimeout = 2 * time.Second

// command is a single backend invocation. Keeping construction pure (no exec)
// makes the exact argv table-testable.
type command struct {
	name string
	args []string
}

// runner executes a command. Overridable in tests; the default shells out with
// a bounded context.
type runner func(ctx context.Context, c command) error

// Controller implements outbound.AgentController over terminal-backend scripting.
type Controller struct {
	repo   outbound.SessionRepository
	logger outbound.Logger
	run    runner
}

// NewController constructs a Controller backed by real shell-outs.
func NewController(repo outbound.SessionRepository, logger outbound.Logger) *Controller {
	return &Controller{repo: repo, logger: logger, run: execRunner}
}

// SendInput injects data into the session's terminal as if typed.
func (c *Controller) SendInput(sessionID string, data []byte) error {
	l, err := c.launcher(sessionID)
	if err != nil {
		return err
	}
	switch resolveBackend(l) {
	case backendTmux:
		return c.exec(tmuxInput(l, data))
	case backendKitty:
		return c.exec(kittyInput(l, data))
	default:
		return fmt.Errorf("control: session %s has no controllable backend", sessionID)
	}
}

// Interrupt delivers an interrupt (Ctrl-C) to the session.
func (c *Controller) Interrupt(sessionID string) error {
	l, err := c.launcher(sessionID)
	if err != nil {
		return err
	}
	switch resolveBackend(l) {
	case backendTmux:
		return c.exec(tmuxInterrupt(l))
	case backendKitty:
		return c.exec(kittyInterrupt(l))
	default:
		return fmt.Errorf("control: session %s has no controllable backend", sessionID)
	}
}

// Controllable reports whether the session has a usable CLI-backend target.
func (c *Controller) Controllable(sessionID string) bool {
	l, err := c.launcher(sessionID)
	if err != nil {
		return false
	}
	return resolveBackend(l) != backendNone
}

func (c *Controller) launcher(sessionID string) (*session.Launcher, error) {
	state, err := c.repo.Load(sessionID)
	if err != nil {
		return nil, fmt.Errorf("control: load session %s: %w", sessionID, err)
	}
	return state.Launcher, nil
}

func (c *Controller) exec(cmd command) error {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	return c.run(ctx, cmd)
}

// execRunner is the production runner: a bounded shell-out that surfaces
// stderr in the error so a misconfigured backend (e.g. kitty remote control
// off) is diagnosable.
func execRunner(ctx context.Context, c command) error {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.name, c.args...)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("%s: %w: %s", c.name, err, stderr.String())
		}
		return fmt.Errorf("%s: %w", c.name, err)
	}
	return nil
}

// backend identifies which terminal backend hosts a session, derived from its
// Launcher fields.
type backend int

const (
	backendNone backend = iota
	backendTmux
	backendKitty
)

// resolveBackend picks the backend to script for a launcher. tmux wins when a
// pane is known (it is the most robust, TCC-free, relay-reachable target);
// kitty is used when its remote-control socket and window id are both present.
func resolveBackend(l *session.Launcher) backend {
	if l == nil {
		return backendNone
	}
	if l.TmuxPane != "" {
		return backendTmux
	}
	if l.KittyListenOn != "" && l.KittyWindowID != "" {
		return backendKitty
	}
	return backendNone
}

// tmuxInput builds the send-keys command that types data into the pane. `-l`
// sends the bytes literally (no key-name lookup); `--` ends option parsing so
// data starting with `-` is safe. A trailing CR (0x0d) in data submits.
func tmuxInput(l *session.Launcher, data []byte) command {
	args := tmuxBase(l)
	args = append(args, "send-keys", "-t", l.TmuxPane, "-l", "--", string(data))
	return command{name: "tmux", args: args}
}

// tmuxInterrupt sends the C-c key (interpreted, not literal) to the pane.
func tmuxInterrupt(l *session.Launcher) command {
	args := tmuxBase(l)
	args = append(args, "send-keys", "-t", l.TmuxPane, "C-c")
	return command{name: "tmux", args: args}
}

// tmuxBase prepends `-S <socket>` when the session's tmux server socket is
// known, so we target the right server even with multiple tmux instances.
func tmuxBase(l *session.Launcher) []string {
	if l.TmuxSocket != "" {
		return []string{"-S", l.TmuxSocket}
	}
	return nil
}

// kittyInput builds the send-text command targeting the session's window over
// its remote-control socket. Requires the user's kitty to have remote control
// enabled; otherwise the command fails and surfaces as a control error.
func kittyInput(l *session.Launcher, data []byte) command {
	return command{name: "kitten", args: []string{
		"@", "--to", l.KittyListenOn,
		"send-text", "--match", "id:" + l.KittyWindowID,
		"--", string(data),
	}}
}

// kittyInterrupt sends a raw ETX (Ctrl-C) to the session's window.
func kittyInterrupt(l *session.Launcher) command {
	return command{name: "kitten", args: []string{
		"@", "--to", l.KittyListenOn,
		"send-text", "--match", "id:" + l.KittyWindowID,
		"--", "\x03",
	}}
}
