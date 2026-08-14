// hook_endpoint_selftest_test.go is the committed mutation evidence for
// AssertHookEndpointFollowsBindAddr (#1178/#1216, two-route since #1453),
// transcribed from PR #1240 and PR #1473 where it lived as prose (#1479).
//
// This family is why #1479 was filed. PR #1473's obligation 1 for the
// address-free route passed its own mutation 4 of 4: inverting it to "the two
// deliveries must be IDENTICAL" destroyed the only assertion proving EndpointOf
// had read anything, because an EndpointOf pointed at a key the entry does not
// carry returns "" — and "" is invariant, carries no address, and equals the ""
// the next sub-test compares against. The whole route passed while asserting
// nothing. assertDeliveryIsOurs is the guard that closed it, and
// TestEndpointGuard_DeliveryIsOurs is that mutation, now re-run on every build
// instead of recorded in a merged PR body.
//
// Obligations 2-4 are the same code in both delivery routes (only the address
// assertion and the seed differ — see deliveryRules), so they are driven once
// here, through the reference beacon installer, and cover both. Each of those
// three makes TWO claims, and the second of each is covered separately in
// TestEndpointArm_SecondAssertions — an installer can satisfy "writes the
// canonical line" while rewriting on every start, or "reports modified" while
// removing nothing.
//
// A fixture here is wrong in one way, which is not the same as reddening one
// assertion: a non-idempotent install also trips the delivery comparison. That
// is what mustReport's required message fragment is for — it grades WHICH
// assertion fired, not merely that something did.
package contracttesting

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/pkg/daemonaddr"
	"irrlicht/core/pkg/hookbeacon"
)

// --- fixtures for the address-only obligations ---

// selfTestEndpointSentinel is the port-independent substring marking our
// entries, as an adapter's hookjson.Config carries it.
const selfTestEndpointSentinel = "/api/v1/hooks/selftest"

// selfTestBeaconSentinel is the address-free route's marker. It deliberately
// carries no route and no port: an adapter whose SENTINEL is address-shaped
// cannot satisfy that route's obligation 1 no matter what its installer does,
// which is a property of the sentinel rather than of the delivery.
const selfTestBeaconSentinel = "hook-post selftest"

// sentinelFor picks the marker the declared route can actually satisfy.
func sentinelFor(mode DeliveryMode) string {
	if mode == DeliveryAddressFree {
		return selfTestBeaconSentinel
	}
	return selfTestEndpointSentinel
}

// entryInstaller is the smallest wiring the obligation-1 arms need: they never
// touch the filesystem, they only ask what the adapter WOULD install at two
// bind addresses. render is the mutation knob — one function from bind address
// to delivery string, which is exactly the surface #1178 and #1453 are about.
func entryInstaller(mode DeliveryMode, render func(bindAddr string) string) HookInstaller {
	return HookInstaller{
		Delivery: mode,
		SettingsPath: func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "settings.json")
		},
		Sentinel: sentinelFor(mode),
		Events:   []string{"Stop"},
		Entry: func() map[string]interface{} {
			// daemonaddr is read at Entry time, exactly as a real adapter's
			// builder reads it — the contract varies the env var, not this.
			return map[string]interface{}{"url": render(os.Getenv(daemonaddr.EnvBindAddr))}
		},
		EndpointOf: func(hook map[string]interface{}) string {
			u, _ := hook["url"].(string)
			return u
		},
		EnsureInstalled: func() (bool, error) { return false, nil },
		Uninstall:       func() (bool, error) { return false, nil },
	}
}

// correctURLDelivery is what a URL-route adapter installs: an endpoint carrying
// the RESOLVED bind address, so a daemon on another port writes a different
// line and an old one is repointed rather than left stale (#1178).
func correctURLDelivery(bindAddr string) string {
	return fmt.Sprintf("http://127.0.0.1:%d%s", daemonaddr.PortOf(bindAddr), selfTestEndpointSentinel)
}

// TestEndpointArm_EndpointFollowsBindAddr is #1178 itself: an installer that
// hardcodes the daemon's port. The delivery is then identical on both bind
// addresses, which is the first and most legible way it shows up.
func TestEndpointArm_EndpointFollowsBindAddr(t *testing.T) {
	t.Run("a_hardcoded_port_does_not_vary", func(t *testing.T) {
		h := entryInstaller(DeliveryURL, func(string) string {
			return "http://localhost:7837" + selfTestEndpointSentinel // #1178, verbatim
		})
		rec := observe(t, func(at armT) { assertEndpointFollowsBindAddr(at, h, h.SettingsPath(t)) })
		mustReport(t, rec, "delivery is identical on the default",
			"an installer that hardcodes :7837 — the #1178 defect")
	})

	// A delivery that varies but keeps the stale port slips past the equality
	// half, so the port half is asserted separately. Without this case an
	// installer that appended the port as a query parameter while leaving the
	// endpoint on 7837 would pass.
	t.Run("varies_but_carries_the_stale_port", func(t *testing.T) {
		h := entryInstaller(DeliveryURL, func(bindAddr string) string {
			return "http://localhost:7837" + selfTestEndpointSentinel + "?on=" + bindAddr
		})
		rec := observe(t, func(at armT) { assertEndpointFollowsBindAddr(at, h, h.SettingsPath(t)) })
		mustReport(t, rec, "still carries the default port",
			"a delivery that varies with the bind address but still points at :7837")
	})

	t.Run("a_correct_url_installer_passes", func(t *testing.T) {
		h := entryInstaller(DeliveryURL, correctURLDelivery)
		mustBeSilent(t, observe(t, func(at armT) { assertEndpointFollowsBindAddr(at, h, h.SettingsPath(t)) }),
			"an installer whose endpoint follows the bind address")
	})
}

// TestEndpointArm_DeliveryCarriesNoAddress is the address-free route's
// obligation 1, and it needs BOTH halves: equality alone passes for a
// hardcoded address (invariant, and the original defect), while the
// no-address check alone passes for a line that varies for some other reason.
//
// The last three cases are PR #1473's M7/M8/M9 — spellings of an address that
// the first draft of the matcher did not recognise. M7 is the one worth
// keeping: with `--port 7837` present, this obligation was GREEN and the run
// failed only incidentally elsewhere.
func TestEndpointArm_DeliveryCarriesNoAddress(t *testing.T) {
	beaconish := "irrlichd hook-post selftest"

	for name, c := range map[string]struct {
		render func(string) string
		want   string
		what   string
	}{
		"an_address_bearing_but_invariant_delivery": {
			render: func(string) string { return beaconish + " http://localhost:7837/x" },
			want:   "carries the address-shaped fragment",
			what:   "a hardcoded URL inside a command entry — #1178 wearing a command entry's clothes (M1a)",
		},
		"a_delivery_that_varies_with_the_bind_address": {
			render: func(bindAddr string) string { return beaconish + " on=" + bindAddr },
			want:   "delivery differs between the default",
			what:   "an address-free delivery that is not actually independent of the bind address (M1b)",
		},
		"a_colon_less_port_flag": {
			render: func(string) string { return beaconish + " --port 7837" },
			want:   "carries the address-shaped fragment",
			what:   "`--port 7837` — invariant and address-free by the naive reading, so it passed BOTH halves before the matcher was widened (M7)",
		},
		"the_daemon_port_as_a_bare_word": {
			render: func(string) string { return beaconish + " 7837" },
			want:   "carries the address-shaped fragment",
			what:   "the daemon port with no colon and no flag (M8)",
		},
		"the_daemons_hook_route": {
			render: func(string) string { return beaconish + " /api/v1/hooks/x" },
			want:   "carries the address-shaped fragment",
			what:   "the daemon's own HTTP route embedded in the line — delivering by URL whatever the adapter declares (M9)",
		},
	} {
		t.Run(name, func(t *testing.T) {
			// The sentinel has to survive every mutation, or assertDeliveryIsOurs
			// fires first and this case would be graded on the wrong obligation.
			h := entryInstaller(DeliveryAddressFree, func(bindAddr string) string {
				return selfTestBeaconSentinel + " " + c.render(bindAddr)
			})
			mustReport(t, observe(t, func(at armT) { assertDeliveryCarriesNoAddress(at, h, h.SettingsPath(t)) }),
				c.want, c.what)
		})
	}

	t.Run("a_correct_address_free_installer_passes", func(t *testing.T) {
		h := entryInstaller(DeliveryAddressFree, func(string) string {
			return "/opt/homebrew/bin/irrlichd " + selfTestBeaconSentinel
		})
		mustBeSilent(t, observe(t, func(at armT) { assertDeliveryCarriesNoAddress(at, h, h.SettingsPath(t)) }),
			"a delivery that varies with nothing and carries no address")
	})
}

// TestEndpointGuard_DeliveryIsOurs is the #1453 instance itself: an EndpointOf
// pointed at a key the installed entry does not carry. It returns "" for every
// bind address, and "" satisfies both halves of the address-free obligation and
// equals whatever the next sub-test compares it against.
//
// The two cases are PR #1473's M6a and M6b — the same wiring error graded under
// each route. Both matter because the key is the one field that genuinely
// differs between the wirings in the tree: claudecode and copilot read `url`,
// codex reads `command`.
func TestEndpointGuard_DeliveryIsOurs(t *testing.T) {
	for name, mode := range map[string]DeliveryMode{
		"address_free_route": DeliveryAddressFree,
		"url_route":          DeliveryURL,
	} {
		t.Run(name, func(t *testing.T) {
			h := entryInstaller(mode, correctURLDelivery)
			// The mutation: read a key the entry does not carry. Everything
			// else about this installer is correct.
			h.EndpointOf = referenceBeaconEndpointOf

			rec := observe(t, func(at armT) { deliveriesOnBothAddrs(at, h) })
			mustReport(t, rec, "EndpointOf is not reading the field the installer writes",
				"EndpointOf pointed at a key the entry does not carry — returns \"\", which is "+
					"invariant, address-free, and equal to itself, so it satisfied every "+
					"obligation while observing nothing (#1453)")
		})
	}
}

// TestEndpointAddressAssertions covers the two per-event address checks
// directly, since they are pure string predicates and each guards a different
// route. They are reached through obligations 1-3, but only for whichever route
// the wiring declares, so a regression in the unused one would be invisible.
func TestEndpointAddressAssertions(t *testing.T) {
	alt := fmt.Sprintf(":%d/", daemonaddr.PortOf(AltBindAddr))

	t.Run("assertNoAddress_reports_an_address", func(t *testing.T) {
		mustReport(t, observe(t, func(at armT) {
			assertNoAddress(at, "probe", "irrlichd hook-post http://localhost:7837/x")
		}), "carries the address-shaped fragment", "an address inside an address-free delivery")
	})
	t.Run("assertNoAddress_passes_an_address_free_line", func(t *testing.T) {
		mustBeSilent(t, observe(t, func(at armT) {
			assertNoAddress(at, "probe", "/opt/homebrew/bin/irrlichd hook-post selftest")
		}), "a beacon command carrying no address")
	})
	t.Run("assertResolvedPort_reports_a_missing_port", func(t *testing.T) {
		mustReport(t, observe(t, func(at armT) {
			assertResolvedPort(at, "probe", "http://localhost/x")
		}), "does not carry the resolved port", "a delivery with no port at all")
	})
	t.Run("assertResolvedPort_reports_the_stale_port", func(t *testing.T) {
		mustReport(t, observe(t, func(at armT) {
			assertResolvedPort(at, "probe", "http://localhost"+alt+"x and :7837/y")
		}), "still carries the default port", "a delivery still carrying :7837 alongside the resolved port")
	})
	t.Run("assertResolvedPort_passes_a_resolved_delivery", func(t *testing.T) {
		mustBeSilent(t, observe(t, func(at armT) {
			assertResolvedPort(at, "probe", "http://localhost"+alt+"x")
		}), "a delivery carrying only the resolved port")
	})
}

// --- fixtures for obligations 2-4, which do touch the filesystem ---

// correctBeaconInstaller is the reference wiring from
// hook_endpoint_addressfree_test.go, unchanged. Each mutation below takes a
// fresh copy and replaces exactly one entry point, so the deviation is the only
// difference from the installer the real contract test runs.
func correctBeaconInstaller() HookInstaller { return referenceBeaconInstaller() }

// TestEndpointArms_PassACorrectInstaller is this family's vacuity guard, the
// counterpart of TestDisclosureArms_PassACorrectDisclosure and
// TestVersionArms_PassACorrectDeclaration: every arm run against the reference
// installer, which is correct in every respect. An arm that reported
// unconditionally would satisfy every mutation in this file and read as
// excellent coverage.
func TestEndpointArms_PassACorrectInstaller(t *testing.T) {
	h := correctBeaconInstaller()
	r := rulesFor(t, h)

	for name, arm := range map[string]func(armT, string){
		r.firstName:     func(at armT, path string) { r.first(at, h, path) },
		r.installName:   func(at armT, path string) { assertInstallWritesCanonicalDelivery(at, h, r, path) },
		r.upgradeName:   func(at armT, path string) { assertUpgradedInPlace(at, h, r, path) },
		r.uninstallName: func(at armT, path string) { assertUninstallIsNotForeignScoped(at, h, r, path) },
	} {
		t.Run(name, func(t *testing.T) {
			// SettingsPath per sub-test, exactly as the contract calls it: a
			// fresh temp home, so no arm inherits another's installed state.
			path := h.SettingsPath(t)
			mustBeSilent(t, observe(t, func(at armT) { arm(at, path) }), "the reference beacon installer")
		})
	}
}

// TestEndpointArm_InstallWritesCanonicalDelivery is obligation 2 (PR #1473 M2):
// the installer writes a line other than the one Entry reports it would.
func TestEndpointArm_InstallWritesCanonicalDelivery(t *testing.T) {
	h := correctBeaconInstaller()
	r := rulesFor(t, h)

	t.Run("installs_a_line_other_than_the_canonical_one", func(t *testing.T) {
		mutated := correctBeaconInstaller()
		mutated.EnsureInstalled = referenceBeaconForeignInstall
		rec := observe(t, func(at armT) { assertInstallWritesCanonicalDelivery(at, mutated, r, mutated.SettingsPath(t)) })
		mustReport(t, rec, "installed delivery =",
			"an installer whose install does not match what Entry reports it would install (M2)")
	})
}

// TestEndpointArm_UpgradedInPlace is obligation 3 (PR #1473 M3a — the one that
// PR called load-bearing): an entry left by a differently-situated daemon must
// be recognized as ours and REPOINTED. An installer that already considers a
// foreign entry canonical leaves it in place and reports modified=false, which
// is the "reports healthy while delivering nothing" state #1178 is about.
func TestEndpointArm_UpgradedInPlace(t *testing.T) {
	h := correctBeaconInstaller()
	r := rulesFor(t, h)

	t.Run("a_foreign_entry_is_left_alone", func(t *testing.T) {
		mutated := correctBeaconInstaller()
		// IsCanonical never checks the binary path, so the seeded foreign
		// install is already "ours and current" and EnsureInstalled does
		// nothing.
		mutated.EnsureInstalled = func() (bool, error) {
			return beaconRunWith(func(cfg *hookjson.Config) {
				cfg.IsCanonical = func(map[string]interface{}) bool { return true }
			}, hookjson.EnsureInstalled)
		}
		rec := observe(t, func(at armT) { assertUpgradedInPlace(at, mutated, r, mutated.SettingsPath(t)) })
		mustReport(t, rec, "expected modified=true when repointing",
			"an IsCanonical that never checks the binary path, so an entry naming a since-deleted "+
				"irrlichd is accepted as current (M3a)")
	})
}

// TestEndpointArm_UninstallIsNotForeignScoped is obligation 4 (PR #1473 M4):
// `irrlichd --uninstall-hooks` run from one daemon must remove another's
// entries, or every daemon leaves its own behind. For the beacon route it
// additionally means uninstall must not depend on resolving a binary path at
// all — site/install.sh --uninstall removes the binary without calling
// --uninstall-hooks, so the entry commonly outlives the path it names.
func TestEndpointArm_UninstallIsNotForeignScoped(t *testing.T) {
	h := correctBeaconInstaller()
	r := rulesFor(t, h)

	t.Run("uninstall_scoped_to_the_running_binary", func(t *testing.T) {
		mutated := correctBeaconInstaller()
		mutated.Uninstall = func() (bool, error) {
			return beaconRunWith(func(cfg *hookjson.Config) {
				// The whole command, so a foreign binary's entry no longer matches.
				cfg.Sentinel = cfg.Entry()["command"].(string)
			}, hookjson.Uninstall)
		}
		rec := observe(t, func(at armT) { assertUninstallIsNotForeignScoped(at, mutated, r, mutated.SettingsPath(t)) })
		mustReport(t, rec, "expected modified=true removing",
			"an Uninstall whose sentinel names the running binary, so another daemon's entries survive (M4)")
	})
}

// TestEndpointRulesFor covers the route selection. A wiring that declares the
// address-free route without a ForeignInstall seed has no way to produce the
// drifted state obligations 3 and 4 both start from, and must be refused rather
// than graded on two obligations that cannot run (PR #1473 M5c).
func TestEndpointRulesFor(t *testing.T) {
	t.Run("address_free_without_a_foreign_seed_is_refused", func(t *testing.T) {
		h := entryInstaller(DeliveryAddressFree, correctURLDelivery)
		h.ForeignInstall = nil
		mustReport(t, observe(t, func(at armT) { rulesFor(at, h) }),
			"DeliveryAddressFree requires ForeignInstall",
			"an address-free wiring with no way to seed a drifted install (M5c)")
	})

	t.Run("an_unknown_route_is_refused", func(t *testing.T) {
		h := entryInstaller(DeliveryMode(99), correctURLDelivery)
		mustReport(t, observe(t, func(at armT) { rulesFor(at, h) }),
			"unhandled DeliveryMode",
			"a delivery mode naming no obligation set — a third route added without saying "+
				"which obligations it runs")
	})
}

// TestEndpointArm_SecondAssertions covers the half of obligations 2, 3 and 4
// that the mutations above leave untouched. Each of those obligations makes two
// claims, and only the first was graded — measured in review of PR #1498, where
// neutering each of these three left the whole suite green.
//
// They are grouped rather than folded into the tests above because each needs a
// fixture that is CORRECT on the first claim and wrong only on the second;
// mixing them into one installer would grade two things at once, which is the
// "the run failed incidentally elsewhere" shape this file exists to refuse.
func TestEndpointArm_SecondAssertions(t *testing.T) {
	h := correctBeaconInstaller()
	r := rulesFor(t, h)

	// Obligation 3's second claim: repointed IN PLACE, not appended beside the
	// foreign entry. A sentinel that does not match what the seed wrote means
	// EnsureInstalled does not recognize the entry as ours, so it adds a second
	// matcher group — reported modified=true, and the user's config now carries
	// two of our hooks, one of them dead. This is #1178's actual damage shape.
	t.Run("upgrade_appends_a_duplicate_group", func(t *testing.T) {
		mutated := correctBeaconInstaller()
		mutated.EnsureInstalled = func() (bool, error) {
			return beaconRunWith(func(cfg *hookjson.Config) {
				cfg.Sentinel = "not-the-sentinel-the-seed-wrote"
			}, hookjson.EnsureInstalled)
		}
		rec := observe(t, func(at armT) { assertUpgradedInPlace(at, mutated, r, mutated.SettingsPath(t)) })
		mustReport(t, rec, "expected exactly 1 matcher group",
			"an install that appends a second group beside the foreign entry instead of "+
				"repointing it (M3b)")
	})

	// Obligation 4's second claim: the entries are actually GONE. Reporting
	// modified=true while removing nothing is the shape a caller cannot detect —
	// `--uninstall-hooks` prints success over a config it did not change.
	t.Run("uninstall_reports_success_but_removes_nothing", func(t *testing.T) {
		mutated := correctBeaconInstaller()
		mutated.Uninstall = func() (bool, error) { return true, nil }
		rec := observe(t, func(at armT) { assertUninstallIsNotForeignScoped(at, mutated, r, mutated.SettingsPath(t)) })
		mustReport(t, rec, "survived uninstall",
			"an Uninstall that reports modified=true and leaves every entry in place")
	})

	// Obligation 2's second claim: idempotency. An install that rewrites on every
	// daemon start churns the user's config forever and makes "modified" useless
	// as a signal that anything actually changed.
	t.Run("install_is_not_idempotent", func(t *testing.T) {
		mutated := correctBeaconInstaller()
		mutated.EnsureInstalled = func() (bool, error) {
			// Writes the canonical delivery, so the first claim still holds and
			// only idempotency is wrong.
			if _, err := referenceBeaconEnsureInstalled(); err != nil {
				return false, err
			}
			return true, nil
		}
		rec := observe(t, func(at armT) { assertInstallWritesCanonicalDelivery(at, mutated, r, mutated.SettingsPath(t)) })
		mustReport(t, rec, "second install on the same bind address",
			"an installer that reports modified=true every time it runs")
	})
}

// beaconRunWith is referenceBeaconRun with one deliberate deviation applied, so
// each mutation states its deviation as a single line and inherits every other
// field from the reference config — a new required hookjson.Config field then
// reaches these mutations as well as the real contract test.
func beaconRunWith(mutate func(*hookjson.Config), op func(hookjson.Config) (bool, error)) (bool, error) {
	command, err := hookbeacon.InstalledCommand(referenceBeaconAdapter)
	if err != nil {
		return false, err
	}
	return referenceBeaconRun(command, func(cfg hookjson.Config) (bool, error) {
		mutate(&cfg)
		return op(cfg)
	})
}
