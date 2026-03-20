package gtbin

import (
	"os"
	"os/exec"
	"path/filepath"
)

// Resolver locates the gt binary at construction time using a three-tier
// strategy: GT_BIN env var → common installation paths → exec.LookPath.
type Resolver struct {
	path string
}

// commonPaths lists well-known installation locations for the gt binary.
var commonPaths = []string{
	"/usr/local/bin/gt",
	"/opt/homebrew/bin/gt",
}

// New creates a Resolver, resolving the gt binary path immediately.
// The resolved path is stored for the lifetime of the process.
func New() *Resolver {
	r := &Resolver{}
	r.path = r.resolve()
	return r
}

// Path returns the resolved absolute path, or "" if not found.
func (r *Resolver) Path() string {
	return r.path
}

// resolve applies the three-tier lookup strategy.
func (r *Resolver) resolve() string {
	// Tier 1: GT_BIN environment variable.
	if p := os.Getenv("GT_BIN"); p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			if isExecutable(abs) {
				return abs
			}
		}
	}

	// Tier 2: well-known installation paths + ~/bin, ~/go/bin.
	home, _ := os.UserHomeDir()
	paths := make([]string, len(commonPaths))
	copy(paths, commonPaths)
	if home != "" {
		paths = append(paths,
			filepath.Join(home, "bin", "gt"),
			filepath.Join(home, "go", "bin", "gt"),
		)
	}
	for _, p := range paths {
		if isExecutable(p) {
			return p
		}
	}

	// Tier 3: fall back to PATH lookup (equivalent to `which gt`).
	if p, err := exec.LookPath("gt"); err == nil {
		if abs, err := filepath.Abs(p); err == nil {
			return abs
		}
		return p
	}

	return ""
}

// isExecutable returns true if path exists and has at least one execute bit.
func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	return fi.Mode()&0111 != 0
}
