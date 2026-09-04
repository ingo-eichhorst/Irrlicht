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
	if err := os.MkdirAll(filepath.Join(build, "cwd"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(build, "recordings"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(build, "prompt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".build", "helper"), []byte("helper"), 0o700); err != nil {
		t.Fatal(err)
	}
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
	if _, err := parseOptions(mutated); err == nil || !strings.Contains(err.Error(), "workspace must resolve under") {
		t.Fatalf("parseOptions() escape error = %v", err)
	}
}

func TestParseOptionsRejectsBuildSymlinkEscape(t *testing.T) {
	repo := t.TempDir()
	cell := filepath.Join(repo, ".build", "refresh", "cell")
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cell, "recordings"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cell, "prompt"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".build", "helper"), []byte("helper"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(cell, "cwd")); err != nil {
		t.Fatal(err)
	}
	args := []string{
		"--repo-root", repo,
		"--staging", cell,
		"--workspace", filepath.Join(cell, "cwd"),
		"--prompt-file", filepath.Join(cell, "prompt"),
		"--helper", filepath.Join(repo, ".build", "helper"),
		"--daemon-address", "127.0.0.1:34567",
		"--recordings", filepath.Join(cell, "recordings"),
		"--irrlicht-version", "0.7.0+test",
		"--timeout", "60s",
	}
	if _, err := parseOptions(args); err == nil || !strings.Contains(err.Error(), "workspace must resolve under") {
		t.Fatalf("parseOptions() symlink escape error = %v", err)
	}
}
