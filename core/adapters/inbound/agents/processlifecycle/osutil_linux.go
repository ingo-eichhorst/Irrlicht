//go:build linux

package processlifecycle

import (
	"context"
	"fmt"
	"os"
	"strings"

	"irrlicht/core/domain/session"
)

// readProcessEnv reads /proc/<pid>/environ and returns the whitelisted
// entries. The file contains NUL-delimited KEY=VALUE entries.
func readProcessEnv(pid int) (map[string]string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, fmt.Errorf("read /proc/%d/environ: %w", pid, err)
	}
	out := map[string]string{}
	for _, entry := range strings.Split(string(data), "\x00") {
		eq := strings.IndexByte(entry, '=')
		if eq <= 0 {
			continue
		}
		key := entry[:eq]
		if _, ok := launcherEnvKeys[key]; !ok {
			continue
		}
		out[key] = entry[eq+1:]
	}
	return out, nil
}

// processTTY is darwin-only host enrichment (Terminal.app window targeting).
// Linux observation doesn't depend on it, so it degrades to "".
// probed is true: having no darwin ps to run is a settled property of this
// platform, not a read that failed (#1533) — the same reading the ancestry
// stubs below give their complete return.
func processTTY(ctx context.Context, pid int) (string, bool) { return "", true }

// resolveTermProgramFromAncestry / resolveHostFromAncestry are darwin-only
// fallbacks for hardened-runtime processes that hide env from sysctl. Linux
// reads /proc/<pid>/environ directly, so these stubs are unused.
//
// complete is true: declining to walk is a settled verdict about this
// platform, not a read that failed. It is false only where a walk was
// attempted and could not be answered (#1492), which is darwin-only.
func resolveTermProgramFromAncestry(ctx context.Context, pid int) string { return "" }

// reads is #1544's per-resolve read memo, carried in the signature so the
// cross-platform caller (ancestryProbe.host / applyAncestryFallbacksVia) has
// one spelling. Unused here for the same reason the walks themselves are: this
// platform makes no `ps` shellouts to dedup.
func resolveHostFromAncestry(ctx context.Context, pid int, reads *ancestryReads) (term string, host int, complete bool) {
	return "", 0, true
}
func resolveHostBundleIDFromAncestry(ctx context.Context, pid int, reads *ancestryReads) (bundleID string, host int, complete bool) {
	return "", 0, true
}

// newAncestryReads binds the memo to a read that refuses. Nothing on this
// platform can reach it — both walks above return before probing anything — and
// it fails loudly rather than returning a zero value, so a future walk added
// here without its own read is reported instead of silently resolving every
// process to no host.
func newAncestryReads() *ancestryReads {
	return newAncestryReadsVia(func(ctx context.Context, pid int) (int, string, error) {
		return 0, "", fmt.Errorf("ancestry walking is darwin-only: no read for pid %d", pid)
	})
}

// Stubs for the kitty "no readable env" enrichment helpers. Linux can read
// /proc/<pid>/environ for any process the user owns, so the back-fill path
// these support isn't needed here.
func kittyAncestryPID(ctx context.Context, pid int) int { return 0 }
func kittyListenOnFor(kittyPID int) string              { return "" }

// probed is true for the same reason processTTY's stub reports it: having no
// kitty remote-control CLI to run is a settled property of this platform, not
// a read that failed (#1537).
func kittyWindowIDForPID(ctx context.Context, socket string, sessionPID int) (string, bool) {
	return "", true
}

// IsKnownInteractiveHost is darwin-only (ancestry walking); other platforms
// fail open. The exclusion signal it backs (CodexBar's non-interactive `agy`
// process, issue #784) only exists on macOS, so failing closed here would
// reject every antigravity CLI session on this platform instead.
func IsKnownInteractiveHost(pid int) bool { return hostGateFor(pid).admits() }

// hostGateFor reports that this platform did not evaluate the gate, rather than
// a verdict it never reached (#1525).
//
// This is the whole of the non-darwin constraint on that issue: the stub admits
// unconditionally, so counting it as admitted.host_matched would publish an
// ancestry verdict from a daemon that never read an ancestor, and counting
// nothing at all would make a Linux bundle's three zeros indistinguishable from
// a darwin daemon whose gate was never exercised. admitted.not_evaluated is
// neither — it says the gate was consulted N times and this platform declined
// to look, which is the fact this build actually has.
func hostGateFor(pid int) hostGateOutcome { return observeHostGate(hostGateNotEvaluated) }

// herdrClientLauncher resolves a herdr pane's window through the attached
// client (#1350). Darwin-only: the identity it produces is only consumed by the
// macOS click-to-focus path, and resolving it needs the same ancestry walk the
// stubs above already decline to do.
//
// So the answer here is "not probed" (#1485), not "nothing attached": this
// platform never looks, and a caller merging the read into a stored launcher
// must not read that silence as a client having detached.
func herdrClientLauncher(socketPath string) (*session.Launcher, bool) { return nil, false }
