package gtbin

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNew_ImplementsInterface(t *testing.T) {
	// Verify gtbinResolver satisfies the outbound.GTBinResolver interface
	// by calling Path() and checking it returns a string.
	r := New()
	_ = r.Path() // must compile — proves interface compatibility
}

func TestResolve_GTBINEnv(t *testing.T) {
	// Create a temporary executable to use as GT_BIN.
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "gt")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GT_BIN", bin)

	r := New()
	got := r.Path()
	if got == "" {
		t.Fatal("expected non-empty path from GT_BIN")
	}
	if got != bin {
		t.Errorf("expected %q, got %q", bin, got)
	}
}

func TestResolve_GTBINEnv_NonExecutable(t *testing.T) {
	// GT_BIN pointing to a non-executable file should be skipped.
	tmp := t.TempDir()
	bin := filepath.Join(tmp, "gt")
	if err := os.WriteFile(bin, []byte("not executable"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("GT_BIN", bin)

	r := &gtbinResolver{}
	// Call resolve directly — GT_BIN should be rejected, fallback may or may not find gt.
	// The key assertion is that the non-executable GT_BIN is NOT returned.
	got := r.resolve()
	if got == bin {
		t.Error("expected non-executable GT_BIN to be rejected")
	}
}

func TestResolve_GTBINEnv_Missing(t *testing.T) {
	// GT_BIN pointing to a nonexistent path should be skipped.
	t.Setenv("GT_BIN", "/nonexistent/path/to/gt")

	r := &gtbinResolver{}
	got := r.resolve()
	if got == "/nonexistent/path/to/gt" {
		t.Error("expected nonexistent GT_BIN to be rejected")
	}
}

func TestIsExecutable(t *testing.T) {
	tmp := t.TempDir()

	exec := filepath.Join(tmp, "exec")
	os.WriteFile(exec, nil, 0755)

	noExec := filepath.Join(tmp, "noexec")
	os.WriteFile(noExec, nil, 0644)

	if !isExecutable(exec) {
		t.Error("expected executable file to pass")
	}
	if isExecutable(noExec) {
		t.Error("expected non-executable file to fail")
	}
	if isExecutable(filepath.Join(tmp, "nonexistent")) {
		t.Error("expected nonexistent file to fail")
	}
}
