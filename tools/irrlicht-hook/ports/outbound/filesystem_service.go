package outbound

// FileSystemService defines the contract for file system operations
type FileSystemService interface {
	// GetFileSize returns the size of a file in bytes
	GetFileSize(path string) int64
	
	// ExtractProjectName extracts the project name from a directory path
	ExtractProjectName(path string) string
	
	// FileExists checks if a file exists at the given path
	FileExists(path string) bool
	
	// ValidatePath validates that a path is safe and within allowed bounds
	ValidatePath(path string) error
}