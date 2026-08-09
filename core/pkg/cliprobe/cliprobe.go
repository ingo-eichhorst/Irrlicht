// Package cliprobe asks an installed agent CLI what version it is, by running
// it (issue #1365).
//
// It is split from core/pkg/cliversion — which only parses and compares — so
// that core/domain/agent can import the comparison without dragging os/exec
// into the domain's dependency graph. Deciding whether a version is new enough
// is domain logic; going and finding out what version is installed is not.
package cliprobe

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"irrlicht/core/pkg/cliversion"
	"irrlicht/core/pkg/pathutil"
)

// Timeout bounds how long a version probe may take. A version check runs on
// the consent path — the user has just clicked "grant" and is watching — so a
// CLI that hangs must lose quickly rather than wedge the grant.
const Timeout = 2 * time.Second

// waitDelay bounds the extra time Probe waits for the child's output pipes to
// close after its context expires. Killing the process is not enough on its
// own: exec waits for stdout to reach EOF, and a grandchild that inherited the
// pipe holds it open after its parent dies, so a plain CommandContext kill can
// still block indefinitely. This is the difference between "fails fast" as an
// intention and as a guarantee.
const waitDelay = 500 * time.Millisecond

// maxOutput caps how much of the child's stdout is read. Timeout bounds how
// long a misbehaving CLI gets, not how much it can write in that time — a
// process streaming to a pipe moves gigabytes in two seconds, and this runs
// inside a long-lived daemon. A version banner is a few hundred bytes.
const maxOutput = 64 << 10

// Probe runs argv and returns the first version triple it prints on stdout.
//
// Every way an external binary can fail to answer — missing, hung, flooding,
// unintelligible, non-zero exit — collapses to an error, which callers turn
// into "unknown", which cliversion.AtLeast fails open on.
//
//   - Missing binary: the lookup fails before anything is spawned, so this
//     returns immediately rather than after Timeout.
//   - Hung binary: bounded by Timeout, and by waitDelay against a grandchild
//     holding the output pipe open past the kill.
//   - Flooding binary: bounded by maxOutput.
//   - Unintelligible output: reported as an error here, unknown by Parse.
//
// argv[0] is resolved through pathutil rather than the inherited PATH, so a
// directory an unprivileged local attacker prepended to PATH cannot decide
// which binary the daemon executes (SonarQube go:S4036); pathutil falls back
// to a plain PATH lookup when the trusted directories don't have it, which is
// the common case for an agent CLI installed under the user's home. argv
// itself is a compile-time literal in an adapter's declaration and is passed
// as separate arguments with no shell in between.
func Probe(ctx context.Context, argv []string) (string, error) {
	if len(argv) == 0 {
		return "", errors.New("cliprobe: no probe command declared")
	}
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, pathutil.MustResolve(argv[0]), argv[1:]...)
	cmd.WaitDelay = waitDelay
	// A version probe must never block on a prompt or inherit the daemon's
	// stdin, and stderr is discarded so a chatty CLI cannot buffer through it.
	cmd.Stdin = nil

	var out cappedWriter
	out.limit = maxOutput
	cmd.Stdout = &out

	runErr := cmd.Run()

	version, ok := cliversion.Parse(out.String())
	if !ok {
		if runErr != nil {
			return "", fmt.Errorf("cliprobe: probing %q: %w", strings.Join(argv, " "), runErr)
		}
		return "", fmt.Errorf("cliprobe: %q printed no recognizable version", strings.Join(argv, " "))
	}
	// A CLI that printed a usable version and then failed has still answered
	// the only question asked, but a non-zero exit is the stronger signal that
	// something is wrong with the install — prefer it.
	if runErr != nil {
		return "", fmt.Errorf("cliprobe: probing %q: %w", strings.Join(argv, " "), runErr)
	}
	return version.String(), nil
}

// cappedWriter keeps at most limit bytes and silently discards the rest. It
// never returns an error: exec's output-copying goroutine treats a write error
// as fatal to the command, and a CLI whose banner happens to be followed by
// noise has still told us its version. Bounding memory is the whole job here —
// the process itself is bounded by Timeout.
type cappedWriter struct {
	limit int
	buf   []byte
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	if room := w.limit - len(w.buf); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		w.buf = append(w.buf, p[:room]...)
	}
	return len(p), nil
}

func (w *cappedWriter) String() string { return string(w.buf) }
