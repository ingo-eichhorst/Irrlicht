package git

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"irrlicht/core/pkg/transcript"
)

// Adapter implements ports/outbound.GitResolver using local git commands and
// transcript file inspection.
type Adapter struct{}

// New returns a new git Adapter.
func New() *Adapter { return &Adapter{} }

// GetBranch returns the current git branch for the given working directory.
// Returns "" if git is unavailable or the directory is not a git repo.
func (a *Adapter) GetBranch(dir string) string {
	if dir == "" {
		return ""
	}
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" || branch == "HEAD" {
		return ""
	}
	// Claude Code worktree branches are named "worktree-<slug>" — strip the prefix.
	branch = strings.TrimPrefix(branch, "worktree-")
	return branch
}

// GetGitRoot returns the absolute path of the git repo root for the given
// directory, or "" if the directory is not inside a git repository.
// For worktrees it returns the main repo root (not the worktree path).
// If dir has been deleted (e.g. a cleaned-up worktree), it walks up to the
// nearest existing ancestor so the repo can still be resolved.
func (a *Adapter) GetGitRoot(dir string) string {
	if dir == "" {
		return ""
	}
	dir = nearestExistingDir(dir)
	if dir == "" {
		return ""
	}
	cmd := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	gitDir := strings.TrimSpace(string(out))
	if gitDir == "" {
		return ""
	}
	// gitDir is e.g. "/Users/x/projects/irrlicht/.git"
	root := filepath.Dir(gitDir)
	if root == "" || root == "." || root == "/" {
		return ""
	}
	return root
}

// nearestExistingDir returns dir if it exists, otherwise walks up to the
// nearest existing ancestor directory. Returns "" if no ancestor exists.
func nearestExistingDir(dir string) string {
	for {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// GetProjectName returns the project name for the given directory.
// It uses the git repo root directory name so that sessions in subdirectories
// of the same repo share the same project name.
// Falls back to filepath.Base(dir) if not inside a git repo.
func (a *Adapter) GetProjectName(dir string) string {
	if root := a.GetGitRoot(dir); root != "" {
		return filepath.Base(root)
	}
	// Fallback for non-git directories.
	if dir == "" {
		return ""
	}
	name := filepath.Base(dir)
	if name == "." || name == "/" || name == "" {
		return ""
	}
	return name
}

// GetCWDFromTranscript extracts the working directory from a transcript file.
// It reads the last ~32KB and returns the LAST cwd found, which reflects the
// agent's current working directory (important for worktree switches).
func (a *Adapter) GetCWDFromTranscript(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	file, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer file.Close()

	// Read from the tail of the file to find the latest CWD.
	stat, err := file.Stat()
	if err != nil {
		return ""
	}
	const maxTail = 32 * 1024
	startPos := int64(0)
	if stat.Size() > maxTail {
		startPos = stat.Size() - maxTail
	}
	if _, err := file.Seek(startPos, 0); err != nil {
		return ""
	}

	var lastCWD string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var data map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &data); err != nil {
			continue
		}
		if cwd := transcript.ExtractCWDFromLine(data); cwd != "" {
			lastCWD = cwd
		}
	}
	return lastCWD
}

// GetBranchFromTranscript tries to extract the gitBranch field from the last
// few lines of a Claude Code transcript file.
func (a *Adapter) GetBranchFromTranscript(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	file, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer file.Close()

	var lines []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > 10 {
			lines = lines[1:]
		}
	}

	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if !strings.Contains(line, "gitBranch") {
			continue
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(line), &data); err == nil {
			if branch, ok := data["gitBranch"].(string); ok && branch != "" {
				return branch
			}
		}
	}
	return ""
}
