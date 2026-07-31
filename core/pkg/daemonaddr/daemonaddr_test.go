package daemonaddr

import "testing"

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
