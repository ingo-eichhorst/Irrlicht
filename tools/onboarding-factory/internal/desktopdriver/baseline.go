package desktopdriver

import (
	"crypto/sha256"
	"encoding/hex"
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
