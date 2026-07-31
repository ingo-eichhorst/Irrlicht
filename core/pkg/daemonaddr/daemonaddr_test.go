package daemonaddr

import (
	"os"
	"path/filepath"
	"testing"
)

// isolateStateTree points the addr-file lookup at an empty temp directory, so
// a test can never read the production daemon's addr file off the dev machine
// running it. Returns the directory for tests that want to plant a file.
func isolateStateTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv(envHome, dir)
	return dir
}

// publishAddr plants an addr file the way a running daemon does.
func publishAddr(t *testing.T, content string) {
	t.Helper()
	dir := isolateStateTree(t)
	if err := os.WriteFile(filepath.Join(dir, addrFileName), []byte(content), 0o600); err != nil {
		t.Fatalf("write addr file: %v", err)
	}
}

// TestDefaultsAgree pins the two spellings of the default together: PortOf's
// fallback and the address the daemon binds must never drift apart, or an
// unset environment would resolve a hook endpoint the daemon does not serve.
func TestDefaultsAgree(t *testing.T) {
	if got := PortOf(defaultBindAddr); got != defaultPort {
		t.Errorf("PortOf(%q) = %d, want defaultPort %d", defaultBindAddr, got, defaultPort)
	}
}

func TestResolveBindAddr(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", defaultBindAddr},
		{"garbage", defaultBindAddr},
		{"127.0.0.1:7837", "127.0.0.1:7837"},
		{"0.0.0.0:7837", "0.0.0.0:7837"},
		{":7837", ":7837"},
		{"127.0.0.1:7838", "127.0.0.1:7838"},
	}
	for _, tt := range tests {
		if got := resolveBindAddr(tt.in); got != tt.want {
			t.Errorf("resolveBindAddr(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPortOf(t *testing.T) {
	tests := []struct {
		in   string
		want int
	}{
		{"127.0.0.1:7837", 7837},
		{"127.0.0.1:7838", 7838},
		{":7838", 7838},
		{"0.0.0.0:9000", 9000},
		{"garbage", defaultPort},
		{"127.0.0.1:notaport", defaultPort},
		// An ephemeral request has no value a client can be pointed at.
		{"127.0.0.1:0", defaultPort},
		{"127.0.0.1:99999", defaultPort},
	}
	for _, tt := range tests {
		if got := PortOf(tt.in); got != tt.want {
			t.Errorf("PortOf(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestLocalURLFollowsEnv(t *testing.T) {
	tests := []struct {
		name, bindAddr, want string
	}{
		{"unset falls back to the default port", "", "http://localhost:7837/x"},
		{"alternate port", "127.0.0.1:7838", "http://localhost:7838/x"},
		// A daemon on 0.0.0.0 is still reached at loopback; never point a
		// hook at a wildcard address.
		{"wildcard bind still yields a loopback URL", "0.0.0.0:7900", "http://localhost:7900/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(EnvBindAddr, tt.bindAddr)
			if got := LocalURL("/x"); got != tt.want {
				t.Errorf("LocalURL(\"/x\") = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestLocalURLIgnoresAddrFile is a lock, not a defect test: an in-daemon
// installer must resolve from the environment alone, so an addr file left
// behind by a killed daemon can never repoint a user's hooks.
func TestLocalURLIgnoresAddrFile(t *testing.T) {
	publishAddr(t, "127.0.0.1:7999\n")
	t.Setenv(EnvBindAddr, "")
	if got, want := LocalURL("/x"), "http://localhost:7837/x"; got != want {
		t.Errorf("LocalURL(\"/x\") = %q, want %q — the addr file must not reach in-daemon resolution", got, want)
	}
}

func TestClientURLPrefersExplicitBindAddr(t *testing.T) {
	publishAddr(t, "127.0.0.1:7999\n")
	t.Setenv(EnvBindAddr, "127.0.0.1:7838")
	if got, want := ClientURL("/x"), "http://localhost:7838/x"; got != want {
		t.Errorf("ClientURL(\"/x\") = %q, want %q", got, want)
	}
}

// TestClientURLFallsBackToAddrFile covers the invocation with no inherited
// environment — the common one, since nothing in the app launches the CLI.
func TestClientURLFallsBackToAddrFile(t *testing.T) {
	tests := []struct {
		name, published, want string
	}{
		{"alternate port", "127.0.0.1:7838\n", "http://localhost:7838/x"},
		{"no trailing newline", "127.0.0.1:7838", "http://localhost:7838/x"},
		// The file carries the port the listener actually got, so a client
		// reaches an ephemeral-port daemon the env alone could not name.
		{"ephemeral port resolved by the OS", "127.0.0.1:52341\n", "http://localhost:52341/x"},
		// A wildcard-bound daemon is still reached at loopback.
		{"wildcard bind", "0.0.0.0:7900\n", "http://localhost:7900/x"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			publishAddr(t, tt.published)
			t.Setenv(EnvBindAddr, "")
			if got := ClientURL("/x"); got != tt.want {
				t.Errorf("ClientURL(\"/x\") = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestClientURLFallsBackToDefault pins every way the ladder can run out of
// evidence: it lands on the default port rather than failing or dialing
// nothing.
func TestClientURLFallsBackToDefault(t *testing.T) {
	tests := []struct {
		name, bindAddr string
		published      string // "" plants no file at all
	}{
		{"nothing set and no daemon running", "", ""},
		{"addr file holds garbage", "", "not-an-address\n"},
		{"addr file holds an unresolved ephemeral request", "", "127.0.0.1:0\n"},
		{"addr file is empty", "", "\n"},
		{"malformed bind addr and no file", "garbage", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.published == "" {
				isolateStateTree(t)
			} else {
				publishAddr(t, tt.published)
			}
			t.Setenv(EnvBindAddr, tt.bindAddr)
			if got, want := ClientURL("/x"), "http://localhost:7837/x"; got != want {
				t.Errorf("ClientURL(\"/x\") = %q, want %q", got, want)
			}
		})
	}
}

// TestClientURLIgnoresMalformedBindAddrInFavorOfFile: a typo'd env var must
// not shadow the daemon that is demonstrably running.
func TestClientURLIgnoresMalformedBindAddrInFavorOfFile(t *testing.T) {
	publishAddr(t, "127.0.0.1:7838\n")
	t.Setenv(EnvBindAddr, "127.0.0.1:notaport")
	if got, want := ClientURL("/x"), "http://localhost:7838/x"; got != want {
		t.Errorf("ClientURL(\"/x\") = %q, want %q", got, want)
	}
}
