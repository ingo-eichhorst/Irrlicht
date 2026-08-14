package push

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVAPIDCreatedOnceAndStableAcrossRestarts(t *testing.T) {
	dir := t.TempDir()
	svc, err := NewService(dir, newTestClock().now)
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	pub := svc.VAPIDPublicKey()
	if pub == "" {
		t.Fatal("VAPIDPublicKey is empty")
	}
	if _, err := os.Stat(filepath.Join(dir, vapidFilename)); err != nil {
		t.Fatalf("vapid file not created: %v", err)
	}

	// A restart loads the same identity — subscriptions are bound to it.
	restarted, err := NewService(dir, newTestClock().now)
	if err != nil {
		t.Fatalf("NewService (restart): %v", err)
	}
	if got := restarted.VAPIDPublicKey(); got != pub {
		t.Fatalf("public key changed across restart: %q → %q", pub, got)
	}
}

func TestVAPIDFileMode0600(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes not meaningful on Windows")
	}
	_, _, dir := newTestService(t)
	fi, err := os.Stat(filepath.Join(dir, vapidFilename))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Fatalf("vapid file mode = %o, want 600", perm)
	}
}

// TestVAPIDCorruptFileIsAnError pins the refusal to self-heal: a LOST vapid
// file regenerates (phones re-subscribe), but a CORRUPT one must be a human
// decision — regenerating would orphan every existing subscription
// (docs/mobile-notifications-arc42.md §8.6).
func TestVAPIDCorruptFileIsAnError(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"garbage", "not json at all"},
		// Valid JSON whose halves disagree: the private scalar derives a
		// public key other than the stored one.
		{"inconsistent halves", `{"private_key":"AQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","public_key":"BAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, vapidFilename)
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := NewService(dir, newTestClock().now)
			if err == nil {
				t.Fatal("NewService loaded a corrupt VAPID file")
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("error %q does not name the file %q", err, path)
			}
			// And it must NOT have regenerated over it.
			data, readErr := os.ReadFile(path)
			if readErr != nil || string(data) != tc.content {
				t.Fatalf("corrupt file was rewritten: %q err=%v", data, readErr)
			}
		})
	}
}
