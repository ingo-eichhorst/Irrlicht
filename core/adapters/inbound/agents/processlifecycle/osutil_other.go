//go:build !darwin && !linux

package processlifecycle

// readProcessEnv is not implemented on this platform — launcher capture
// is disabled and the menu-bar app falls back to Finder-reveal of cwd.
func readProcessEnv(pid int) (map[string]string, error) {
	return nil, nil
}
