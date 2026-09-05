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

func CaptureTreeSnapshot(roots []string) (TreeSnapshot, error) {
	snapshot := TreeSnapshot{}
	for _, root := range roots {
		if !filepath.IsAbs(root) {
			return nil, fmt.Errorf("configuration baseline root is not absolute: %q", root)
		}
		if err := captureRoot(snapshot, filepath.Clean(root)); err != nil {
			return nil, err
		}
	}
	return snapshot, nil
}

func captureRoot(snapshot TreeSnapshot, root string) error {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		snapshot[root] = SnapshotEntry{Kind: "absent"}
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect configuration path %q: %w", root, err)
	}
	if !info.IsDir() {
		entry, err := snapshotEntry(root, info)
		if err != nil {
			return err
		}
		snapshot[root] = entry
		return nil
	}
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return fmt.Errorf("walk configuration path %q: %w", path, walkErr)
		}
		if len(snapshot) >= maxBaselineEntries {
			return fmt.Errorf("configuration baseline exceeded %d entries", maxBaselineEntries)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect configuration path %q: %w", path, err)
		}
		value, err := snapshotEntry(path, info)
		if err != nil {
			return err
		}
		snapshot[path] = value
		return nil
	})
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
