// Package pathutil resolves external commands to absolute paths under a
// fixed, unwriteable set of system directories instead of the process's
// inherited PATH — which a local attacker with filesystem write access
// could have prepended a malicious directory to before this process
// started (SonarQube go:S4036).
package pathutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// trustedDirs are searched in order; all are root-owned and non-writable by
// an unprivileged user on a stock macOS or Linux install.
var trustedDirs = []string{
	"/usr/bin",
	"/bin",
	"/usr/sbin",
	"/sbin",
	"/usr/local/bin",
	"/opt/homebrew/bin",
}

// Resolve returns the absolute path of name found under one of trustedDirs,
// or an error if it isn't present in any of them. Unlike exec.LookPath, it
// never consults the PATH environment variable.
func Resolve(name string) (string, error) {
	for _, dir := range trustedDirs {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("pathutil: %q not found in trusted directories", name)
}

// MustResolve returns Resolve's result, falling back to name itself (letting
// exec.Command's own PATH-based lookup apply) when the trusted directories
// don't have it — so a nonstandard install location degrades to the old
// behavior instead of breaking the caller outright. Meant for package-level
// vars resolved once at init.
func MustResolve(name string) string {
	if p, err := Resolve(name); err == nil {
		return p
	}
	return name
}
