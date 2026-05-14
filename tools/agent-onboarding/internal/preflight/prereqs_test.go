package preflight

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCheck_noManifestIsOK(t *testing.T) {
	root := t.TempDir()
	r, err := Check(root, "newagent")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r.Status != "no-manifest" {
		t.Errorf("status=%q", r.Status)
	}
}

func TestCheck_manifestWithoutOKFails(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "replaydata", "agents", "ag"))
	mustWrite(t, filepath.Join(root, "replaydata", "agents", "ag", "prerequisites.md"), "step 1\n")

	r, err := Check(root, "ag")
	if !errors.Is(err, ErrPrereqsNotMet) {
		t.Fatalf("want ErrPrereqsNotMet, got %v", err)
	}
	if r.Status != "missing-ok" {
		t.Errorf("status=%q", r.Status)
	}
	if r.Detail == "" {
		t.Error("detail empty")
	}
}

func TestCheck_okFileSatisfies(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(root, "replaydata", "agents", "ag", "prerequisites.md")
	mustMkdir(t, filepath.Dir(manifest))
	mustWrite(t, manifest, "step 1\n")

	okPath := filepath.Join(root, ".agent-onboarding", "prereqs-ag.ok")
	mustMkdir(t, filepath.Dir(okPath))
	// Touch the ok file slightly after the manifest.
	time.Sleep(20 * time.Millisecond)
	mustWrite(t, okPath, "")

	r, err := Check(root, "ag")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if r.Status != "ok" {
		t.Errorf("status=%q (detail=%q)", r.Status, r.Detail)
	}
}

func TestCheck_staleOKFails(t *testing.T) {
	root := t.TempDir()
	manifest := filepath.Join(root, "replaydata", "agents", "ag", "prerequisites.md")
	mustMkdir(t, filepath.Dir(manifest))
	okPath := filepath.Join(root, ".agent-onboarding", "prereqs-ag.ok")
	mustMkdir(t, filepath.Dir(okPath))

	// Write OK first, then update the manifest so manifest.ModTime > ok.ModTime.
	mustWrite(t, okPath, "")
	time.Sleep(20 * time.Millisecond)
	mustWrite(t, manifest, "step 1 (updated)\n")

	r, err := Check(root, "ag")
	if !errors.Is(err, ErrPrereqsNotMet) {
		t.Fatalf("want ErrPrereqsNotMet, got %v", err)
	}
	if r.Status != "stale-ok" {
		t.Errorf("status=%q", r.Status)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
