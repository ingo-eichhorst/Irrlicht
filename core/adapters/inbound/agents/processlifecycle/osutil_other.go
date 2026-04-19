//go:build !darwin && !linux

package processlifecycle

// readProcessEnv is not implemented on this platform — launcher capture
// is disabled and the menu-bar app falls back to Finder-reveal of cwd.
func readProcessEnv(pid int) (map[string]string, error) {
	return nil, nil
}

// resolveTermProgramFromAncestry is a darwin-only fallback; other platforms
// return "" and keep whatever the env-based path produced.
func resolveTermProgramFromAncestry(pid int) string { return "" }
