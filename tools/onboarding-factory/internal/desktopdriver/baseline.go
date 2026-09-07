package desktopdriver

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const maxBaselineEntries = 20_000

type SnapshotEntry struct {
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode,omitempty"`
	Digest string `json:"digest,omitempty"`
	Link   string `json:"link,omitempty"`
}

// TreeSnapshot is an exact, non-following inventory of selected config roots.
// Missing roots are recorded so unexpected creation is also detected.
type TreeSnapshot map[string]SnapshotEntry

type configPath string

type treeSnapshotCapture struct {
	snapshot TreeSnapshot
}

func CaptureTreeSnapshot(roots []string) (TreeSnapshot, error) {
	snapshot := TreeSnapshot{}
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			return nil, fmt.Errorf("configuration baseline root is not absolute: %q", root)
		}
		if err := captureRoot(snapshot, configPath(filepath.Clean(root))); err != nil {
			return nil, err
		}
	}
	return snapshot, nil
}

func captureRoot(snapshot TreeSnapshot, root configPath) error {
	info, err := os.Lstat(string(root))
	if os.IsNotExist(err) {
		snapshot[string(root)] = SnapshotEntry{Kind: "absent"}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect configuration path %q: %w", root, err)
	}
	if !info.IsDir() {
		return captureSnapshotEntry(snapshot, root, info)
	}
	capture := treeSnapshotCapture{snapshot: snapshot}
	return filepath.WalkDir(string(root), func(path string, entry fs.DirEntry, walkErr error) error {
		return capture.walk(configPath(path), entry, walkErr)
	})
}

func (capture treeSnapshotCapture) walk(path configPath, entry fs.DirEntry, walkErr error) error {
	if walkErr != nil {
		return fmt.Errorf("walk configuration path %q: %w", path, walkErr)
	}
	if isDerivedConfigCache(path) {
		return skipDerivedConfigCache(entry)
	}
	if len(capture.snapshot) >= maxBaselineEntries {
		return fmt.Errorf("configuration baseline exceeded %d entries", maxBaselineEntries)
	}
	info, err := entry.Info()
	if err != nil {
		return fmt.Errorf("inspect configuration path %q: %w", path, err)
	}
	return captureSnapshotEntry(capture.snapshot, path, info)
}

func captureSnapshotEntry(snapshot TreeSnapshot, path configPath, info fs.FileInfo) error {
	value, err := snapshotEntry(string(path), info)
	if err != nil {
		return err
	}
	snapshot[string(path)] = value
	return nil
}

func skipDerivedConfigCache(entry fs.DirEntry) error {
	if entry.IsDir() {
		return fs.SkipDir
	}
	return nil
}

// derivedConfigCachePaths are the parts of a guarded configuration root that
// the Claude Code CLI rewrites on its own schedule, for reasons no Desktop run
// causes. They are exempt from the digest for exactly the reason ~/.claude.json
// is not a root at all (see defaultConfigurationRoots): a recording machine has
// a Claude Code session running by definition, and a guard that fails on
// somebody else's cache sweep reports a change the driver did not make.
//
// Measured on 2026-09-06: cell 2-17 drove its turn, captured a 400 KB
// transcript and every piece of evidence, and was then failed by
// `app-wide Desktop configuration changed: unexpected path
// ".../.claude/plugins/cache/claude-plugins-official/frontend-design/
// 85cce0381e78/.orphaned_at"` — a marker the CLI's own in-use sweep drops on a
// cache entry it has orphaned. That subtree held 3984 entries at the time.
//
// The exemption is deliberately narrow: it names derived cache and nothing
// else. The plugin SET — config.json, installed_plugins.json,
// known_marketplaces.json, blocklist.json, marketplaces/, repos/, data/ — stays
// guarded, and that is what a run installing or removing a plugin would move.
var derivedConfigCachePaths = []string{
	// The plugin content cache. Populated, swept and orphan-marked by the CLI.
	filepath.Join(".claude", "plugins", "cache"),
	// The timestamp of the last in-use sweep. Its whole content is a clock read.
	filepath.Join(".claude", "plugins", ".last_inuse_sweep"),
	// A cache of the marketplace catalog, refetched on the CLI's own schedule.
	filepath.Join(".claude", "plugins", "plugin-catalog-cache.json"),
}

// isDerivedConfigCache matches a full path SUFFIX, never a bare directory name:
// "cache" alone would exempt any directory anywhere that happened to be called
// that, which is how a narrow exemption turns into a hole.
func isDerivedConfigCache(path configPath) bool {
	clean := filepath.Clean(string(path))
	for _, suffix := range derivedConfigCachePaths {
		if strings.HasSuffix(clean, string(filepath.Separator)+suffix) {
			return true
		}
	}
	return false
}

func snapshotEntry(path string, info fs.FileInfo) (SnapshotEntry, error) {
	mode := uint32(info.Mode())
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(path)
		if err != nil {
			return SnapshotEntry{}, fmt.Errorf("read configuration symlink %q: %w", path, err)
		}
		return SnapshotEntry{Kind: "symlink", Mode: mode, Link: target}, nil
	case info.IsDir():
		return SnapshotEntry{Kind: "directory", Mode: mode}, nil
	case info.Mode().IsRegular():
		data, err := os.ReadFile(path)
		if err != nil {
			return SnapshotEntry{}, fmt.Errorf("read configuration file %q: %w", path, err)
		}
		sum := sha256.Sum256(data)
		return SnapshotEntry{Kind: "file", Mode: mode, Digest: hex.EncodeToString(sum[:])}, nil
	default:
		return SnapshotEntry{}, fmt.Errorf("unsupported configuration path type at %q", path)
	}
}

func VerifyTreeSnapshot(expected TreeSnapshot) error {
	roots := snapshotRoots(expected)
	actual, err := CaptureTreeSnapshot(roots)
	if err != nil {
		return err
	}
	if equalTreeSnapshots(expected, actual) {
		return nil
	}
	return fmt.Errorf("app-wide Desktop configuration changed: %s", firstSnapshotDifference(expected, actual))
}

func snapshotRoots(snapshot TreeSnapshot) []string {
	paths := make([]string, 0, len(snapshot))
	for path := range snapshot {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var roots []string
	for _, path := range paths {
		covered := false
		for _, root := range roots {
			if path == root || isWithin(path, root) {
				covered = true
				break
			}
		}
		if !covered {
			roots = append(roots, path)
		}
	}
	return roots
}

func isWithin(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative)
}

func equalTreeSnapshots(left, right TreeSnapshot) bool {
	if len(left) != len(right) {
		return false
	}
	for path, expected := range left {
		if actual, ok := right[path]; !ok || actual != expected {
			return false
		}
	}
	return true
}

func firstSnapshotDifference(expected, actual TreeSnapshot) string {
	paths := make([]string, 0, len(expected)+len(actual))
	seen := map[string]struct{}{}
	for path := range expected {
		seen[path] = struct{}{}
		paths = append(paths, path)
	}
	for path := range actual {
		if _, ok := seen[path]; !ok {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		before, beforeOK := expected[path]
		after, afterOK := actual[path]
		if !beforeOK {
			return fmt.Sprintf("unexpected path %q", path)
		}
		if !afterOK {
			return fmt.Sprintf("missing path %q", path)
		}
		if before != after {
			return fmt.Sprintf("bytes, type, target, or mode differ at %q", path)
		}
	}
	return "unknown difference"
}

// verifyUserProjectEntries is the app-wide guard for ~/.claude.json.
//
// That file cannot be held byte-exact across a run. It belongs to the Claude
// Code CLI, which rewrites caches, counters and per-project entries whenever
// any session on the machine does anything — and a recording machine has one
// running by definition. Holding it exact failed every run for a reason that
// was never the driver's doing.
//
// So this asserts the two things the driver IS answerable for: it took no
// project entry away, and every entry that appeared names its own scratch
// workspace. Foreign churn passes; a loss does not.
func verifyUserProjectEntries(before, after []byte, workspace string) error {
	baseline, err := userProjectEntries(before, "baseline")
	if err != nil {
		return err
	}
	current, err := userProjectEntries(after, "current")
	if err != nil {
		return err
	}
	for path := range baseline {
		if _, kept := current[path]; !kept {
			return fmt.Errorf("the run removed the Claude Code project entry for %q", path)
		}
	}
	for path := range current {
		if _, existed := baseline[path]; existed {
			continue
		}
		if path != workspace && !isWithin(path, workspace) {
			return fmt.Errorf(
				"the run added a Claude Code project entry for %q, which is not its workspace %q",
				path, workspace)
		}
	}
	return nil
}

// userProjectEntries reads the "projects" object. An unreadable file is an
// error: a guard that cannot look must never report what a guard that looked
// and found nothing reports.
func userProjectEntries(data []byte, which string) (map[string]json.RawMessage, error) {
	var document struct {
		Projects map[string]json.RawMessage `json:"projects"`
	}
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("read the %s Claude Code configuration: %w", which, err)
	}
	return document.Projects, nil
}

// verifyNoKeyLosses is the guard for a shared JSON configuration file the
// driver must not damage but cannot hold still.
//
// Claude Desktop's config.json carries token caches, allowlist timestamps and
// window layout; ~/.claude.json carries the CLI's caches and counters. Both are
// rewritten by their owners while a run is in progress, so byte equality
// reports a change on every run and proves nothing. A key that DISAPPEARS is
// different: no ordinary churn removes one, and a driver that truncated or
// rewrote the file would.
func verifyNoKeyLosses(before, after []byte, what string) error {
	baseline, err := topLevelKeys(before, "baseline "+what)
	if err != nil {
		return err
	}
	current, err := topLevelKeys(after, "current "+what)
	if err != nil {
		return err
	}
	var lost []string
	for key := range baseline {
		if _, kept := current[key]; !kept {
			lost = append(lost, key)
		}
	}
	if len(lost) > 0 {
		// Sorted, so the message is the same every time it is produced.
		sort.Strings(lost)
		return fmt.Errorf("the run removed %d key(s) from the %s: %s",
			len(lost), what, strings.Join(lost, ", "))
	}
	return nil
}

func topLevelKeys(data []byte, which string) (map[string]json.RawMessage, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("read the %s: %w", which, err)
	}
	return document, nil
}
