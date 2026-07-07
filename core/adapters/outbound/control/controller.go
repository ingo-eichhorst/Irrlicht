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
	"context"
	"fmt"
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

// Controller implements outbound.AgentController over terminal-backend scripting.
type Controller struct {
	repo   outbound.SessionRepository
	push   outbound.PushBroadcaster
	logger outbound.Logger
	run    captureRunner
}

// NewController constructs a Controller. push is used to delegate
// AppleScript-only backends (iTerm2/Terminal.app) to the macOS app, which holds
// the Automation TCC grant the daemon lacks.
func NewController(repo outbound.SessionRepository, push outbound.PushBroadcaster, logger outbound.Logger) *Controller {
	return &Controller{repo: repo, push: push, logger: logger, run: captureRunnerExec}
}

// SendInput injects data into the session's terminal as if typed.
func (c *Controller) SendInput(sessionID string, data []byte) error {
	state, err := c.loadState(sessionID)
	if err != nil {
		return err
	}
	l := state.Launcher
	switch resolveBackend(l) {
	case backendTmux:
		return c.exec(tmuxInput(l, data))
	case backendKitty:
		return c.exec(kittyInput(l, data))
	case backendAppleScript:
		c.broadcastInput(state, outbound.InputActionInput, string(data))
		return nil
	default:
		return fmt.Errorf("control: session %s has no controllable backend", sessionID)
	}
}

// submitCR is the carriage return that submits a line on the CLI backends.
// The AppleScript hosts auto-submit, so it is appended only for tmux/kitty.
const submitCR = "\r"

// SendCommand injects command text and submits it, owning the per-backend
// submit sequence so the caller (a preset rule) never has to (issue #754):
// tmux/kitty get a trailing CR appended; the AppleScript path broadcasts the
// bare command, since the macOS app's write-text/do-script auto-submits (and
// strips any trailing newline) — appending a CR there would double-submit.
func (c *Controller) SendCommand(sessionID, command string) error {
	state, err := c.loadState(sessionID)
	if err != nil {
		return err
	}
	l := state.Launcher
	switch resolveBackend(l) {
	case backendTmux:
		return c.exec(tmuxInput(l, []byte(command+submitCR)))
	case backendKitty:
		return c.exec(kittyInput(l, []byte(command+submitCR)))
	case backendAppleScript:
		c.broadcastInput(state, outbound.InputActionInput, command)
		return nil
	default:
		return fmt.Errorf("control: session %s has no controllable backend", sessionID)
	}
}

// Interrupt delivers an interrupt (Ctrl-C) to the session.
func (c *Controller) Interrupt(sessionID string) error {
	state, err := c.loadState(sessionID)
	if err != nil {
		return err
	}
	l := state.Launcher
	switch resolveBackend(l) {
	case backendTmux:
		return c.exec(tmuxInterrupt(l))
	case backendKitty:
		return c.exec(kittyInterrupt(l))
	case backendAppleScript:
		c.broadcastInput(state, outbound.InputActionInterrupt, "")
		return nil
	default:
		return fmt.Errorf("control: session %s has no controllable backend", sessionID)
	}
}

// Controllable reports whether the session has a usable backend target.
func (c *Controller) Controllable(sessionID string) bool {
	state, err := c.loadState(sessionID)
	if err != nil {
		return false
	}
	return resolveBackend(state.Launcher) != backendNone
}

// broadcastInput asks the macOS app to perform the action on an AppleScript
// backend. Fire-and-forget, like the Focus push: the daemon can't confirm the
// app acted, and an absent app simply means nothing happens.
func (c *Controller) broadcastInput(state *session.SessionState, action, data string) {
	if c.push == nil {
		return
	}
	c.push.Broadcast(outbound.PushMessage{
		Type:    outbound.PushTypeInputRequested,
		Session: state,
		Input:   &outbound.InputRequest{Action: action, Data: data},
	})
}

func (c *Controller) loadState(sessionID string) (*session.SessionState, error) {
	state, err := c.repo.Load(sessionID)
	if err != nil {
		return nil, fmt.Errorf("control: load session %s: %w", sessionID, err)
	}
	return state, nil
}

// exec runs a write command, discarding the captured stdout the shared runner
// returns (the write path only cares whether the backend accepted the input).
func (c *Controller) exec(cmd command) error {
	ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
	defer cancel()
	_, err := c.run(ctx, cmd)
	return err
}

// backend identifies which terminal backend hosts a session, derived from its
// Launcher fields.
type backend int

const (
	backendNone backend = iota
	backendTmux
	backendKitty
	// backendAppleScript covers iTerm2/Terminal.app — scripted by the macOS
	// app (which has the Automation TCC grant), not the daemon, via a
	// PushTypeInputRequested message.
	backendAppleScript
)

// resolveBackend picks the backend to script for a launcher. tmux wins when a
// pane is known (most robust, TCC-free, relay-reachable); kitty next (its
// remote-control socket + window id); then the AppleScript hosts, which the
// daemon delegates to the macOS app. A session in tmux *inside* iTerm resolves
// to tmux, because tmux owns the pty.
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
	if isAppleScriptHost(l) {
		return backendAppleScript
	}
	return backendNone
}

// isAppleScriptHost reports whether the launcher is an iTerm2/Terminal.app
// session the macOS app can target (and thus we have the id/tty to address).
func isAppleScriptHost(l *session.Launcher) bool {
	switch l.TermProgram {
	case "iTerm.app":
		return l.ITermSessionID != ""
	case "Apple_Terminal":
		return l.TTY != ""
	}
	return false
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
