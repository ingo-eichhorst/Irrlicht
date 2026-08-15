package git

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"irrlicht/core/domain/dora"
	"irrlicht/core/pkg/pathutil"
	"irrlicht/core/pkg/shellout"
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

// gitTimeout is the ceiling EVERY shellout in this adapter runs under. Until
// #1543 there was none at all: seven plain exec.Command calls with no context,
// inside a long-lived daemon, on directories that can be network mounts or
// repos holding a lock.
//
// The value is this adapter's own rather than a copy of the 2s next door
// (processlifecycle's shelloutTimeout), because the workloads are not
// comparable — a `ps` answers about one PID, `git log --all --grep` walks
// every commit a user has ever reachable. Measured on this repo (3192 commits,
// 265 MiB pack, git 2.50.1 / Apple Git-155, darwin/arm64, warm cache):
// `log --all --grep` 0.05s, `for-each-ref refs/tags` 0.01s,
// `tag --contains` 0.01s, `rev-parse` 0.01s. 5s is ~100x the heaviest of
// those, and ~500x the three on the session-enrichment path, which are all
// rev-parse.
//
// Two consequences are stated rather than left implied. The ceiling is a
// SINGLE value across two profiles: the enrichment reads (GetBranch,
// GetHeadCommit, GetGitRoot) run inside the detector loop where 5s of stall is
// bad but bounded, while the DORA reads run on a request a user is already
// waiting on. And a repo large enough to legitimately exceed 5s now reports a
// non-answer where it previously returned a correct-but-slow result — which is
// the trade this whole family makes, and is safe only because a non-answer is
// now propagated as "unknown" rather than as "there was nothing" (#1492's
// polarity; nothing here gates admission). UNVERIFIED: the scaling to a very
// large repo is extrapolation from the numbers above (~810 bytes and ~16us of
// `log` per commit), not a measurement on such a repo.
//
// It bounds ONE CALL, not one operation, and callers stack it: adoptGitMetadata
// makes 2, RefreshOnActivity up to 3, and ComputeDoraMetrics
// 1 + len(tags) + len(revertCandidates) — on a server whose WriteTimeout is
// deliberately 0 (core/cmd/irrlichd/startup.go). A per-operation budget is a
// larger change than #1543 and is not attempted here; what is fixed is that the
// unbounded case is gone.
const gitTimeout = 5 * time.Second

// gitMaxOutput caps how much of git's stdout is held in memory. gitTimeout
// bounds how long git runs, not how much it writes in that time, and this runs
// inside a long-lived daemon: `git log --pretty=format:...%B` over full history
// measured 2.59 MB on this repo's 3192 commits (~810 bytes/commit), so a
// 100k-commit repo is ~81 MB and a 1M-commit one ~810 MB, all buffered by
// cmd.Output() before #1543 with nothing to stop it.
//
// run() reports overflow as a NON-ANSWER rather than truncating, which is the
// opposite of cliprobe's choice and deliberately so: a truncated version banner
// still contains the version, whereas a truncated commit list silently drops
// releases and reverts, and DORA would then publish a smaller sample as though
// it were the whole one. "I could not read it all" is a fact; a biased median
// presented as Available:true is not.
const gitMaxOutput = 64 << 20

// gitCmd builds one bounded git child process. Injected so a test can arrange
// the one distinction that matters here and that no faked return value can
// pin: a child that RAN to a normal exit versus one killed or never started.
// The shape is processlifecycle's shelloutCmd (#1524/#1538) with the two
// arguments every git call varies.
type gitCmd func(ctx context.Context, dir string, args ...string) *exec.Cmd

// Adapter implements ports/outbound.GitResolver using local git commands and
// transcript file inspection.
type Adapter struct {
	cmd gitCmd
	// timeout is gitTimeout unless a test lowered it. Injected for the same
	// reason cmd is — driving the ceiling means waiting it out, and two tests
	// waiting out 5s each is both slow and the most load-sensitive thing in
	// the package. TestProductionAdapterIsBounded deliberately does NOT use
	// this seam: it goes through New(), so the real constant stays proven.
	timeout time.Duration
}

// New returns a new git Adapter.
func New() *Adapter { return &Adapter{cmd: execGitCmd, timeout: gitTimeout} }

// execGitCmd is the production builder: git, resolved through pathutil rather
// than the inherited PATH (go:S4036), run in dir.
func execGitCmd(ctx context.Context, dir string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, gitPath, args...)
	cmd.Dir = dir
	return cmd
}

// run executes `git args...` in dir under gitTimeout and reports whether git
// ANSWERED — as opposed to never having been asked.
//
// answered is false for exactly three things: the ceiling killed the child,
// the child never started (git missing, fork failure), or its output exceeded
// gitMaxOutput. It is TRUE for every non-zero exit git produces on its own,
// because git has no exit status meaning "I could not look": measured on git
// 2.50.1 / Apple Git-155 (darwin/arm64), not-a-repo is 128 for all six
// subcommands this adapter runs, an unborn branch's rev-parse is 128, a range
// naming an unknown ref is 128, `tag --contains` with an unresolvable object
// name is 129, and "no tags" / "no reverts" are 0 with empty stdout. That is
// why shellout.Answered is called in its EMPTY-variadic form here — the same
// form, for the same reason, that plutil uses (#1524).
//
// The limit that reading buys: a repo whose objects are corrupt, or whose
// .git is on a filesystem returning EIO, also exits 128, so this reports it as
// "git answered: not a repo". That is the reading git itself gives (it prints
// `fatal:`), and it is the pre-existing behaviour; the distinction #1543 is
// about is "git could not be RUN" versus "git ran and said no", and that is
// the line drawn here.
// ceiling is the adapter's timeout, defaulting to gitTimeout so a
// zero-valued Adapter (a struct literal in a test) is still bounded rather
// than unbounded — the failure mode this whole issue is about.
func (a *Adapter) ceiling() time.Duration {
	if a.timeout > 0 {
		return a.timeout
	}
	return gitTimeout
}

func (a *Adapter) run(dir string, args ...string) (out []byte, answered bool) {
	if dir == "" {
		// Nothing was asked, so nothing failed — the reading processTTYVia
		// gives a non-positive pid. Deciding it here rather than at each
		// method keeps it one fact: it is a property of starting a child, not
		// of any particular git subcommand.
		return nil, true
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.ceiling())
	defer cancel()

	cmd := a.cmd(ctx, dir, args...)
	cmd.WaitDelay = shellout.WaitDelay
	// Explicit rather than load-bearing: a nil Stdin is already /dev/null, so
	// os/exec never hands the daemon's own stdin to the child. Spelled out so
	// a later edit does not "helpfully" wire one up — a git that decided to
	// prompt (a credential helper, a hook) would then block until the ceiling.
	cmd.Stdin = nil
	buf := shellout.CappedBuffer{Limit: gitMaxOutput}
	cmd.Stdout = &buf
	// stderr is left nil (i.e. /dev/null) rather than captured: nothing here
	// reads it, and a pipe nobody drains is a way to block.
	err := cmd.Run()

	if !shellout.Answered(err) {
		return nil, false
	}
	if buf.Overflowed() {
		return nil, false
	}
	if err != nil {
		// git RAN and reported a failure. That is an answer — "not a repo",
		// "no such ref" — but its stdout is NOT the answer's content, and
		// returning it is a defect in both directions:
		//
		//   - `git rev-parse HEAD` on an unborn branch exits 128 and writes
		//     the literal string "HEAD" to STDOUT (measured, git 2.50.1 /
		//     Apple Git-155, darwin/arm64). Forwarding that made
		//     GetHeadCommit report "HEAD" as a commit SHA, which
		//     CaptureYieldOnReady then persisted as YieldProductive.
		//   - A `git log` that streams commits and then hits a fatal (a
		//     corrupt object) exits 128 having already written a PREFIX of
		//     the history. Forwarding it hands DORA a silently truncated
		//     commit list — the exact harm gitMaxOutput's doc argues must
		//     never happen, arriving through a different door.
		//
		// Discarding it restores what .Output()'s `if err != nil` arm did
		// before #1543, while keeping the non-answer distinction that arm
		// could not express.
		return nil, true
	}
	return buf.Bytes(), true
}

// GetBranch returns the current git branch for the given working directory.
//
// answered is false when git could not be RUN at all (see run). It is TRUE
// with an empty branch for every case where git answered and there is no
// branch to report: dir is not a repo, HEAD is detached, or the branch is
// unborn. Callers must not overwrite a branch they already hold on a
// non-answer — an empty string there carries no information (#1485/#1543).
func (a *Adapter) GetBranch(dir string) (string, bool) {
	out, answered := a.run(dir, gitRevParseCmd, "--abbrev-ref", "HEAD")
	if !answered {
		return "", false
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" || branch == "HEAD" {
		return "", true
	}
	// Claude Code worktree branches are named "worktree-<slug>" — strip the prefix.
	branch = strings.TrimPrefix(branch, "worktree-")
	return branch, true
}

// GetHeadCommit returns the full SHA of the current HEAD commit for the given
// working directory. An empty SHA with answered=true means git ran and there
// is no HEAD — not a git repo, or an unborn branch with no commits yet (#373).
// answered=false means git could not be run, which is not evidence about
// either.
func (a *Adapter) GetHeadCommit(dir string) (string, bool) {
	out, answered := a.run(dir, gitRevParseCmd, "HEAD")
	if !answered {
		return "", false
	}
	return strings.TrimSpace(string(out)), true
}

// RevertedCommits returns the full SHAs reverted in the repo containing dir,
// parsed from the "This reverts commit <sha>." trailer that `git revert`
// writes. It scans all reachable history (`--all`), so a revert on any branch
// counts — the documented v1 behavior (#373).
//
// A nil slice with answered=true means git ran and this repo has no reverts
// (or is not a repo at all): a sweep over many projects must tolerate non-git,
// permission-denied and missing directories without failing, and git reports
// all of those as ordinary non-zero exits. answered=false means the scan did
// not happen, which is NOT "no reverts" — treating it as such is what makes a
// yield sweep report "nothing flipped" for a repo it never read (#1543).
func (a *Adapter) RevertedCommits(dir string) ([]string, bool) {
	out, answered := a.run(dir, "log", "--all", "--grep", "^This reverts commit ", "--pretty=format:%b")
	if !answered {
		return nil, false
	}
	matches := revertTrailer.FindAllSubmatch(out, -1)
	if len(matches) == 0 {
		return nil, true
	}
	shas := make([]string, 0, len(matches))
	for _, m := range matches {
		shas = append(shas, string(m[1]))
	}
	return shas, true
}

// ListReleaseTags returns every release tag in the repo containing dir,
// oldest-first by creation date (#951). A nil slice with answered=true means
// git ran and there are no release tags (or dir is not a repo);
// answered=false means git could not be run, and reporting that as "no
// releases found for this project" is the false claim #1543 removes.
func (a *Adapter) ListReleaseTags(dir string) ([]dora.TagInfo, bool) {
	out, answered := a.run(dir, "for-each-ref", "--sort=creatordate", "--format=%(refname:short)%09%(creatordate:unix)", "refs/tags")
	if !answered {
		return nil, false
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
	return tags, true
}

// CommitsInRange returns the commits reachable from toRef but not fromRef
// (fromRef empty walks toRef's entire history — for the oldest release
// tag, which has no predecessor) (#951).
//
// answered=false is the case DORA must not average over: a partial failure is
// worse than a total one, because a median lead time computed over the tags
// that happened to succeed is a biased number reported as Available:true.
func (a *Adapter) CommitsInRange(dir, fromRef, toRef string) ([]dora.CommitInfo, bool) {
	// toRef, unlike dir, is still guarded HERE: an empty one would become an
	// empty argv entry rather than no call at all. run() handles the dir case.
	if toRef == "" {
		return nil, true
	}
	rangeSpec := toRef
	if fromRef != "" {
		rangeSpec = fromRef + ".." + toRef
	}
	// \x01/\x02 are ASCII control bytes used as record/field separators —
	// they won't collide with real commit message content, so a multi-line
	// %B body can be parsed without spawning a process per commit.
	out, answered := a.run(dir, "log", "--pretty=format:%x01%H%x02%at%x02%B", rangeSpec)
	if !answered {
		return nil, false
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
	return commits, true
}

// TagContaining returns the earliest release tag (by creation date) that
// contains hash (#951). Used to resolve which release shipped the original
// commit a revert commit targets.
//
// An empty tag with answered=true means git ran and no release contains it —
// the commit was never released, dir is not a repo, or the object name does
// not resolve (measured: exit 129). answered=false means git could not be run,
// and treating that as "never released" DROPS the revert from the failure
// list, which makes Change Failure Rate read BETTER than reality — the one
// path in this adapter where the collapse flatters the number (#1543).
func (a *Adapter) TagContaining(dir, hash string) (string, bool) {
	// hash, unlike dir, is still guarded HERE — an empty one would become an
	// empty argv entry. run() handles the dir case.
	if hash == "" {
		return "", true
	}
	out, answered := a.run(dir, "tag", "--contains", hash, "--sort=creatordate")
	if !answered {
		return "", false
	}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if releaseTagPattern.MatchString(line) {
			return line, true
		}
	}
	return "", true
}

// GetGitRoot returns the absolute path of the git repo root for the given
// directory. For worktrees it returns the main repo root (not the worktree
// path). If dir has been deleted (e.g. a cleaned-up worktree), it walks up to
// the nearest existing ancestor so the repo can still be resolved.
//
// An empty root with answered=true means git ran and dir is not inside a
// repository; answered=false means git could not be run, and reporting that to
// a user as "project not found or not a git repository" is a second false
// claim #1543 removes.
func (a *Adapter) GetGitRoot(dir string) (string, bool) {
	// This guard must stay, and it is NOT the one run() absorbed:
	// nearestExistingDir("") walks Dir("") == "." and Stat(".") succeeds, so
	// without it an empty dir would resolve to the DAEMON'S OWN working
	// directory and report that repo's root.
	if dir == "" {
		return "", true
	}
	dir = nearestExistingDir(dir)
	out, answered := a.run(dir, gitRevParseCmd, "--path-format=absolute", "--git-common-dir")
	if !answered {
		return "", false
	}
	gitDir := strings.TrimSpace(string(out))
	if gitDir == "" {
		return "", true
	}
	// gitDir is e.g. "/Users/x/projects/irrlicht/.git"
	root := filepath.Dir(gitDir)
	if root == "" || root == "." || root == "/" {
		return "", true
	}
	return root, true
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
//
// It runs no child process of its own — the second return is GetGitRoot's,
// forwarded. That forwarding is the point rather than bookkeeping: on a
// non-answer the returned name is the directory basename, which for a worktree
// is the WORKTREE's name rather than the repo's, and the one consumer that
// caches this (filesystem.ConcurrencyTracker) memoises for the daemon's
// lifetime. A guess cached forever as though it were resolved is the same
// defect one layer up (#1528's "memoize a non-answer as a non-answer").
func (a *Adapter) GetProjectName(dir string) (string, bool) {
	root, answered := a.GetGitRoot(dir)
	if root != "" {
		return filepath.Base(root), answered
	}
	// Fallback for non-git directories.
	if dir == "" {
		return "", answered
	}
	name := filepath.Base(dir)
	if name == "." || name == "/" || name == "" {
		return "", answered
	}
	return name, answered
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
		lastCWD = cwdFromFallbackSidecars(transcriptPath)
	}
	return lastCWD
}

// cwdFromFallbackSidecars tries each agent-specific sidecar/index convention
// in turn, stopping at the first that resolves a cwd. These are the agents
// whose transcript lines never carry a cwd at all, so GetCWDFromTranscript's
// main scan above always comes up empty for them.
func cwdFromFallbackSidecars(transcriptPath string) string {
	extractors := []func(string) string{
		// Some agents (Kiro CLI) record the cwd only in a metadata sidecar
		// next to the transcript, never in the JSONL lines themselves.
		transcript.ExtractCWDFromSidecar,
		// Antigravity records only its sandbox scratch dir in the transcript
		// body; the real workspace lives in the sibling history.jsonl index,
		// keyed by conversationId (no-op for non-antigravity paths).
		transcript.ExtractCWDFromAntigravityHistory,
		// mistral-vibe never writes cwd into messages.jsonl, and its meta.json
		// sidecar doesn't follow the generic same-basename convention above
		// (fixed filename, cwd nested under environment.working_directory) —
		// see issue #906 (presession promotion can't CWD-match without this).
		transcript.ExtractCWDFromVibeMetaJSON,
	}
	for _, extract := range extractors {
		if cwd := extract(transcriptPath); cwd != "" {
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
