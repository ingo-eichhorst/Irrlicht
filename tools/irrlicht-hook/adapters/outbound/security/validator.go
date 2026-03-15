package security

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// PathValidator implements ports/outbound.PathValidator, ensuring paths are
// within the user's home directory and free of suspicious patterns.
type PathValidator struct {
	homeDir string
}

// New returns a PathValidator for the current user's home directory.
func New() (*PathValidator, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine home directory: %w", err)
	}
	return &PathValidator{homeDir: homeDir}, nil
}

// NewWithHomeDir returns a PathValidator anchored to the given home directory (for tests).
func NewWithHomeDir(homeDir string) *PathValidator {
	return &PathValidator{homeDir: homeDir}
}

var suspiciousPatterns = []string{
	"/etc/", "/root/", "/var/", "/usr/", "/sys/", "/dev/", "/proc/",
	"../", "..\\", "C:\\", "\\\\", "//",
}

// Validate returns an error if path is outside the home directory or contains
// suspicious traversal patterns.
func (v *PathValidator) Validate(path string) error {
	for _, p := range suspiciousPatterns {
		if strings.Contains(path, p) {
			return fmt.Errorf("suspicious path pattern: %s", p)
		}
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("path must be absolute")
	}
	if !strings.HasPrefix(path, v.homeDir) {
		return fmt.Errorf("path must be within user home directory")
	}
	return nil
}
