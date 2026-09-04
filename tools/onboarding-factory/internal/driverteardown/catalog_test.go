package driverteardown

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdaptersRefusesAnUnreadableDriverEntry(t *testing.T) {
	root := t.TempDir()
	validDir := filepath.Join(root, AgentsDir, "valid")
	brokenDir := filepath.Join(root, AgentsDir, "broken")
	for _, dir := range []string{validDir, brokenDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create adapter directory %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(validDir, DriverName), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("write valid driver: %v", err)
	}
	brokenDriver := filepath.Join(brokenDir, DriverName)
	if err := os.Symlink(DriverName, brokenDriver); err != nil {
		t.Fatalf("create self-referential driver link: %v", err)
	}

	adapters, err := Adapters(root)
	if err == nil {
		t.Fatalf("Adapters returned %v and no error; the broken driver was silently omitted", adapters)
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Fatalf("Adapters error %q does not name the broken adapter", err)
	}
}

func TestAdaptersRefusesADanglingDriverEntry(t *testing.T) {
	root := t.TempDir()
	validDir := filepath.Join(root, AgentsDir, "valid")
	brokenDir := filepath.Join(root, AgentsDir, "broken")
	for _, dir := range []string{validDir, brokenDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create adapter directory %s: %v", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(validDir, DriverName), []byte("#!/bin/sh\n"), 0o644); err != nil {
		t.Fatalf("write valid driver: %v", err)
	}
	brokenDriver := filepath.Join(brokenDir, DriverName)
	if err := os.Symlink("missing-driver.sh", brokenDriver); err != nil {
		t.Fatalf("create dangling driver link: %v", err)
	}

	adapters, err := Adapters(root)
	if err == nil {
		t.Fatalf("Adapters returned %v and no error; the dangling driver was silently omitted", adapters)
	}
	if !strings.Contains(err.Error(), "broken") {
		t.Fatalf("Adapters error %q does not name the broken adapter", err)
	}
}
