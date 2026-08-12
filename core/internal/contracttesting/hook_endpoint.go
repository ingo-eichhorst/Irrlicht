// hook_endpoint.go holds the issue #1178 contract every hook-installing agent
// adapter must satisfy: what it writes into the user's agent config cannot go
// stale when the daemon moves. Like AssertPermissionGated it exists because the
// obligation is a runtime choice an adapter has to opt into — a static check
// cannot see whether an installer baked ":7837" in — and because the
// alternative is a third adapter copying a test file, or (issue #1216, more
// likely) not copying it and silently shipping a hardcoded port.
//
// There are two ways to satisfy that, and the contract holds an adapter to
// whichever one it declares in HookInstaller.Delivery (issue #1453):
//
//   - DeliveryURL — the installed entry CARRIES the daemon's address, so the
//     address it carries has to be the resolved one and a daemon on another
//     port has to rewrite it in place. This is claudecode, codex and copilot.
//   - DeliveryAddressFree — the installed entry carries NO address, because it
//     names the `irrlichd hook-post` beacon (core/pkg/hookbeacon) which reads
//     the daemon's address from the addr file at fire time. This is the route
//     an agent whose hooks only support `command` delivery must take.
//
// The second mode is not an exemption from the first. Address-free delivery
// makes the #1178 stale-port class INEXPRESSIBLE rather than merely fixing it
// again — there is no port in the config to go stale — so the port obligations
// become unsatisfiable by construction, and three of the four measurably fail
// against a working beacon. What the mode asserts instead is that the property
// really was obtained (the line varies with nothing and carries nothing
// address-shaped) and that the failure the beacon NEWLY admits is policed: an
// entry naming a binary path that is no longer the running one must be
// rewritten in place, exactly as a stale port must be. Only the FIRST of the
// four sub-tests differs between the modes; the other three are the same code,
// parameterised by which seed stands in for "what a differently-situated daemon
// left behind" and which address assertion applies.
//
// Declaring the WRONG mode cannot pass quietly, which is the objection
// hook_receipt.go raises against flag-shaped options ("a wiring that silently
// ignored a flag would pass it by accident") and the reason this one is safe:
// the two modes' first obligation are mutually exclusive assertions about the
// same two strings — DeliveryURL requires the delivery to DIFFER between two
// bind addresses, DeliveryAddressFree requires it to be IDENTICAL. A beacon
// adapter that forgets the field is held to the URL obligations and fails; a
// URL adapter that sets it is held to the address-free ones and fails. There is
// no value of Delivery an adapter can be graded under vacuously.
package contracttesting

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/pkg/daemonaddr"
)

// AltBindAddr is the non-default bind address AssertHookEndpointFollowsBindAddr
// installs against — the port a `separate`-mode dev daemon and the onboarding
// factory's coexist recorder use. The contract only needs it to differ from the
// default; the specific value keeps failure messages recognizable.
const AltBindAddr = "127.0.0.1:7838"

// DeliveryMode selects which half of the contract an adapter is held to. See
// the file comment for why a mode field rather than a second family: only
// obligation 1 differs, and no value of it grades an adapter vacuously.
type DeliveryMode int

const (
	// DeliveryURL is the zero value: the installed entry carries the daemon's
	// address, whether as an http entry's `url` or embedded in a curl
	// command. Every adapter wired before #1453 is this, and stays this
	// without touching its call site.
	DeliveryURL DeliveryMode = iota

	// DeliveryAddressFree is beacon delivery: the installed entry names the
	// irrlichd binary and an adapter segment, and the address is resolved in
	// the beacon process at fire time. Requires ForeignInstall, because the
	// drift such an install can suffer is a binary path rather than a port
	// and the contract has no environment variable that produces it.
	DeliveryAddressFree
)

// HookInstaller wires one adapter's hook installer into
// AssertHookEndpointFollowsBindAddr. Every field is required except Delivery,
// whose zero value is a route, and ForeignInstall, which only the address-free
// route uses. Between them they cover the only three things that genuinely
// differ per adapter: where the
// config file lives and which env var relocates it (SettingsPath), the
// install/uninstall entry points, and how an installed entry's endpoint is read
// back (EndpointOf) — Claude Code writes a native `type: http` entry whose
// endpoint is its `url`, Codex a `type: command` curl string that embeds it.
type HookInstaller struct {
	// Delivery declares the adapter's delivery route, and therefore which
	// obligations apply. See DeliveryURL / DeliveryAddressFree.
	Delivery DeliveryMode

	// SettingsPath points the installer at a temp config home — by t.Setenv of
	// whatever env var relocates it ($HOME, $CODEX_HOME) — and returns the
	// absolute path of the settings file the installer will write.
	//
	// The contract calls it FIRST in every sub-test and sets
	// daemonaddr.EnvBindAddr itself afterwards, so an implementation that pins
	// the bind address as a side effect (claudecode's withTempHome does) is
	// safe: the contract's own value always wins.
	SettingsPath func(t *testing.T) string

	// Sentinel is the port-independent substring marking our entries — the
	// same value the adapter passes to hookjson.Config. Used to confirm
	// uninstall left nothing of ours behind.
	Sentinel string

	// Events is the adapter's installed hook event list. Every obligation is
	// checked against every event, so an installer that resolves the port for
	// some events and not others is caught.
	Events []string

	// Entry builds the adapter's canonical inner hook object at the CURRENTLY
	// resolved bind address — the same builder the adapter hands to
	// hookjson.Config. Used to learn what endpoint the adapter would install on
	// a given bind address without going near the filesystem.
	Entry func() map[string]interface{}

	// EndpointOf extracts the delivery string carrying the endpoint from an
	// inner hook object: the `url` of an http entry, the whole `command` of a
	// curl entry. Compared for equality between entries, and searched for the
	// port, so returning the whole command is correct.
	EndpointOf func(hook map[string]interface{}) string

	// EnsureInstalled and Uninstall are the adapter's exported entry points.
	EnsureInstalled func() (bool, error)
	Uninstall       func() (bool, error)

	// ForeignInstall leaves behind what a DIFFERENTLY-SITUATED daemon would
	// have installed — the state obligations 3 and 4 both start from. It is
	// required in DeliveryAddressFree mode and ignored in DeliveryURL, where
	// the contract produces the same thing itself by pinning the default bind
	// address and calling EnsureInstalled.
	//
	// For a beacon adapter that means installing against a DIFFERENT irrlichd
	// binary path — hookbeacon.Command takes one explicitly for this reason.
	// Prefer a path that does not exist: an absolute path that has stopped
	// existing is the drift the beacon newly admits (#1373), and it needs no
	// second executable to arrange.
	//
	// Neither way of getting it wrong passes: an entry that is already
	// canonical leaves obligation 3 with modified=false, and one that does not
	// carry Sentinel is not recognized as ours, so EnsureInstalled appends a
	// second matcher group and onlyEntry fails on the group count. The seed is
	// additionally cross-checked against Sentinel before either obligation
	// runs.
	ForeignInstall func() (bool, error)
}

// AssertHookEndpointFollowsBindAddr runs the issue #1178 contract against h, in
// whichever of the two delivery routes h declares.
//
// Four sub-tests run in both routes. Only the FIRST differs:
//
//  1. DeliveryURL — the delivery tracks the bind address and carries the
//     resolved port. DeliveryAddressFree — it does not vary with the bind
//     address at all and carries nothing address-shaped.
//  2. an install writes what the adapter would install now, and is idempotent.
//  3. an entry left by a differently-situated daemon (another port; another
//     irrlichd binary) is still recognized as ours — the sentinel names neither
//     — and rewritten IN PLACE, with no duplicate matcher group appended.
//  4. uninstall is not scoped to the installing daemon, or `irrlichd
//     --uninstall-hooks` run from one leaves another's entries behind.
//
// What it deliberately does NOT assert in address-free mode is that the beacon
// resolves a live daemon's address at fire time. That is the beacon's own
// obligation and is covered where the beacon lives, by
// hookbeacon.TestPostResolvesTheDaemonAddressAtFireTime; this family grades the
// ADAPTER, and an adapter cannot get it wrong or right.
func AssertHookEndpointFollowsBindAddr(t *testing.T, h HookInstaller) {
	t.Helper()
	r := rulesFor(t, h)
	t.Run(r.firstName, func(t *testing.T) { r.first(realT(t), h) })
	t.Run(r.installName, func(t *testing.T) { assertInstallWritesCanonicalDelivery(realT(t), h, r) })
	t.Run(r.upgradeName, func(t *testing.T) { assertUpgradedInPlace(realT(t), h, r) })
	t.Run(r.uninstallName, func(t *testing.T) { assertUninstallIsNotForeignScoped(realT(t), h, r) })
}

// deliveryRules is everything that differs between the two routes, selected
// ONCE from h.Delivery.
//
// It replaced a mode field re-consulted in four separate functions. Three of
// those needed a defensive `default:` arm that could never actually run —
// t.Fatalf is runtime.Goexit, so an unrecognized mode terminates the parent
// before any sub-test is created — so the guards read as safety while being
// dead. Selecting once makes "only the first sub-test differs" a fact about the
// code rather than a promise four switches have to keep, and leaves exactly one
// place a third route has to be taught.
type deliveryRules struct {
	// firstName and first are the one obligation that genuinely differs.
	firstName string
	first     func(t armT, h HookInstaller)

	// The remaining three sub-tests are the same functions in both routes and
	// differ only in what they are called.
	installName, upgradeName, uninstallName string

	// assertAddress is the route's address obligation, applied per event.
	assertAddress func(t reporter, what, delivery string)

	// seed leaves behind what a differently-situated daemon installed, and
	// foreign names it for the failure messages of the two obligations built
	// on it.
	seed    func(t armT, h HookInstaller)
	foreign string
}

func rulesFor(t reporter, h HookInstaller) deliveryRules {
	t.Helper()
	switch h.Delivery {
	case DeliveryURL:
		return deliveryRules{
			firstName:     "endpoint_follows_bind_addr",
			first:         assertEndpointFollowsBindAddr,
			installName:   "install_writes_resolved_port",
			upgradeName:   "default_port_install_upgraded_in_place",
			uninstallName: "uninstall_is_not_port_scoped",
			assertAddress: assertResolvedPort,
			seed:          seedDefaultPortInstall,
			foreign:       "a default-port install",
		}
	case DeliveryAddressFree:
		if h.ForeignInstall == nil {
			t.Fatal("DeliveryAddressFree requires ForeignInstall — there is no bind address the contract can vary to produce a drifted install for it")
		}
		return deliveryRules{
			firstName:     "delivery_carries_no_address",
			first:         assertDeliveryCarriesNoAddress,
			installName:   "install_writes_address_free_delivery",
			upgradeName:   "foreign_binary_install_upgraded_in_place",
			uninstallName: "uninstall_is_not_binary_scoped",
			assertAddress: assertNoAddress,
			seed:          seedForeignBinaryInstall,
			foreign:       "an install naming a different irrlichd binary",
		}
	default:
		t.Fatalf("unhandled DeliveryMode %d — every route must name the obligations it runs", h.Delivery)
		return deliveryRules{}
	}
}

// deliveriesOnBothAddrs returns what the adapter would install on the default
// and on the alternate bind address. The two obligation-1 bodies differ only in
// how they compare the pair.
//
// It relocates the config home even though it writes nothing: an Entry() that
// reads anything home-relative must never be evaluated against the developer's
// real agent config. And it confirms EndpointOf read something at all before
// either body compares — see assertDeliveryIsOurs.
func deliveriesOnBothAddrs(t armT, h HookInstaller) (onDefault, onAlt string) {
	t.Helper()
	h.SettingsPath(t.fixtures)
	onDefault = deliveryOn(t, h, "")
	onAlt = deliveryOn(t, h, AltBindAddr)
	assertDeliveryIsOurs(t, h, "delivery on the default bind address", onDefault)
	return onDefault, onAlt
}

// assertDeliveryCarriesNoAddress is DeliveryAddressFree's obligation 1, and the
// exact inverse of assertEndpointFollowsBindAddr: rather than checking that the
// delivery TRACKS the bind address, it checks that it is independent of it and
// carries no address at all. Both halves are needed. Equality alone would pass
// for an installer that hardcoded "http://localhost:7837/..." — the original
// #1178 defect, which is address-bearing and equally invariant — and the
// no-address check alone would pass for a line that varied for some other
// reason and so could still be written differently by two daemons.
func assertDeliveryCarriesNoAddress(t armT, h HookInstaller) {
	t.Helper()
	onDefault, onAlt := deliveriesOnBothAddrs(t, h)
	if onDefault != onAlt {
		t.Fatalf("delivery differs between the default and %s bind addresses (%q vs %q) — an address-free install must not vary with %s, or a daemon on another port writes a different line and the whole point of the route is lost",
			AltBindAddr, onDefault, onAlt, daemonaddr.EnvBindAddr)
	}
	// onDefault and onAlt are equal past the Fatalf, so one check covers both.
	assertNoAddress(t, "delivery on both bind addresses", onDefault)
}

// assertEndpointFollowsBindAddr is DeliveryURL's obligation 1 at the source:
// the endpoint the adapter would install is a function of the bind address at
// all. An installer that hardcodes the port fails here first, and most legibly.
func assertEndpointFollowsBindAddr(t armT, h HookInstaller) {
	t.Helper()
	onDefault, onAlt := deliveriesOnBothAddrs(t, h)
	if onDefault == onAlt {
		t.Fatalf("delivery is identical on the default and %s bind addresses (%q) — the endpoint does not follow %s",
			AltBindAddr, onDefault, daemonaddr.EnvBindAddr)
	}
	assertResolvedPort(t, "delivery on "+AltBindAddr, onAlt)
}

// assertInstallWritesCanonicalDelivery is obligation 2: what actually lands in
// the settings file, plus idempotency on an unchanged bind address. Shared by
// both routes — only r.assertAddress differs.
func assertInstallWritesCanonicalDelivery(t armT, h HookInstaller, r deliveryRules) {
	t.Helper()
	path := h.SettingsPath(t.fixtures)
	want := deliveryOn(t, h, AltBindAddr)

	if _, err := h.EnsureInstalled(); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	assertEveryEventDelivers(t, h, r, path, want)

	modified, err := h.EnsureInstalled()
	if err != nil {
		t.Fatalf("second EnsureInstalled: %v", err)
	}
	if modified {
		t.Error("second install on the same bind address: got modified=true, want false")
	}
}

// assertUpgradedInPlace is obligation 3: an install left by a differently
// situated daemon is recognized as ours and repointed, not orphaned beside a
// duplicate group. What "differently situated" means is r.seed's business.
func assertUpgradedInPlace(t armT, h HookInstaller, r deliveryRules) {
	t.Helper()
	path := h.SettingsPath(t.fixtures)
	seedForeignInstall(t, h, r, path)
	want := deliveryOn(t, h, AltBindAddr)

	modified, err := h.EnsureInstalled()
	if err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	if !modified {
		t.Fatalf("expected modified=true when repointing %s — an install left unrepaired here reports healthy while delivering nothing, and no later daemon start converges on it", r.foreign)
	}
	// assertEveryEventDelivers fails on a second matcher group, which is the
	// "upgraded in place, not duplicated" half of this obligation.
	assertEveryEventDelivers(t, h, r, path, want)
}

// assertUninstallIsNotForeignScoped is obligation 4: a daemon cleans up entries
// a differently-situated daemon installed. In DeliveryURL mode that is another
// port; in DeliveryAddressFree mode another binary path, where it additionally
// means Uninstall must not depend on resolving a binary path at all —
// `site/install.sh --uninstall` removes the binary without calling
// --uninstall-hooks, so the entry commonly outlives the path it names.
func assertUninstallIsNotForeignScoped(t armT, h HookInstaller, r deliveryRules) {
	t.Helper()
	path := h.SettingsPath(t.fixtures)
	seedForeignInstall(t, h, r, path)
	t.fixtures.Setenv(daemonaddr.EnvBindAddr, AltBindAddr)

	modified, err := h.Uninstall()
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if !modified {
		t.Fatalf("expected modified=true removing %s from a daemon on %s", r.foreign, AltBindAddr)
	}
	hooksMap := readHooksMap(t, path)
	for _, event := range h.Events {
		if hookjson.HasOurHook(hooksMap, event, h.Sentinel) {
			t.Errorf("event %s: %s survived uninstall", event, r.foreign)
		}
	}
}

// --- helpers ---

// deliveryOn pins the daemon bind address to bindAddr for the rest of the
// sub-test and returns the delivery string the adapter would install there.
func deliveryOn(t armT, h HookInstaller, bindAddr string) string {
	t.Helper()
	t.fixtures.Setenv(daemonaddr.EnvBindAddr, bindAddr)
	return h.EndpointOf(h.Entry())
}

// assertEveryEventDelivers checks each event's installed entry against want and
// against the route's address obligation. Both matter: equality catches an
// installer writing a stale shape, the address check catches the case want
// itself is wrong — because the endpoint stopped following the bind address, or
// because an "address-free" delivery grew an address.
func assertEveryEventDelivers(t armT, h HookInstaller, r deliveryRules, path, want string) {
	t.Helper()
	hooksMap := readHooksMap(t, path)
	for _, event := range h.Events {
		got := h.EndpointOf(onlyEntry(t, hooksMap, event))
		assertDeliveryIsOurs(t, h, "event "+event, got)
		if got != want {
			t.Errorf("event %s: installed delivery = %q, want %q", event, got, want)
		}
		r.assertAddress(t, "event "+event, got)
	}
}

// assertDeliveryIsOurs fails unless the delivery string carries the sentinel.
//
// It is the vacuity guard for EndpointOf, and it is load-bearing in
// DeliveryAddressFree mode specifically. Inverting obligation 1 from "the two
// deliveries must DIFFER" to "must be IDENTICAL" removed the only assertion
// that proved EndpointOf observed anything at all: an EndpointOf reading a key
// the entry does not carry returns "" for both, and "" is invariant, carries no
// address, and equals the "" that the next sub-test compares against — so the
// whole mode passes 4/4 while asserting nothing. That is not a hypothetical
// slip; the key is the one field that genuinely differs between the wirings
// already in the tree (claudecode and copilot read `url`, codex reads
// `command`).
//
// The sentinel is the right probe because hookjson identifies OUR entries by
// finding it in exactly the field EndpointOf is supposed to be reading, so a
// delivery that does not contain it is either not ours or not being read.
func assertDeliveryIsOurs(t reporter, h HookInstaller, what, delivery string) {
	t.Helper()
	if !strings.Contains(delivery, h.Sentinel) {
		t.Fatalf("%s: %q does not carry Sentinel %q — EndpointOf is not reading the field the installer writes, so every obligation below it is graded against the wrong string",
			what, delivery, h.Sentinel)
	}
}

// hookRoutePrefix is the path every hook receiver the daemon serves is
// registered under. An address-free delivery must not contain it: an adapter
// that embeds the daemon's HTTP route is delivering by URL whatever it declares.
const hookRoutePrefix = "/api/v1/"

// addressShaped matches anything that could pin a delivery to one daemon — a
// scheme, a loopback host, a port-shaped `:<digits>`, a port passed as a flag
// argument, either concrete daemon port as a bare word, or the hook route.
//
// It is deliberately broader than the ":<port>/" fragment assertResolvedPort
// looks for, because the failure it guards is an adapter reintroducing an
// address in SOME spelling — including one no daemon of ours currently uses.
//
// Two of the branches are not decoration. A HARDCODED port is invariant across
// bind addresses, so it slips past the equality half of obligation 1 as well:
// `--port 7837` and a bare `7837` would each otherwise have satisfied BOTH
// halves while writing exactly the #1178 defect into the user's config.
//
// This is the superset of hookbeacon's own TestCommandCarriesNoAddress, which
// states the same rule as a substring list ("7837", "7838", "http://",
// "https://", "localhost", "127.0.0.1", "/api/v1/") over the rendered command.
// Two spellings of one rule can disagree, so this one is built to contain that
// list rather than to parallel it — the ports are taken from daemonaddr instead
// of retyped, so they cannot drift at all.
//
// It is applied to the WHOLE delivery, which for a beacon begins with a
// shell-quoted absolute path, so it is fail-closed on two known false
// positives: a binary path containing `:<digits>` or the literal `localhost`
// (`/home/localhost-dev/bin/irrlichd`) reads as address-shaped. That is the
// deliberate direction — the failure message prints the matched fragment and
// the whole delivery, so the cause is legible — and no layout we ship trips it
// (`/Applications/Irrlicht.app/...`, `/opt/homebrew/bin/...`, `~/.local/bin/...`
// and go's `/var/folders/.../go-build.../b001/...` test paths are all clean).
// The bare-port branch is word-bounded for that reason: a go-build directory
// like `go-build17837293` contains "7837" but is not it.
var addressShaped = regexp.MustCompile(
	`(?i)https?://` +
		`|\b(?:localhost|127\.0\.0\.1|0\.0\.0\.0)\b` +
		`|\[::1\]` +
		`|:\d{2,5}(?:\b|/)` +
		`|\bports?\b[\s=:]*\d{2,5}\b` +
		`|` + regexp.QuoteMeta(hookRoutePrefix) +
		fmt.Sprintf(`|\b(?:%d|%d)\b`, daemonaddr.PortOf(""), daemonaddr.PortOf(AltBindAddr)),
)

// assertNoAddress fails when delivery carries anything address-shaped. This is
// the DeliveryAddressFree counterpart of assertResolvedPort, and it is what
// turns "the beacon happens not to embed a port" into a property the next
// beacon adapter cannot quietly lose.
func assertNoAddress(t reporter, what, delivery string) {
	t.Helper()
	if m := addressShaped.FindString(delivery); m != "" {
		t.Errorf("%s: %q carries the address-shaped fragment %q — an address-free delivery must resolve the daemon at fire time, or it is a stale-port install (#1178) wearing a command entry's clothes", what, delivery, m)
	}
}

// assertResolvedPort fails unless delivery carries the alternate port and no
// longer carries the default one.
func assertResolvedPort(t reporter, what, delivery string) {
	t.Helper()
	if want := portMarker(AltBindAddr); !strings.Contains(delivery, want) {
		t.Errorf("%s: %q does not carry the resolved port %q", what, delivery, want)
	}
	if stale := portMarker(""); strings.Contains(delivery, stale) {
		t.Errorf("%s: %q still carries the default port %q", what, delivery, stale)
	}
}

// portMarker renders the ":<port>/" fragment of bindAddr's endpoint. An empty
// bindAddr yields the default port, which is what daemonaddr falls back to.
func portMarker(bindAddr string) string {
	return fmt.Sprintf(":%d/", daemonaddr.PortOf(bindAddr))
}

// seedDefaultPortInstall is DeliveryURL's seed: the adapter's own installer run
// under a default-port environment.
func seedDefaultPortInstall(t armT, h HookInstaller) {
	t.Helper()
	t.fixtures.Setenv(daemonaddr.EnvBindAddr, "")
	if _, err := h.EnsureInstalled(); err != nil {
		t.Fatalf("seeding a default-port install: %v", err)
	}
}

// seedForeignBinaryInstall is DeliveryAddressFree's seed. It has to be
// adapter-supplied because no environment variable makes an installer name a
// different binary, where the bind address is one the contract can set itself.
func seedForeignBinaryInstall(t armT, h HookInstaller) {
	t.Helper()
	if _, err := h.ForeignInstall(); err != nil {
		t.Fatalf("seeding an install naming a different irrlichd binary: %v", err)
	}
}

// seedForeignInstall runs the route's seed and then cross-checks it.
//
// Both seeds go through a real installer. Synthesizing the matcher groups by
// hand instead would be a second implementation of what hookjson.EnsureInstalled
// already does, and one that could drift from the shape the adapter actually
// writes.
func seedForeignInstall(t armT, h HookInstaller, r deliveryRules, path string) {
	t.Helper()
	r.seed(t, h)

	// Sentinel is the one field nothing else cross-checks, and a wrong value
	// makes the uninstall-survival assertion pass vacuously — HasOurHook would
	// simply find nothing, for the wrong reason. Confirming the seeded install
	// is matched by the declared sentinel turns that silent hole into a wiring
	// error.
	seeded := readHooksMap(t, path)
	for _, event := range h.Events {
		if !hookjson.HasOurHook(seeded, event, h.Sentinel) {
			t.Fatalf("event %s: %s is not matched by Sentinel %q — the contract is wired wrong", event, r.foreign, h.Sentinel)
		}
	}
}

// onlyEntry returns the single inner hook of the event's single matcher group,
// failing on any other shape — which is the assertion, not incidental: an
// upgrade that appends a second group instead of rewriting in place shows up
// here as a group count of 2.
func onlyEntry(t reporter, hooksMap map[string]interface{}, event string) map[string]interface{} {
	t.Helper()
	groups, ok := hooksMap[event].([]interface{})
	if !ok {
		t.Fatalf("event %s: missing from the settings file or not an array", event)
	}
	if len(groups) != 1 {
		t.Fatalf("event %s: expected exactly 1 matcher group (in-place upgrade, no duplicate appended), got %d", event, len(groups))
	}
	group, ok := groups[0].(map[string]interface{})
	if !ok {
		t.Fatalf("event %s: matcher group is not an object: %v", event, groups[0])
	}
	entries, ok := group["hooks"].([]interface{})
	if !ok || len(entries) != 1 {
		t.Fatalf("event %s: expected exactly 1 inner hook, got %v", event, group["hooks"])
	}
	entry, ok := entries[0].(map[string]interface{})
	if !ok {
		t.Fatalf("event %s: inner hook is not an object: %v", event, entries[0])
	}
	return entry
}

// readHooksMap reads the settings file through the same codec the installers
// use and returns its top-level "hooks" object (nil when absent).
func readHooksMap(t reporter, path string) map[string]interface{} {
	t.Helper()
	settings, err := hookjson.ReadSettings(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	hooksMap, _ := settings["hooks"].(map[string]interface{})
	return hooksMap
}
