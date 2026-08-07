// Package control implements outbound.AgentController: it writes input and
// interrupts back into a discovered agent session by scripting whatever
// terminal backend owns the session's pty (issue #724, the "backchannel").
// The daemon never owns the agent process — it targets the backend using the
// session's already-captured session.Launcher (populated by the Focus feature).
//
// The CLI backends need no GUI/TCC and work headless and over the relay:
// herdr (pane send-text), tmux (send-keys) and kitty (kitten @ send-text) —
// see cliBackends, which is the set of them. AppleScript
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
	// env holds "K=V" additions layered onto the daemon's own environment for
	// this invocation. Empty for every backend addressed purely through argv;
	// see herdrEnv for the one that isn't.
	env []string
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
	return c.write(sessionID, data, string(data))
}

// submitCR is the carriage return that submits a line on the CLI backends.
// The AppleScript hosts auto-submit, so it is appended only for the CLI ones.
const submitCR = "\r"

// SendCommand injects command text and submits it, owning the per-backend
// submit sequence so the caller (a preset rule) never has to (issue #754):
// herdr/tmux/kitty get a trailing CR appended; the AppleScript path broadcasts
// the bare command, since the macOS app's write-text/do-script auto-submits
// (and strips any trailing newline) — appending a CR there would double-submit.
func (c *Controller) SendCommand(sessionID, command string) error {
	return c.write(sessionID, []byte(command+submitCR), command)
}

// write is the shared dispatch for both typed-input entry points. cli is the
// byte sequence handed to a scripted backend — already carrying its submit CR
// when the caller wants one — and script is the text broadcast to the macOS
// app for the AppleScript hosts, which auto-submit and so must not receive it.
func (c *Controller) write(sessionID string, cli []byte, script string) error {
	state, err := c.loadState(sessionID)
	if err != nil {
		return err
	}
	l := state.Launcher
	b := resolveBackend(l)
	if be, ok := cliBackends[b]; ok {
		return c.exec(be.input(l, cli))
	}
	if b == backendAppleScript {
		c.broadcastInput(state, outbound.InputActionInput, script)
		return nil
	}
	return noBackendErr(sessionID)
}

// Interrupt delivers an interrupt (Ctrl-C) to the session.
func (c *Controller) Interrupt(sessionID string) error {
	state, err := c.loadState(sessionID)
	if err != nil {
		return err
	}
	l := state.Launcher
	b := resolveBackend(l)
	if be, ok := cliBackends[b]; ok {
		return c.exec(be.interrupt(l))
	}
	if b == backendAppleScript {
		c.broadcastInput(state, outbound.InputActionInterrupt, "")
		return nil
	}
	return noBackendErr(sessionID)
}

func noBackendErr(sessionID string) error {
	return fmt.Errorf("control: session %s has no controllable backend", sessionID)
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
	backendHerdr
	backendTmux
	backendKitty
	// backendAppleScript covers iTerm2/Terminal.app — scripted by the macOS
	// app (which has the Automation TCC grant), not the daemon, via a
	// PushTypeInputRequested message.
	backendAppleScript
)

// cliBackend groups the command builders for a backend the daemon scripts
// directly. Membership of cliBackends is what makes a backend both
// controllable and readable; the AppleScript hosts are deliberately absent,
// because they are broadcast to the macOS app rather than exec'd, and that
// asymmetry is the only real branch left in the dispatch.
type cliBackend struct {
	input     func(*session.Launcher, []byte) command
	interrupt func(*session.Launcher) command
	capture   func(*session.Launcher) command
}

var cliBackends = map[backend]cliBackend{
	backendHerdr: {herdrInput, herdrInterrupt, herdrCapture},
	backendTmux:  {tmuxInput, tmuxInterrupt, tmuxCapture},
	backendKitty: {kittyInput, kittyInterrupt, kittyCapture},
}

// resolveBackend picks the backend to script for a launcher. herdr wins
// outright when a pane is known; then tmux (TCC-free, relay-reachable); then
// kitty (its remote-control socket + window id); then the AppleScript hosts,
// which the daemon delegates to the macOS app. A session in tmux *inside*
// iTerm resolves to tmux, because tmux owns the pty.
//
// herdr is checked before tmux despite that innermost-owns-the-pty rule: the
// fields are mutually exclusive by construction, since ReadLauncherEnv gives a
// herdr pane no other identity, so the order only matters as a safe default if
// some future capture path populates both (#1348).
func resolveBackend(l *session.Launcher) backend {
	if l == nil {
		return backendNone
	}
	// Both fields required, as for kitty: a pane id alone would be addressed
	// against whatever server the daemon's own environment points at, and
	// ids like "w1:p1" exist on every server — so a missing socket would not
	// degrade to "the default session", it would degrade to typing into a
	// stranger. herdr injects the two together, so requiring both costs
	// nothing in practice.
	if l.HerdrPaneID != "" && l.HerdrSocketPath != "" {
		return backendHerdr
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

// herdrInput builds the send-text command that types data into the pane. A
// trailing CR (0x0d) in data submits, exactly as it does for tmux send-keys -l
// (verified against herdr 0.8.0).
//
// Deliberately no `--` before the text, unlike the tmux and kitty builders:
// herdr's CLI accepts a leading-dash positional as literal text, so a `--`
// would itself be typed into the pane rather than ending option parsing.
func herdrInput(l *session.Launcher, data []byte) command {
	return command{
		name: "herdr",
		args: []string{"pane", "send-text", l.HerdrPaneID, string(data)},
		env:  herdrEnv(l),
	}
}

// herdrInterrupt sends the C-c key (interpreted, not literal) to the pane.
// herdr rejects the `ctrl-c` spelling with `unsupported key`; `C-c` is the
// accepted form and matches tmux's.
func herdrInterrupt(l *session.Launcher) command {
	return command{
		name: "herdr",
		args: []string{"pane", "send-keys", l.HerdrPaneID, "C-c"},
		env:  herdrEnv(l),
	}
}

// herdrEnv targets the session's herdr server. This is the one backend whose
// addressing rides in the environment rather than in argv: herdr's CLI has no
// socket flag (`--session <name>` needs a name the default session doesn't
// have), and $HERDR_SOCKET_PATH is the documented override — verified to
// address a specific server on its own, as `tmux -S <socket>` does.
func herdrEnv(l *session.Launcher) []string {
	return []string{"HERDR_SOCKET_PATH=" + l.HerdrSocketPath}
}
