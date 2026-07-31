package daemonaddr

import "testing"

func TestResolveBindAddr(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"", DefaultBindAddr},
		{"garbage", DefaultBindAddr},
		{"127.0.0.1:7837", "127.0.0.1:7837"},
		{"0.0.0.0:7837", "0.0.0.0:7837"},
		{":7837", ":7837"},
		{"127.0.0.1:7838", "127.0.0.1:7838"},
	}
	for _, tt := range tests {
		if got := ResolveBindAddr(tt.in); got != tt.want {
			t.Errorf("ResolveBindAddr(%q) = %q, want %q", tt.in, got, tt.want)
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
		{"garbage", DefaultPort},
		{"127.0.0.1:notaport", DefaultPort},
		// An ephemeral request has no value a client can be pointed at.
		{"127.0.0.1:0", DefaultPort},
		{"127.0.0.1:99999", DefaultPort},
	}
	for _, tt := range tests {
		if got := PortOf(tt.in); got != tt.want {
			t.Errorf("PortOf(%q) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

func TestPortAndLocalURLFollowEnv(t *testing.T) {
	t.Run("unset falls back to the default port", func(t *testing.T) {
		t.Setenv(EnvBindAddr, "")
		if got := Port(); got != DefaultPort {
			t.Errorf("Port() = %d, want %d", got, DefaultPort)
		}
		if got, want := LocalURL("/api/v1/hooks/codex"), "http://localhost:7837/api/v1/hooks/codex"; got != want {
			t.Errorf("LocalURL = %q, want %q", got, want)
		}
	})

	t.Run("alternate port", func(t *testing.T) {
		t.Setenv(EnvBindAddr, "127.0.0.1:7838")
		if got := Port(); got != 7838 {
			t.Errorf("Port() = %d, want 7838", got)
		}
		if got, want := LocalURL("/api/v1/hooks/codex"), "http://localhost:7838/api/v1/hooks/codex"; got != want {
			t.Errorf("LocalURL = %q, want %q", got, want)
		}
	})

	t.Run("wildcard bind still yields a loopback client URL", func(t *testing.T) {
		t.Setenv(EnvBindAddr, "0.0.0.0:7900")
		if got, want := LocalURL("/api/v1/hooks/claudecode"), "http://localhost:7900/api/v1/hooks/claudecode"; got != want {
			t.Errorf("LocalURL = %q, want %q", got, want)
		}
	})
}
