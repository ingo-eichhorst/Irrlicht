package accountquota

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileCredentialResolver_ReadsAndTrimsSecret(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credential")
	if err := os.WriteFile(path, []byte("  sk-test-123\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	r := FileCredentialResolver{Path: func() (string, error) { return path, nil }}
	cred, err := r.Resolve(context.Background())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := cred.Reveal(); got != "sk-test-123" {
		t.Fatalf("Reveal() = %q, want %q", got, "sk-test-123")
	}
}

func TestFileCredentialResolver_MissingFile(t *testing.T) {
	r := FileCredentialResolver{Path: func() (string, error) { return "/nonexistent/path/for/test", nil }}
	if _, err := r.Resolve(context.Background()); err == nil {
		t.Fatal("expected an error for a missing credential file")
	}
}

func TestFileCredentialResolver_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credential")
	if err := os.WriteFile(path, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	r := FileCredentialResolver{Path: func() (string, error) { return path, nil }}
	if _, err := r.Resolve(context.Background()); err == nil {
		t.Fatal("expected an error for an all-whitespace credential file")
	}
}

func TestFileCredentialResolver_NoPathResolver(t *testing.T) {
	r := FileCredentialResolver{}
	if _, err := r.Resolve(context.Background()); err == nil {
		t.Fatal("expected an error when Path is nil")
	}
}
