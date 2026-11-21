package settingsenv

import (
	"os/user"
	"path/filepath"
	"runtime"
)

// ManagedDir returns the system directory Claude Code reads
// managed-settings.json and managed-settings.d/ from, per the managed
// settings documentation: "/Library/Application Support/ClaudeCode/ on
// macOS, /etc/claude-code/ on Linux and WSL, and C:\Program Files\ClaudeCode\
// on Windows". Empty on any other OS.
func ManagedDir() string {
	switch runtime.GOOS {
	case "darwin":
		return "/Library/Application Support/ClaudeCode"
	case "linux":
		return "/etc/claude-code"
	case "windows":
		return `C:\Program Files\ClaudeCode`
	}
	return ""
}

// MDMProfiles returns the files whose existence means a macOS configuration
// profile delivers Claude Code policy. The documentation names the managed
// preferences domain (com.anthropic.claudecode) but not its path; the two
// paths below are where macOS installs a managed-preferences domain for the
// device and for one user, an OS convention rather than a Claude Code
// statement. Their content is never read (Source.Opaque): only whether one
// exists. Empty on every other OS — the Windows registry is not looked at.
func MDMProfiles() []string {
	if runtime.GOOS != "darwin" {
		return nil
	}
	const domain = "com.anthropic.claudecode.plist"
	out := []string{filepath.Join("/Library/Managed Preferences", domain)}
	if u, err := user.Current(); err == nil && u.Username != "" {
		out = append(out, filepath.Join("/Library/Managed Preferences", u.Username, domain))
	}
	return out
}
