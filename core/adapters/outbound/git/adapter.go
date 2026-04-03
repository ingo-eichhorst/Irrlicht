package git

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	// Claude Code worktree branches are named "worktree-<slug>" — strip the prefix.
	branch = strings.TrimPrefix(branch, "worktree-")
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
	// Resolve the main repo root. For worktrees, --show-toplevel returns the
	// worktree path, not the main repo. Use --git-common-dir to find the
	// shared .git directory, whose parent is the real repo root.
	cmd := exec.Command("git", "rev-parse", "--path-format=absolute", "--git-common-dir")
	cmd.Dir = dir
	if out, err := cmd.Output(); err == nil {
		if gitDir := strings.TrimSpace(string(out)); gitDir != "" {
			// gitDir is e.g. "/Users/x/projects/irrlicht/.git"
			root := filepath.Dir(gitDir)
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
		// Claude Code: top-level "cwd" field.
		if cwd, ok := data["cwd"].(string); ok && cwd != "" {
			lastCWD = cwd
		}
		// Codex: CWD embedded in <cwd> XML tag inside environment_context.
		if cwd := extractCWDFromContent(data); cwd != "" {
			lastCWD = cwd
		}
		// Codex: workdir inside function_call arguments JSON string.
		if data["type"] == "function_call" {
			if args, ok := data["arguments"].(string); ok {
				var parsed map[string]interface{}
				if json.Unmarshal([]byte(args), &parsed) == nil {
					if wd, ok := parsed["workdir"].(string); ok && wd != "" {
						lastCWD = wd
					}
				}
			}
		}
	}
	return lastCWD
}

// cwdTagRe matches <cwd>/path</cwd> in Codex environment_context blocks.
var cwdTagRe = regexp.MustCompile(`<cwd>([^<]+)</cwd>`)

// extractCWDFromContent extracts CWD from Codex-style content blocks
// where environment_context contains <cwd>/path</cwd>.
func extractCWDFromContent(data map[string]interface{}) string {
	content, ok := data["content"].([]interface{})
	if !ok {
		return ""
	}
	for _, item := range content {
		block, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		text, ok := block["text"].(string)
		if !ok {
			continue
		}
		if m := cwdTagRe.FindStringSubmatch(text); len(m) > 1 {
			return strings.TrimSpace(m[1])
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
