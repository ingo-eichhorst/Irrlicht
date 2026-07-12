package git

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"irrlicht/core/domain/dora"
	"irrlicht/core/pkg/pathutil"
	"irrlicht/core/pkg/transcript"
)

// revertTrailer matches the "This reverts commit <sha>." trailer that
// `git revert` writes into the body of a revert commit (#373).
var revertTrailer = regexp.MustCompile(`(?m)^This reverts commit ([0-9a-f]{7,40})`)

// releaseTagPattern matches release tags of the form v<major>.<minor>.<patch>
// (this repo's version.json convention) — used by the DORA metrics methods
// below to filter out non-release tags (#951).
var releaseTagPattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+$`)

// gitPath is resolved once from a fixed set of trusted directories rather
// than trusted PATH, per go:S4036.
var gitPath = pathutil.MustResolve("git")

// gitRevParseCmd is the git subcommand shared by GetBranch, GetHeadCommit,
// and GetGitRoot to resolve refs, commits, and repo-relative paths.
const gitRevParseCmd = "rev-parse"

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
	cmd := exec.Command(gitPath, gitRevParseCmd, "--abbrev-ref", "HEAD")
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

// GetHeadCommit returns the full SHA of the current HEAD commit for the given
// working directory. Returns "" if git is unavailable or the directory is not
// a git repo (e.g. an unborn branch with no commits yet). See issue #373.
func (a *Adapter) GetHeadCommit(dir string) string {
	if dir == "" {
		return ""
	}
	cmd := exec.Command(gitPath, gitRevParseCmd, "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// RevertedCommits returns the full SHAs reverted in the repo containing dir,
// parsed from the "This reverts commit <sha>." trailer that `git revert`
// writes. It scans all reachable history (`--all`), so a revert on any branch
// counts — the documented v1 behavior (#373). Returns nil if dir is not a git
// repo or has no reverts. Errors are swallowed: a sweep over many projects must
// tolerate non-git, permission-denied, and missing directories without failing.
func (a *Adapter) RevertedCommits(dir string) []string {
	if dir == "" {
		return nil
	}
	cmd := exec.Command(gitPath, "log", "--all", "--grep", "^This reverts commit ", "--pretty=format:%b")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	matches := revertTrailer.FindAllSubmatch(out, -1)
	if len(matches) == 0 {
		return nil
	}
	shas := make([]string, 0, len(matches))
	for _, m := range matches {
		shas = append(shas, string(m[1]))
	}
	return shas
}

// ListReleaseTags returns every release tag in the repo containing dir,
// oldest-first by creation date (#951). Returns nil if dir is not a git
// repo or has no release tags. Errors are swallowed, matching this
// adapter's other read methods.
func (a *Adapter) ListReleaseTags(dir string) []dora.TagInfo {
	if dir == "" {
		return nil
	}
	cmd := exec.Command(gitPath, "for-each-ref", "--sort=creatordate", "--format=%(refname:short)%09%(creatordate:unix)", "refs/tags")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var tags []dora.TagInfo
	for _, line := range strings.Split(string(out), "\n") {
		name, epochStr, ok := strings.Cut(line, "\t")
		if !ok || !releaseTagPattern.MatchString(name) {
			continue
		}
		epoch, err := strconv.ParseInt(epochStr, 10, 64)
		if err != nil {
			continue
		}
		tags = append(tags, dora.TagInfo{Name: name, Epoch: epoch})
	}
	return tags
}

// CommitsInRange returns the commits reachable from toRef but not fromRef
// (fromRef empty walks toRef's entire history — for the oldest release
// tag, which has no predecessor) (#951). Returns nil if dir is not a git
// repo, toRef is empty, or the range is empty.
func (a *Adapter) CommitsInRange(dir, fromRef, toRef string) []dora.CommitInfo {
	if dir == "" || toRef == "" {
		return nil
	}
	rangeSpec := toRef
	if fromRef != "" {
		rangeSpec = fromRef + ".." + toRef
	}
	// \x01/\x02 are ASCII control bytes used as record/field separators —
	// they won't collide with real commit message content, so a multi-line
	// %B body can be parsed without spawning a process per commit.
	cmd := exec.Command(gitPath, "log", "--pretty=format:%x01%H%x02%at%x02%B", rangeSpec)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var commits []dora.CommitInfo
	for _, record := range bytes.Split(out, []byte{0x01}) {
		if len(record) == 0 {
			continue
		}
		parts := bytes.SplitN(record, []byte{0x02}, 3)
		if len(parts) != 3 {
			continue
		}
		epoch, err := strconv.ParseInt(string(parts[1]), 10, 64)
		if err != nil {
			continue
		}
		commits = append(commits, dora.CommitInfo{Hash: string(parts[0]), AuthorEpoch: epoch, Body: string(parts[2])})
	}
	return commits
}

// TagContaining returns the earliest release tag (by creation date) that
// contains hash, or "" if none does — either the commit was never
// released, or dir is not a git repo (#951). Used to resolve which release
// shipped the original commit a revert commit targets.
func (a *Adapter) TagContaining(dir, hash string) string {
	if dir == "" || hash == "" {
		return ""
	}
	cmd := exec.Command(gitPath, "tag", "--contains", hash, "--sort=creatordate")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if releaseTagPattern.MatchString(line) {
			return line
		}
	}
	return ""
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
	cmd := exec.Command(gitPath, gitRevParseCmd, "--path-format=absolute", "--git-common-dir")
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
	if lastCWD == "" {
		// Some agents (Kiro CLI) record the cwd only in a metadata sidecar
		// next to the transcript, never in the JSONL lines themselves.
		lastCWD = transcript.ExtractCWDFromSidecar(transcriptPath)
	}
	if lastCWD == "" {
		// Antigravity records only its sandbox scratch dir in the transcript
		// body; the real workspace lives in the sibling history.jsonl index,
		// keyed by conversationId (no-op for non-antigravity paths).
		lastCWD = transcript.ExtractCWDFromAntigravityHistory(transcriptPath)
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
