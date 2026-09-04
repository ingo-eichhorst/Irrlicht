package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseOptionsConfinesGeneratedPathsToRepositoryBuild(t *testing.T) {
	repo := t.TempDir()
	build := filepath.Join(repo, ".build", "refresh", "cell")
	args := []string{
		"--repo-root", repo,
		"--staging", build,
		"--workspace", filepath.Join(build, "cwd"),
		"--prompt-file", filepath.Join(build, "prompt"),
		"--helper", filepath.Join(repo, ".build", "helper"),
		"--daemon-address", "127.0.0.1:34567",
		"--recordings", filepath.Join(build, "recordings"),
		"--irrlicht-version", "0.7.0+test",
		"--timeout", "60s",
	}
	if _, err := parseOptions(args); err != nil {
		t.Fatalf("parseOptions() error = %v", err)
	}
	mutated := append([]string(nil), args...)
	mutated[5] = filepath.Join(repo, "worktree")
	if _, err := parseOptions(mutated); err == nil || !strings.Contains(err.Error(), "workspace must stay under") {
		t.Fatalf("parseOptions() escape error = %v", err)
	}
}

func TestRunLockRefusesAConcurrentDesktopDriver(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".build"), 0o700); err != nil {
		t.Fatal(err)
	}
	first, err := acquireRunLock(repo)
	if err != nil {
		t.Fatalf("first acquireRunLock() error = %v", err)
	}
	defer releaseRunLock(first)
	second, err := acquireRunLock(repo)
	if err == nil || !strings.Contains(err.Error(), "another Desktop driver holds") {
		releaseRunLock(second)
		t.Fatalf("second acquireRunLock() error = %v", err)
	}
}
