package daemonaddr

import (
	"os"
	"path/filepath"
	"testing"
)

// TestClientTargetsANamedDaemon covers the three rungs of the client ladder,
// which is what decides whether a caller is about to dial a daemon somebody
// pointed it at or a stranger on the default port (#1425).
func TestClientTargetsANamedDaemon(t *testing.T) {
	t.Run("an explicit bind addr names one", func(t *testing.T) {
		t.Setenv(envHome, t.TempDir()) // no addr file
		t.Setenv(EnvBindAddr, "127.0.0.1:7999")
		if !ClientTargetsANamedDaemon() {
			t.Error("an explicit IRRLICHT_BIND_ADDR was not treated as naming a daemon")
		}
	})

	t.Run("a published addr file names one", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv(envHome, home)
		t.Setenv(EnvBindAddr, "")
		if err := os.WriteFile(filepath.Join(home, addrFileName), []byte("127.0.0.1:7998\n"), 0o600); err != nil {
			t.Fatalf("write addr file: %v", err)
		}
		if !ClientTargetsANamedDaemon() {
			t.Error("a published addr file was not treated as naming a daemon")
		}
	})

	t.Run("nothing at all names none", func(t *testing.T) {
		t.Setenv(envHome, t.TempDir()) // no addr file
		t.Setenv(EnvBindAddr, "")
		if ClientTargetsANamedDaemon() {
			t.Error("the default-port fallback was reported as a named daemon")
		}
	})

	t.Run("a malformed bind addr with nothing else names none", func(t *testing.T) {
		t.Setenv(envHome, t.TempDir())
		t.Setenv(EnvBindAddr, "not-a-host-port")
		if ClientTargetsANamedDaemon() {
			t.Error("a malformed IRRLICHT_BIND_ADDR was reported as naming a daemon")
		}
	})
}
