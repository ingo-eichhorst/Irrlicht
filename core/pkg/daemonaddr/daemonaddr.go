// Package daemonaddr is the single source of truth for the address the
// irrlicht daemon binds to, and for the loopback URL that the agent hooks it
// installs must deliver to.
//
// Both sides read the same knob (IRRLICHT_BIND_ADDR), so a daemon on an
// alternate port installs hooks that reach *it* rather than whatever happens
// to own the default port. Before issue #1178 the installers baked
// "localhost:7837" in as a constant, which meant a dev or onboarding-factory
// daemon on :7838 silently delivered every hook to the production daemon.
//
// Resolution is a pure function of the environment rather than a resolved
// address threaded down from the composition root, because the installers run
// from permission Apply closures on the values `agents.All()` builds — and
// that constructor is shared with tooling that has no daemon at all (the
// onboarding-factory viewer, `irrlichd --diagnose`), where a bind address
// would be a meaningless parameter. It also builds each permission's Detail
// prose as a plain string at construction time, which a threaded address would
// force into a func. Reading the same env var the daemon reads keeps the two
// in agreement without either cost, and without process-wide mutable state.
//
// One configuration is still exempt: IRRLICHT_BIND_ADDR=127.0.0.1:0 asks the
// OS for an ephemeral port, whose value does not exist until the listener is
// open. PortOf falls back to DefaultPort there. Only the headless smoke test
// uses :0, and it runs against a temp HOME with permissions unanswered, so it
// installs nothing — but a :0 daemon that did install would write an endpoint
// it does not serve.
package daemonaddr

import (
	"fmt"
	"net"
	"os"
	"strconv"
)

// EnvBindAddr overrides the daemon's TCP bind address, e.g. "0.0.0.0:7837" to
// expose it on the LAN, or "127.0.0.1:7838" to run a dev daemon alongside
// production.
const EnvBindAddr = "IRRLICHT_BIND_ADDR"

const (
	// defaultPort is the daemon's TCP port when EnvBindAddr is unset. It is
	// the port in defaultBindAddr; TestDefaultsAgree pins them together.
	defaultPort = 7837

	// defaultBindAddr is the default bind address: loopback only, so the
	// daemon is not exposed to the network unless the user opts in.
	defaultBindAddr = "127.0.0.1:7837"
)

// resolveBindAddr returns the TCP bind address for the daemon given the raw
// EnvBindAddr value. An empty or malformed value falls back to
// defaultBindAddr — a typo must not silently bind somewhere unexpected.
func resolveBindAddr(envValue string) string {
	if envValue == "" {
		return defaultBindAddr
	}
	if _, _, err := net.SplitHostPort(envValue); err != nil {
		return defaultBindAddr
	}
	return envValue
}

// BindAddr returns the resolved bind address for the current environment.
func BindAddr() string {
	return resolveBindAddr(os.Getenv(EnvBindAddr))
}

// PortOf extracts the TCP port from a host:port address, falling back to the
// default when the address is unparseable or carries no usable fixed port.
// Port 0 falls back too — see the package doc.
func PortOf(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return defaultPort
	}
	n, err := strconv.Atoi(p)
	if err != nil || !isFixedPort(n) {
		return defaultPort
	}
	return n
}

// isFixedPort reports whether n is a port a client can be pointed at ahead of
// the daemon's bind: in range, and not 0 — which does not name a port at all,
// it asks the OS to pick one.
func isFixedPort(n int) bool {
	return n > 0 && n <= 65535
}

// LocalURL builds the loopback URL a same-machine client should call for path
// (which must start with "/"). The host is always loopback no matter what the
// daemon binds to: a daemon on 0.0.0.0 is still reachable at localhost, and a
// hook installed into a user's agent config must never point at a wildcard
// address.
func LocalURL(path string) string {
	return fmt.Sprintf("http://localhost:%d%s", PortOf(BindAddr()), path)
}
