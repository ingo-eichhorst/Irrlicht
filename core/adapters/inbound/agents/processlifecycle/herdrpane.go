// herdrpane.go resolves a herdr pane for an agent process whose own
// environment cannot be read (#1934).
//
// launcherFromEnv takes HERDR_PANE_ID from the agent process's environment,
// which on darwin comes from sysctl(kern.procargs2). A Node-based agent that
// sets process.title overwrites the contiguous argv+env region that call
// exposes, so the read comes back empty and the session gets no pane — while
// the same session's state, cwd and adapter are observed normally. Measured on
// herdr 0.8.0 with pi (a `/usr/bin/env node` script): `ps eww` shows 0
// environment variables for the pi process and a full environment for a
// compiled agent (claude, opencode) in a neighbouring pane.
//
// The ancestry fallback cannot recover it either. Walking up from such a
// process reaches the pane's shell, whose environment is equally unreadable,
// and then the herdr *server*, which knows the session but not the pane.
//
// So the pane is asked for, from the one process that certainly knows it.
// herdr's control socket speaks the same newline-delimited JSON-RPC its own
// pi extension uses (~/.pi/agent/extensions/herdr-agent-state.ts sends
// {"id","method":"pane.report_agent","params":{...}} over HERDR_SOCKET_PATH),
// and answers `pane.list` and `pane.process_info` on the same connection.
//
// This is a socket read rather than a shellout on purpose: it needs no herdr
// binary on the daemon's PATH, and it does not consume the probe budget that
// probecount.go accounts for.
package processlifecycle

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"time"

	"irrlicht/core/domain/session"
)

// herdrSessionsDirFn locates herdr's per-session state directories. A variable
// so tests can point it at a fixture tree; production takes the documented
// layout, which herdrClientLogPath already assumes elsewhere in this package.
var herdrSessionsDirFn = defaultHerdrSessionsDir

func defaultHerdrSessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "herdr", "sessions")
}

// herdrDialTimeout bounds one request end to end. The socket is local and the
// server answers from memory, so this is a liveness bound rather than a
// latency budget: a herdr that has stopped leaves its socket file behind, and
// without a deadline the connect would block on it.
const herdrDialTimeout = 2 * time.Second

// maxPaneCandidates bounds how many panes are asked for their process list
// after the cwd filter has narrowed them.
//
// The filter is what makes this small: `pane.list` returns every pane on the
// server (19 on the machine where #1934 was measured), and the ones sharing a
// working directory with the session are typically one to three. The cap is
// the guard for the case the filter cannot narrow — many panes opened in one
// directory — and its cost when it truncates is a session that keeps no pane,
// which is exactly today's behaviour.
//
// cwd is used ONLY to choose which panes to ask about. It is never the answer:
// two sessions can share a working directory, so the pane is decided by an
// exact pid match against pane.process_info. That distinction is the whole
// reason this resolves rather than guesses.
const maxPaneCandidates = 8

// herdrPane is the subset of herdr's `pane.list` entries this file reads.
type herdrPane struct {
	PaneID        string `json:"pane_id"`
	CWD           string `json:"cwd"`
	ForegroundCWD string `json:"foreground_cwd"`
}

// herdrProcessInfo is the subset of `pane.process_info` this file reads.
type herdrProcessInfo struct {
	ShellPID            int `json:"shell_pid"`
	ForegroundProcesses []struct {
		PID int `json:"pid"`
	} `json:"foreground_processes"`
}

// herdrPaneForPID returns the pane displaying pid and the socket it was found
// on, plus whether the lookup actually ran.
//
// The third return is #1485's distinction applied here: "no herdr is running"
// and "herdr is running and this process is not in one of its panes" are both
// legitimate no-answers, but "the socket refused us" is not evidence of
// either, and a caller that overwrites a known launcher must be able to tell
// them apart. probed is true only when at least one socket answered.
func herdrPaneForPID(ctx context.Context, pid int, cwd string) (paneID, socketPath string, probed bool) {
	if pid <= 0 {
		return "", "", false
	}
	for _, sock := range herdrSocketPaths() {
		panes, ok := herdrPanes(ctx, sock)
		if !ok {
			// This socket did not answer. Others still might, so keep going
			// rather than reporting a failed probe for the whole lookup.
			continue
		}
		probed = true
		for _, pane := range candidatePanes(panes, cwd) {
			info, ok := herdrPaneProcessInfo(ctx, sock, pane.PaneID)
			if !ok {
				continue
			}
			if processInfoNames(info, pid) {
				return pane.PaneID, sock, true
			}
		}
	}
	return "", "", probed
}

// herdrSocketPaths lists the control sockets of every herdr server with state
// on this machine, in a stable order.
//
// Sorted because filepath.Glob's order is documented as sorted and the result
// feeds a first-match loop: an unstable order would make which pane is found
// depend on directory iteration when two servers somehow claim one pid, and a
// non-deterministic answer is worse than a wrong one because it cannot be
// reproduced from a bug report.
func herdrSocketPaths() []string {
	dir := herdrSessionsDirFn()
	if dir == "" {
		return nil
	}
	matches, err := filepath.Glob(filepath.Join(dir, "*", "herdr.sock"))
	if err != nil {
		// Only ErrBadPattern, which the literal above cannot produce.
		return nil
	}
	return matches
}

// candidatePanes narrows the panes worth asking about to those whose working
// directory matches the session's, capped at maxPaneCandidates.
//
// Both cwd fields are compared because they answer different questions and
// either can be the match: `cwd` is where the pane's shell sits, and
// `foreground_cwd` is where its running process does. They agree for an agent
// started in place and diverge when the agent was started after a cd.
//
// An empty cwd disables the filter rather than matching nothing: a session
// whose directory Irrlicht does not know is exactly the case where asking a
// bounded number of panes is still better than giving up, and the pid match
// downstream is what decides either way.
func candidatePanes(panes []herdrPane, cwd string) []herdrPane {
	var out []herdrPane
	for _, p := range panes {
		if cwd != "" && p.CWD != cwd && p.ForegroundCWD != cwd {
			continue
		}
		out = append(out, p)
		if len(out) == maxPaneCandidates {
			break
		}
	}
	return out
}

// processInfoNames reports whether pid is the pane's foreground process or its
// shell.
//
// The shell is included because a pane whose agent has exited leaves the shell
// in the foreground, and a session observed in that instant still belongs to
// that pane. Matching only the foreground process would drop the pane exactly
// while the session is ending, which is when its identity is most likely to be
// read.
func processInfoNames(info herdrProcessInfo, pid int) bool {
	if info.ShellPID == pid {
		return true
	}
	for _, p := range info.ForegroundProcesses {
		if p.PID == pid {
			return true
		}
	}
	return false
}

func herdrPanes(ctx context.Context, socketPath string) ([]herdrPane, bool) {
	var payload struct {
		Panes []herdrPane `json:"panes"`
	}
	if !herdrRequest(ctx, socketPath, "pane.list", nil, &payload) {
		return nil, false
	}
	return payload.Panes, true
}

func herdrPaneProcessInfo(ctx context.Context, socketPath, paneID string) (herdrProcessInfo, bool) {
	var payload struct {
		ProcessInfo herdrProcessInfo `json:"process_info"`
	}
	params := map[string]any{"pane_id": paneID}
	if !herdrRequest(ctx, socketPath, "pane.process_info", params, &payload) {
		return herdrProcessInfo{}, false
	}
	return payload.ProcessInfo, true
}

// herdrRequest sends one JSON-RPC request and decodes the `result` member of
// the single line that comes back.
//
// Returns false for every failure — no such socket, nothing listening, a
// timeout, a protocol error, an error member instead of a result. The caller
// distinguishes only "answered" from "did not", because none of those failures
// is evidence about which pane a process is in.
func herdrRequest(ctx context.Context, socketPath, method string, params map[string]any, out any) bool {
	deadline := time.Now().Add(herdrDialTimeout)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(deadline) {
		deadline = ctxDeadline
	}

	dialer := net.Dialer{Deadline: deadline}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return false
	}
	defer func() { _ = conn.Close() }()
	if err := conn.SetDeadline(deadline); err != nil {
		return false
	}

	request := map[string]any{"id": "irrlicht:" + method, "method": method}
	if params != nil {
		request["params"] = params
	}
	body, err := json.Marshal(request)
	if err != nil {
		return false
	}
	if _, err := conn.Write(append(body, '\n')); err != nil {
		return false
	}

	// One request, one response line. A streaming decoder rather than a
	// read-to-EOF because the server keeps the connection open for further
	// requests, so EOF would only arrive at the deadline.
	var envelope struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.NewDecoder(conn).Decode(&envelope); err != nil {
		return false
	}
	if len(envelope.Result) == 0 {
		return false
	}
	return json.Unmarshal(envelope.Result, out) == nil
}

// adoptHerdrPane fills in a launcher's herdr pane when the environment read
// could not, leaving everything else alone.
//
// A no-op unless the pane is genuinely missing, so a session whose environment
// WAS readable never pays for this and never has its own answer second-guessed:
// the environment is the more direct evidence, and this is the fallback.
//
// The socket path is adopted alongside the pane id because they are one fact.
// clientHostFor needs both to reach the herdr client, and a pane id without its
// socket would name the pane while leaving #1350's focus path unable to use it.
func adoptHerdrPane(l *session.Launcher, pid int) {
	if l == nil || l.HerdrPaneID != "" {
		return
	}
	pane, socketPath, probed := herdrPaneForPID(context.Background(), pid, cwdForPaneMatch(pid))
	if !probed || pane == "" {
		return
	}
	l.HerdrPaneID = pane
	l.HerdrSocketPath = socketPath
}

// cwdForPaneMatch reads the process's working directory to narrow which panes
// are asked about, and returns "" when it cannot.
//
// "" is not a failure here: candidatePanes treats it as "do not filter" and
// falls back to the bounded scan, which still decides by pid. Reading through
// the osProc seam rather than shelling out keeps this off the probe budget,
// matching how discovery.go reads a pid's directory.
func cwdForPaneMatch(pid int) string {
	dir, err := osProc.CWDOf(pid)
	if err != nil {
		return ""
	}
	return dir
}
