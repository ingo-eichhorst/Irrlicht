package git

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"

	"irrlicht/hook/ports/outbound"
)

// Service implements the GitService interface
type Service struct{}

// NewService creates a new git service
func NewService() outbound.GitService {
	return &Service{}
}

// GetCurrentBranch gets the current Git branch from a directory
func (g *Service) GetCurrentBranch(cwd string) string {
	if cwd == "" {
		return ""
	}
	
	branch, err := g.GetBranch(cwd)
	if err != nil || branch == "" || branch == "HEAD" {
		return ""
	}
	
	return branch
}

// GetRepositoryInfo returns comprehensive repository information
func (g *Service) GetRepositoryInfo(cwd string) (*outbound.RepositoryInfo, error) {
	if !g.IsGitRepository(cwd) {
		return nil, fmt.Errorf("not a git repository: %s", cwd)
	}
	
	info := &outbound.RepositoryInfo{
		ProjectName: filepath.Base(cwd),
	}
	
	// Get branch
	if branch, err := g.GetBranch(cwd); err == nil {
		info.Branch = branch
	}
	
	// Get commit hash
	if hash, err := g.GetCommitHash(cwd); err == nil {
		info.CommitHash = hash
	}
	
	// Get remote URL
	if url, err := g.GetRemoteURL(cwd, "origin"); err == nil {
		info.RemoteURL = url
	}
	
	// Get status information
	hasChanges, hasUntracked, err := g.GetStatus(cwd)
	if err == nil {
		info.IsClean = !hasChanges
		info.HasUntracked = hasUntracked
	}
	
	return info, nil
}

// IsGitRepository checks if a directory is a Git repository
func (g *Service) IsGitRepository(path string) bool {
	return g.IsGitRepo(path)
}

// GetBranch returns the current git branch for the given directory
func (g *Service) GetBranch(workingDir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = workingDir
	
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	
	branch := strings.TrimSpace(string(output))
	return branch, nil
}

// GetCommitHash returns the current git commit hash for the given directory
func (g *Service) GetCommitHash(workingDir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "HEAD")
	cmd.Dir = workingDir
	
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	
	hash := strings.TrimSpace(string(output))
	return hash, nil
}

// GetRepoRoot returns the root directory of the git repository
func (g *Service) GetRepoRoot(workingDir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = workingDir
	
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	
	root := strings.TrimSpace(string(output))
	return root, nil
}

// IsGitRepo checks if the given directory is inside a git repository
func (g *Service) IsGitRepo(workingDir string) bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = workingDir
	
	err := cmd.Run()
	return err == nil
}

// GetRemoteURL returns the URL of the remote repository
func (g *Service) GetRemoteURL(workingDir string, remoteName string) (string, error) {
	if remoteName == "" {
		remoteName = "origin"
	}
	
	cmd := exec.Command("git", "remote", "get-url", remoteName)
	cmd.Dir = workingDir
	
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	
	url := strings.TrimSpace(string(output))
	return url, nil
}

// GetStatus returns git status information as strings
func (g *Service) GetStatus(workingDir string) (bool, bool, error) {
	// Get porcelain status
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = workingDir
	
	output, err := cmd.Output()
	if err != nil {
		return false, false, err
	}
	
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	
	hasChanges := false
	hasUntracked := false
	
	for _, line := range lines {
		if len(line) < 3 {
			continue
		}
		
		statusCode := line[:2]
		
		// Parse git status codes
		if statusCode[0] != ' ' || statusCode[1] != ' ' {
			hasChanges = true
		}
		if statusCode == "??" {
			hasUntracked = true
		}
	}
	
	return hasChanges, hasUntracked, nil
}

// GetRelativePath returns the relative path from the git repo root
func (g *Service) GetRelativePath(workingDir, filePath string) (string, error) {
	root, err := g.GetRepoRoot(workingDir)
	if err != nil {
		return "", err
	}
	
	relPath, err := filepath.Rel(root, filePath)
	if err != nil {
		return "", err
	}
	
	return relPath, nil
}