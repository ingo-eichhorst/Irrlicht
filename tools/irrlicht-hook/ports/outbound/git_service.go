package outbound

// GitService defines the outbound port for Git operations
type GitService interface {
	// GetCurrentBranch gets the current Git branch from a directory
	GetCurrentBranch(cwd string) string
	
	// GetRepositoryInfo returns comprehensive repository information
	GetRepositoryInfo(cwd string) (*RepositoryInfo, error)
	
	// IsGitRepository checks if a directory is a Git repository
	IsGitRepository(path string) bool
}

// RepositoryInfo holds information about a Git repository
type RepositoryInfo struct {
	Branch       string `json:"branch"`
	ProjectName  string `json:"project_name"`
	RemoteURL    string `json:"remote_url,omitempty"`
	CommitHash   string `json:"commit_hash,omitempty"`
	IsClean      bool   `json:"is_clean"`
	HasUntracked bool   `json:"has_untracked"`
}

// GitExecutor defines the interface for executing Git commands
type GitExecutor interface {
	// ExecuteGitCommand executes a Git command in the specified directory
	ExecuteGitCommand(dir string, args ...string) (string, error)
	
	// ExecuteGitCommandWithTimeout executes a Git command with timeout
	ExecuteGitCommandWithTimeout(dir string, timeoutMs int, args ...string) (string, error)
}

// GitValidator defines validation for Git-related operations
type GitValidator interface {
	// ValidateGitDirectory validates that a directory is safe for Git operations
	ValidateGitDirectory(dir string) error
	
	// ValidateBranchName validates a Git branch name format
	ValidateBranchName(branch string) error
	
	// SanitizeGitOutput sanitizes Git command output
	SanitizeGitOutput(output string) string
}

// GitMetrics defines metrics collection for Git operations
type GitMetrics interface {
	// RecordGitOperation records metrics for a Git operation
	RecordGitOperation(operation string, duration int64, success bool)
	
	// GetGitStats returns Git operation statistics
	GetGitStats() GitStats
}

// GitStats holds statistics about Git operations
type GitStats struct {
	TotalOperations   int64
	SuccessfulOps     int64
	FailedOps         int64
	AverageDurationMs float64
	LastOperationTime int64
}