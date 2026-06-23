//go:build linux

package processlifecycle

import (
	"fmt"
	"os"
	"strings"
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
func processTTY(pid int) string { return "" }

// resolveTermProgramFromAncestry / resolveHostFromAncestry are darwin-only
// fallbacks for hardened-runtime processes that hide env from sysctl. Linux
// reads /proc/<pid>/environ directly, so these stubs are unused.
func resolveTermProgramFromAncestry(pid int) string                       { return "" }
func resolveHostFromAncestry(pid int) (term string, host int)             { return "", 0 }
func resolveHostBundleIDFromAncestry(pid int) (bundleID string, host int) { return "", 0 }

// Stubs for the kitty "no readable env" enrichment helpers. Linux can read
// /proc/<pid>/environ for any process the user owns, so the back-fill path
// these support isn't needed here.
func kittyAncestryPID(pid int) int                             { return 0 }
func kittyListenOnFor(kittyPID int) string                     { return "" }
func kittyWindowIDForPID(socket string, sessionPID int) string { return "" }
