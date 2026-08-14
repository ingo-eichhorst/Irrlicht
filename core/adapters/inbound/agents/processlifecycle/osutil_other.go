//go:build !darwin && !linux

package processlifecycle

import "irrlicht/core/domain/session"

// readProcessEnv is not implemented on this platform — launcher capture
// is disabled and the menu-bar app falls back to Finder-reveal of cwd.
func readProcessEnv(pid int) (map[string]string, error) {
	return nil, nil
}

// processTTY is darwin-only host enrichment; other platforms degrade to "".
// probed is true: having no darwin ps to run is a settled property of this
// platform, not a read that failed (#1533) — the same reading the ancestry
// stubs below give their complete return.
func processTTY(pid int) (string, bool) { return "", true }

// resolveTermProgramFromAncestry / resolveHostFromAncestry are darwin-only
// fallbacks; other platforms return zero values.
//
// complete is true: declining to walk is a settled verdict about this
// platform, not a read that failed (#1492).
func resolveTermProgramFromAncestry(pid int) string { return "" }
func resolveHostFromAncestry(pid int) (term string, host int, complete bool) {
	return "", 0, true
}
func resolveHostBundleIDFromAncestry(pid int) (bundleID string, host int, complete bool) {
	return "", 0, true
}

// Stubs for the kitty "no readable env" enrichment helpers — darwin-only.
func kittyAncestryPID(pid int) int                             { return 0 }
func kittyListenOnFor(kittyPID int) string                     { return "" }
func kittyWindowIDForPID(socket string, sessionPID int) string { return "" }

// IsKnownInteractiveHost is darwin-only (ancestry walking); other platforms
// fail open. The exclusion signal it backs (CodexBar's non-interactive `agy`
// process, issue #784) only exists on macOS, so failing closed here would
// reject every antigravity CLI session on this platform instead.
func IsKnownInteractiveHost(pid int) bool { return true }

// herdrClientLauncher resolves a herdr pane's window through the attached
// client (#1350). Darwin-only: the identity it produces is only consumed by the
// macOS click-to-focus path, and resolving it needs the same ancestry walk the
// stubs above already decline to do.
//
// So the answer here is "not probed" (#1485), not "nothing attached": this
// platform never looks, and a caller merging the read into a stored launcher
// must not read that silence as a client having detached.
func herdrClientLauncher(socketPath string) (*session.Launcher, bool) { return nil, false }
