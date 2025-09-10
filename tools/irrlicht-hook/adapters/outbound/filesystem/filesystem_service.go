package filesystem

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"irrlicht/hook/ports/outbound"
)

// ServiceAdapter implements the FileSystemService port
type ServiceAdapter struct{}

// NewServiceAdapter creates a new file system service adapter
func NewServiceAdapter() outbound.FileSystemService {
	return &ServiceAdapter{}
}

// GetFileSize returns the size of a file in bytes
func (fs *ServiceAdapter) GetFileSize(path string) int64 {
	if path == "" {
		return 0
	}

	stat, err := os.Stat(path)
	if err != nil {
		return 0
	}

	return stat.Size()
}

// ExtractProjectName extracts the project name from a directory path
func (fs *ServiceAdapter) ExtractProjectName(path string) string {
	if path == "" {
		return ""
	}

	// Get the last directory name from the path
	projectName := filepath.Base(path)

	// Handle edge cases
	if projectName == "." || projectName == "/" || projectName == "" {
		return ""
	}

	return projectName
}

// FileExists checks if a file exists at the given path
func (fs *ServiceAdapter) FileExists(path string) bool {
	if path == "" {
		return false
	}

	_, err := os.Stat(path)
	return err == nil
}

// ValidatePath validates that a path is safe and within allowed bounds
func (fs *ServiceAdapter) ValidatePath(path string) error {
	// Check for suspicious patterns
	suspicious := []string{
		"/etc/", "/root/", "/var/", "/usr/", "/sys/", "/dev/", "/proc/",
		"../", "..\\", "C:\\", "\\\\", "//",
	}

	for _, pattern := range suspicious {
		if strings.Contains(path, pattern) {
			return fmt.Errorf("suspicious path pattern: %s", pattern)
		}
	}

	// Must be absolute path within user home
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("cannot determine home directory")
	}

	if !filepath.IsAbs(path) {
		return fmt.Errorf("path must be absolute")
	}

	if !strings.HasPrefix(path, homeDir) {
		return fmt.Errorf("path must be within user home directory")
	}

	return nil
}