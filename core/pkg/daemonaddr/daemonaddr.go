// Package daemonaddr is the single source of truth for the address the
// irrlicht daemon binds to, and for the loopback URL that same-machine
// clients — agent hooks, the Claude Code statusline feed — must deliver to.
//
// Both sides read the same knob (IRRLICHT_BIND_ADDR), so a daemon on an
// alternate port installs hooks that reach *it* rather than whatever happens
// to own the default port. Before issue #1178 the installers baked
// "localhost:7837" in as a constant, which meant a dev or onboarding-factory
// daemon on :7838 silently delivered every hook to the production daemon.
//
// Resolution is a pure function of the environment, deliberately: the hook
// installers run from permission Apply closures inside adapter packages, which
// are constructed before the TCP listener exists, so there is no resolved
// address to thread down to them. Reading the same env var they would have
// been handed keeps the two in agreement without a startup-ordering
// constraint or process-wide mutable state.
package daemonaddr

import (
	"fmt"
	"net"
	"os"
	"strconv"
)

const (
	// EnvBindAddr overrides the daemon's TCP bind address, e.g. "0.0.0.0:7837"
	// to expose it on the LAN, or "127.0.0.1:7838" to run a dev daemon
	// alongside production.
	EnvBindAddr = "IRRLICHT_BIND_ADDR"

	// DefaultPort is the daemon's TCP port when EnvBindAddr is unset.
	DefaultPort = 7837

	// DefaultBindAddr is the default bind address: loopback only, so the
	// daemon is not exposed to the network unless the user opts in.
	DefaultBindAddr = "127.0.0.1:7837"
)

// ResolveBindAddr returns the TCP bind address for the daemon given the raw
// EnvBindAddr value. An empty or malformed value falls back to
// DefaultBindAddr — a typo must not silently bind somewhere unexpected.
func ResolveBindAddr(envValue string) string {
	if envValue == "" {
		return DefaultBindAddr
	}
	if _, _, err := net.SplitHostPort(envValue); err != nil {
		return DefaultBindAddr
	}
	return envValue
}

// BindAddr returns the resolved bind address for the current environment.
func BindAddr() string {
	return ResolveBindAddr(os.Getenv(EnvBindAddr))
}

// PortOf extracts the TCP port from a host:port address, falling back to
// DefaultPort when the address is unparseable or carries no usable fixed
// port. Port 0 falls back too: it asks the OS for an ephemeral port, which
// has no value a client can be pointed at ahead of the actual bind.
func PortOf(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return DefaultPort
	}
	n, err := strconv.Atoi(p)
	if err != nil || n <= 0 || n > 65535 {
		return DefaultPort
	}
	return n
}

// Port returns the TCP port same-machine clients should reach the daemon on,
// resolved from the current environment.
func Port() int {
	return PortOf(BindAddr())
}

// LocalURL builds the loopback URL a same-machine client should call for path
// (which must start with "/"). The host is always loopback no matter what the
// daemon binds to: a daemon on 0.0.0.0 is still reachable at localhost, and a
// hook installed into a user's agent config must never point at a wildcard
// address.
func LocalURL(path string) string {
	return fmt.Sprintf("http://localhost:%d%s", Port(), path)
}
