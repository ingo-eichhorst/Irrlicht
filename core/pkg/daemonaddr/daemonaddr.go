// Package daemonaddr is the single source of truth for the address the
// irrlicht daemon binds to, and for the loopback URL that same-machine callers
// deliver to it on.
//
// It answers that question for two audiences, and the difference is which
// evidence each one is allowed to trust:
//
//   - Code running *inside* the daemon — the agent hook and statusline
//     installers — uses LocalURL, which resolves purely from
//     IRRLICHT_BIND_ADDR. The daemon reads the same knob to bind, so a daemon
//     on an alternate port installs hooks that reach *it* rather than whatever
//     happens to own the default port. Before issue #1178 the installers baked
//     "localhost:7837" in as a constant, which meant a dev or
//     onboarding-factory daemon on :7838 silently delivered every hook to the
//     production daemon.
//   - A separate client process — the irrlicht-focus CLI, or anything else
//     talking to a daemon it did not start — uses ClientURL, which prefers an
//     explicit IRRLICHT_BIND_ADDR but falls back to the address file the
//     running daemon publishes (<IRRLICHT_HOME>/irrlichd.addr). A client
//     cannot assume it inherited the daemon's environment: the macOS app plants
//     IRRLICHT_BIND_ADDR only in the environment of the daemon child it spawns
//     (DaemonManager.buildDaemonEnv), never in its own, and it does not launch
//     irrlicht-focus at all — that CLI is typed by a human, in a shell that
//     usually has no such variable. Env-only resolution there would just be the
//     hardcoded :7837 wearing a different hat.
//
// Env before file, deliberately: an explicit IRRLICHT_BIND_ADDR is the caller
// naming the daemon it means, and the addr file can outlive a crashed daemon
// (the daemon removes it on clean shutdown, not on SIGKILL). LocalURL never
// consults the file for the same reason — an in-daemon installer must not
// repoint a user's hooks at a port some earlier daemon happened to bind.
//
// Resolution is a pure function of the environment (plus, for clients, that one
// file) rather than a resolved address threaded down from the composition root,
// because the installers run from permission Apply closures on the values
// `agents.All()` builds — and that constructor is shared with tooling that has
// no daemon at all (the onboarding-factory viewer, `irrlichd --diagnose`),
// where a bind address would be a meaningless parameter. It also builds each
// permission's Detail prose as a plain string at construction time, which a
// threaded address would force into a func. Reading the same env var the daemon
// reads keeps the two in agreement without either cost, and without
// process-wide mutable state.
//
// One configuration is exempt from the env path: IRRLICHT_BIND_ADDR=127.0.0.1:0
// asks the OS for an ephemeral port, whose value does not exist until the
// listener is open, so PortOf (and therefore LocalURL) falls back to the
// default port there. Only the headless smoke test uses :0, and it runs against
// a temp HOME with permissions unanswered, so it installs nothing — but a :0
// daemon that did install would write an endpoint it does not serve. ClientURL
// has no such gap: the addr file carries the port the listener actually got.
//
// A note on the other spelling. IRRLICHT_DAEMON_PORT is the macOS app's own
// knob for the same number, deliberately kept separate: the Swift side cannot
// call into this package, and DaemonManager derives
// IRRLICHT_BIND_ADDR=127.0.0.1:<IRRLICHT_DAEMON_PORT> for the daemon it spawns,
// so the two cannot disagree. site/docs/configuration.html documents which to
// set.
package daemonaddr

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// EnvBindAddr overrides the daemon's TCP bind address, e.g. "0.0.0.0:7837" to
// expose it on the LAN, or "127.0.0.1:7838" to run a dev daemon alongside
// production.
const EnvBindAddr = "IRRLICHT_BIND_ADDR"

const (
	// envHome relocates the daemon's state tree, and with it the addr file.
	// Mirrors irrlichd's own dataDir rule (core/cmd/irrlichd/paths.go).
	envHome = "IRRLICHT_HOME"

	// addrFileName is the file the daemon publishes its bound address to,
	// next to its socket, once every route is served — and removes on
	// shutdown so it cannot outlive the daemon (core/cmd/irrlichd).
	addrFileName = "irrlichd.addr"
)

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
	if p, ok := fixedPortOf(addr); ok {
		return p
	}
	return defaultPort
}

// fixedPortOf extracts a port a client can be pointed at from a host:port
// address, reporting false when the address names none — so callers with a
// second source of truth can try it instead of taking the default.
func fixedPortOf(addr string) (int, bool) {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(p)
	if err != nil || !isFixedPort(n) {
		return 0, false
	}
	return n, true
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

// ClientURL builds the loopback URL a separate same-machine client process
// should call for path (which must start with "/") — the CLI counterpart to
// LocalURL. It resolves the port from an explicit IRRLICHT_BIND_ADDR first,
// then from the running daemon's published addr file, then the default. See
// the package doc for why a client gets the extra source and the daemon
// itself does not.
func ClientURL(path string) string {
	return fmt.Sprintf("http://localhost:%d%s", clientPort(), path)
}

// clientPort resolves the port a client should dial, in order of how much the
// value can be trusted to name the daemon the caller means.
func clientPort() int {
	if p, ok := fixedPortOf(os.Getenv(EnvBindAddr)); ok {
		return p
	}
	if p, ok := fixedPortOf(publishedAddr()); ok {
		return p
	}
	return defaultPort
}

// publishedAddr returns the address the running daemon wrote to its addr file,
// or "" when there is no readable file — no daemon, a daemon too old to
// publish one, or a state tree this process cannot see.
func publishedAddr() string {
	b, err := os.ReadFile(addrFilePath())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// addrFilePath locates the addr file the way irrlichd places it: under
// IRRLICHT_HOME when set, else the default state tree.
func addrFilePath() string {
	if v := os.Getenv(envHome); v != "" {
		return filepath.Join(v, addrFileName)
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join("/tmp/irrlicht", addrFileName)
	}
	return filepath.Join(home, ".local", "share", "irrlicht", addrFileName)
}
