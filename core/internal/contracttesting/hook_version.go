// hook_version.go holds the issue #1365 contract every hook-installing agent
// adapter must satisfy: it declares the minimum upstream CLI version its
// install requires, that floor is high enough that every event it writes
// exists at it, and the floor actually refuses an older CLI.
//
// It is the static half of the obligation. The runtime half — that
// PermissionService consults the declaration before running the Apply closure
// — cannot be checked from a declaration and lives in the services package's
// own tests, the same split AssertPermissionGated draws.
//
// The family exists for the reason #1365 was filed: Codex grew a private
// codexSupportsHooks with its own parser and floor constants, Claude Code grew
// nothing and wrote seven entries into the user's settings.json at any version,
// and the third adapter was going to copy one of those two. What an adapter
// declares can be checked; what it remembers to implement cannot.
package contracttesting

import (
	"slices"
	"strings"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/pkg/cliversion"
)

// HookVersionGate wires one adapter's hooks permission into
// AssertHookVersionGate.
type HookVersionGate struct {
	// Agent is the adapter's registration, exactly as the daemon consumes it.
	Agent agent.Agent
	// PermissionKey is the hooks permission's key within that registration.
	PermissionKey string
	// Installed is the installer's event list — pass the same variable handed
	// to hookjson.Config.Events, never a copy, for the reason spelled out on
	// HookDisclosure.Installed.
	Installed []string
	// Since maps each installed event to the upstream CLI version at which it
	// is proven to exist. Every entry in Installed needs one: an event with no
	// stated provenance is an event nobody checked, which is exactly the
	// position claudecode's seven-event install was in.
	Since map[string]string
}

// AssertHookVersionGate runs the issue #1365 contract against g.
func AssertHookVersionGate(t *testing.T, g HookVersionGate) {
	t.Helper()

	perm, ok := findPermission(g.Agent, g.PermissionKey)
	if !ok {
		t.Fatalf("agent %q declares no permission with key %q", g.Agent.Identity.Name, g.PermissionKey)
	}
	if perm.Hooks == nil {
		t.Fatalf("permission %q declares no HookInstall — a hooks permission must, "+
			"so that --uninstall-hooks and the version gate can both see it", g.PermissionKey)
	}
	if perm.Hooks.Version == nil || perm.Hooks.Version.Min == "" {
		t.Fatalf("hooks permission %q declares no minimum CLI version (#1365) — an adapter "+
			"that installs into an agent's config at any version writes entries an older CLI "+
			"may reject, and the user sees a granted permission whose channel never fires",
			g.PermissionKey)
	}
	gate := perm.Hooks.Version

	t.Run("floor_is_parseable", func(t *testing.T) {
		assertFloorParses(t, gate.Min)
	})
	t.Run("floor_covers_every_installed_event", func(t *testing.T) {
		assertFloorCoversEveryEvent(t, gate.Min, g.Installed, g.Since)
	})
	t.Run("floor_refuses_an_older_cli", func(t *testing.T) {
		assertFloorRefusesOlder(t, gate)
	})
	t.Run("unknown_version_fails_open", func(t *testing.T) {
		assertUnknownFailsOpen(t, gate)
	})
	t.Run("version_source_is_declared", func(t *testing.T) {
		if gate.Observed == nil && len(gate.Probe) == 0 {
			t.Errorf("hooks permission %q declares a floor of %q but no way to learn the "+
				"installed version — neither Observed nor Probe — so the gate can never "+
				"refuse anything and reads as protection that isn't there",
				g.PermissionKey, gate.Min)
		}
	})
}

// assertFloorParses rejects a floor the comparison cannot read. An unparseable
// floor fails open on every comparison, so the declaration would look like a
// gate while gating nothing — the failure mode this whole family is about.
func assertFloorParses(t *testing.T, min string) {
	t.Helper()
	if _, ok := cliversion.Parse(min); !ok {
		t.Errorf("declared minimum version %q is not a major.minor.patch triple; "+
			"an unparseable floor silently permits every version", min)
	}
}

// assertFloorCoversEveryEvent is the scope-item-2 arm: the floor must dominate
// every installed event's own introduction, so that at any version where the
// adapter installs at all, every entry it writes names an event the CLI knows.
func assertFloorCoversEveryEvent(t *testing.T, min string, installed []string, since map[string]string) {
	t.Helper()
	if len(installed) == 0 {
		t.Fatal("HookVersionGate.Installed is empty — pass the installer's event list")
	}
	for _, event := range installed {
		got, ok := since[event]
		if !ok {
			t.Errorf("no Since entry for installed event %q — every event written into the "+
				"user's config needs a stated version it is known to exist at, or the floor "+
				"cannot be shown to cover it", event)
			continue
		}
		if _, parsed := cliversion.Parse(got); !parsed {
			t.Errorf("Since[%q] = %q is not a major.minor.patch triple", event, got)
			continue
		}
		if ok, known := cliversion.AtLeast(min, got); known && !ok {
			t.Errorf("declared floor %s is below %s, the version %q is known at — at a CLI "+
				"between the two the install writes an entry naming an event that CLI does "+
				"not have; raise the floor to at least %s", min, got, event, got)
		}
	}
	for event := range since {
		if !slices.Contains(installed, event) {
			t.Errorf("Since names %q, which is not installed — a stale provenance entry "+
				"makes the floor look better justified than it is", event)
		}
	}
}

// assertFloorRefusesOlder proves the declaration actually blocks, rather than
// merely being present. It walks one patch level below the floor, which is the
// boundary an off-by-one in a comparison shows up at.
func assertFloorRefusesOlder(t *testing.T, gate *agent.VersionGate) {
	t.Helper()
	floor, ok := cliversion.Parse(gate.Min)
	if !ok {
		return // already reported by assertFloorParses
	}
	if allowed, _ := gate.Permits(gate.Min); !allowed {
		t.Errorf("gate refuses its own floor %s — the comparison is exclusive where it "+
			"should be inclusive", gate.Min)
	}
	older := justBelow(floor)
	if older == floor {
		return // a 0.0.0 floor has no predecessor to refuse
	}
	allowed, why := gate.Permits(older.String())
	if allowed {
		t.Errorf("gate permits %s, below the declared floor %s", older, gate.Min)
		return
	}
	// The refusal is the only thing the user ever sees about it (via the
	// daemon log today, #1362's wizard surfacing once PR #1379 lands), so an
	// empty or version-free reason is a silent skip wearing an error's clothes
	// — the exact behaviour #1365 filed against Codex.
	if why == "" {
		t.Error("gate refuses without a reason; the refusal is what the user is shown")
	}
	if !strings.Contains(why, older.String()) || !strings.Contains(why, gate.Min) {
		t.Errorf("refusal %q names neither the installed version %s nor the required %s; "+
			"a user cannot act on it", why, older, gate.Min)
	}
}

// assertUnknownFailsOpen pins the direction chosen in cliversion.AtLeast. It is
// a LOCK, not a defect test: it passes by construction and exists so that
// flipping the default to fail-closed — which would silently disable hooks for
// every user whose binary the daemon's PATH cannot reach — cannot happen
// quietly.
func assertUnknownFailsOpen(t *testing.T, gate *agent.VersionGate) {
	t.Helper()
	for _, unknown := range []string{"", "not a version", "2.1"} {
		if allowed, why := gate.Permits(unknown); !allowed {
			t.Errorf("gate refuses on unreadable version %q (%s) — an unknown version is "+
				"not an old one, and failing closed here disables hooks for anyone whose "+
				"CLI the daemon cannot probe", unknown, why)
		}
	}
}

// justBelow returns the version one patch level below v, borrowing across
// minor and major so a floor of x.y.0 still has a representable predecessor.
func justBelow(v cliversion.Version) cliversion.Version {
	switch {
	case v.Patch > 0:
		return cliversion.Version{Major: v.Major, Minor: v.Minor, Patch: v.Patch - 1}
	case v.Minor > 0:
		return cliversion.Version{Major: v.Major, Minor: v.Minor - 1, Patch: 99}
	case v.Major > 0:
		return cliversion.Version{Major: v.Major - 1, Minor: 99, Patch: 99}
	default:
		return v // 0.0.0 has no predecessor; assertFloorRefusesOlder tolerates it
	}
}
