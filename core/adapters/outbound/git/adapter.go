package git

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	return branch
}

// GetProjectName returns the project name for the given directory.
// It uses the git repo root directory name so that sessions in subdirectories
// of the same repo share the same project name.
// Falls back to filepath.Base(dir) if not inside a git repo.
func (a *Adapter) GetProjectName(dir string) string {
	if dir == "" {
		return ""
	}
	// Try to resolve git repo root — sessions in the same repo share one project name.
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	if out, err := cmd.Output(); err == nil {
		if root := strings.TrimSpace(string(out)); root != "" {
			name := filepath.Base(root)
			if name != "" && name != "." && name != "/" {
				return name
			}
		}
	}
	// Fallback for non-git directories.
	name := filepath.Base(dir)
	if name == "." || name == "/" || name == "" {
		return ""
	}
	return name
}

// GetCWDFromTranscript extracts the working directory from the first few lines
// of a Claude Code transcript file by looking for a "cwd" field in JSON entries.
func (a *Adapter) GetCWDFromTranscript(transcriptPath string) string {
	if transcriptPath == "" {
		return ""
	}
	file, err := os.Open(transcriptPath)
	if err != nil {
		return ""
	}
	defer file.Close()

	scanned := 0
	scanner := bufio.NewScanner(file)
	for scanner.Scan() && scanned < 10 {
		scanned++
		var data map[string]interface{}
		if err := json.Unmarshal(scanner.Bytes(), &data); err != nil {
			continue
		}
		if cwd, ok := data["cwd"].(string); ok && cwd != "" {
			return cwd
		}
	}
	return ""
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
