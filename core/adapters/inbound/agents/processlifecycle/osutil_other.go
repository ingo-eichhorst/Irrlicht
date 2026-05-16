//go:build !darwin && !linux

package processlifecycle

// readProcessEnv is not implemented on this platform — launcher capture
// is disabled and the menu-bar app falls back to Finder-reveal of cwd.
func readProcessEnv(pid int) (map[string]string, error) {
	return nil, nil
}

// resolveTermProgramFromAncestry / resolveHostFromAncestry are darwin-only
// fallbacks; other platforms return zero values.
func resolveTermProgramFromAncestry(pid int) string           { return "" }
func resolveHostFromAncestry(pid int) (term string, host int) { return "", 0 }

// Stubs for the kitty "no readable env" enrichment helpers — darwin-only.
func kittyAncestryPID(pid int) int                             { return 0 }
func kittyListenOnFor(kittyPID int) string                     { return "" }
func kittyWindowIDForPID(socket string, sessionPID int) string { return "" }
