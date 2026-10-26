package services

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite" // the fixture store below is built with database/sql

	// Test-only adapter import, the same exception permission_version_gate_test.go
	// takes for codex: this is where opencode's REAL declaration and the REAL
	// enforcement meet. core/architecture_test.go walks non-test imports only.
	"irrlicht/core/adapters/inbound/agents/opencode"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// This file covers the runtime half of #2108: PermissionService resolves the
// installed opencode version once, selects the artifact for it, and installs
// that one — and the #1372 loop verifies against the same selection. The static
// half (each variant declared sanely) is contracttesting.AssertHookVersionGate,
// wired from the opencode package.
//
// No test here runs the host's opencode. Every probe is replaced by a `sh -c`
// that prints a fixed answer or fails, because cliprobe resolves
// /opt/homebrew/bin first (#2105 correction 1) and a unit test must not depend
// on whatever is installed there.

// probeSaying is a version probe that prints version; "" makes it fail.
func probeSaying(version string) []string {
	if version == "" {
		return []string{"sh", "-c", "exit 1"}
	}
	return []string{"sh", "-c", "echo " + version}
}

// opencodeHooksProbing returns opencode's real hooks permission with ONLY the
// probe replaced: the real Observed, the real ObservedRequires, the real
// variants.
func opencodeHooksProbing(t *testing.T, probed string) agent.Permission {
	t.Helper()
	p := opencodeHooksAt(t, "")
	declared := realOpenCodeGate(t)
	p.Writes.Version.Observed = declared.Observed
	p.Writes.Version.ObservedRequires = declared.ObservedRequires
	p.Writes.Version.Probe = probeSaying(probed)
	return p
}

// realOpenCodeGate is the gate opencode's registration declares, untouched.
func realOpenCodeGate(t *testing.T) *agent.VersionGate {
	t.Helper()
	for _, p := range opencode.Agent().Permissions {
		if p.Key == opencode.PermissionKeyHooks && p.Writes != nil && p.Writes.Version != nil {
			return p.Writes.Version
		}
	}
	t.Fatal("opencode declares no hooks version gate")
	return nil
}

// grantDatabase records the database permission granted for grant()'s agent,
// which is what lets the real Observed read the store.
func grantDatabase(svc *PermissionService) {
	svc.mu.Lock()
	svc.set.Put("test-agent", opencode.PermissionKeyDatabase, permission.StateGranted)
	svc.mu.Unlock()
}

// fixtureStore builds an opencode store from the adapter's committed 2.1.4
// fixtures — the real schema and the rows of a store 2.1.4 migrated in place:
// one session imported from 1.x at version 1.19.4, then one 2.1.4 ran — and
// points OPENCODE_DB at it.
func fixtureStore(t *testing.T) {
	t.Helper()
	const testdata = "../../adapters/inbound/agents/opencode/testdata/"
	dbPath := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	ddl, err := os.ReadFile(testdata + "v2-schema.sql")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	if _, err := db.Exec(string(ddl)); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	rows, err := os.ReadFile(testdata + "v2-migrated.rows.sql")
	if err != nil {
		t.Fatalf("read rows: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(rows), "\n") {
		if strings.HasPrefix(line, "INSERT INTO session_v2 ") {
			if _, err := db.Exec(line); err != nil {
				t.Fatalf("insert: %v\n%s", err, line)
			}
			n++
		}
	}
	if n != 2 {
		t.Fatalf("the migrated fixture loaded %d session rows, want its 2 — this test's premise is gone", n)
	}
	t.Setenv("OPENCODE_DB", dbPath)
}

// installed reads the plugin file a grant left, "" when there is none.
func installed(t *testing.T, path string) []byte {
	t.Helper()
	got, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return got
}

// v1Rendering is what the 1.x installer writes, read back from a reference
// install in a tree of its own. Call it before isolating the tree under test.
func v1Rendering(t *testing.T) []byte {
	t.Helper()
	isolateOpenCodeConfig(t)
	if _, err := opencode.EnsureHooksInstalled(); err != nil {
		t.Fatalf("reference 1.x install: %v", err)
	}
	ref, _ := opencode.PluginPath()
	want, err := os.ReadFile(ref)
	if err != nil {
		t.Fatalf("read the reference 1.x install: %v", err)
	}
	return want
}

// TestVariantGate_AStoreShowingTwoXSelectsTwoX is red-first. The probe cannot
// answer and the store can: opencode's own store — the 2.1.4 fixture, which
// also holds a session imported from 1.19.4 — says the newest session was
// created by 2.1.4, so the 2.x artifact is installed. Before #2108 opencode
// declared no Observed, the unknown version failed open, and the 1.x file was
// written for a 2.x opencode.
func TestVariantGate_AStoreShowingTwoXSelectsTwoX(t *testing.T) {
	want := v2Rendering(t)
	path := isolateOpenCodeConfig(t)
	fixtureStore(t)
	svc, log := newGateService()
	grantDatabase(svc)

	grant(svc, opencodeHooksProbing(t, ""))

	if got := installed(t, path); !bytes.Equal(got, want) {
		t.Errorf("a store whose newest session is 2.1.4 installed %d bytes, want the 2.x artifact's %d "+
			"(errors %v)", len(got), len(want), log.errors)
	}
}

// TestVariantGate_NeitherProbeNorStoreRefusesWithAReason is red-first: with no
// version from anywhere, a variant declaration writes NOTHING and says the major
// is unknown. Before #2108 an unknown version failed open and the 1.x file was
// written — for whichever major the user actually runs.
func TestVariantGate_NeitherProbeNorStoreRefusesWithAReason(t *testing.T) {
	path := isolateOpenCodeConfig(t)
	t.Setenv("OPENCODE_DB", filepath.Join(t.TempDir(), "no-store-here.db"))
	svc, log := newGateService()
	grantDatabase(svc)

	grant(svc, opencodeHooksProbing(t, ""))

	if got := installed(t, path); got != nil {
		t.Errorf("an unknown opencode version installed %d bytes; a wrong guess is the file the "+
			"other major rejects", len(got))
	}
	if !log.errorMentioning("unknown", "nothing was written") {
		t.Errorf("no logged refusal saying the version is unknown; errors were %v", log.errors)
	}
}

// TestVariantGate_ObservedIsNotConsultedWithoutItsPermission is red-first: the
// store is opencode's session database, which only its database permission
// discloses, so while that permission is not granted the store is not read —
// Observed is never called — and the probe decides. Before #2108 the gate called
// Observed unconditionally.
//
// It is also the "store says 2.x, probe says 1.x" case with the store
// unreadable: the probe's 1.19.4 selects the 1.x artifact.
func TestVariantGate_ObservedIsNotConsultedWithoutItsPermission(t *testing.T) {
	want := v1Rendering(t)
	path := isolateOpenCodeConfig(t)
	fixtureStore(t)
	svc, log := newGateService() // test-agent holds no database consent

	p := opencodeHooksProbing(t, "1.19.4")
	calls := 0
	storeReader := p.Writes.Version.Observed
	p.Writes.Version.Observed = func() string {
		calls++
		if storeReader == nil { // a declaration with no store reader: nothing to read
			return ""
		}
		return storeReader()
	}

	grant(svc, p)

	if calls != 0 {
		t.Errorf("Observed was called %d time(s) while the database permission is not granted — it "+
			"reads opencode's session database", calls)
	}
	if got := installed(t, path); !bytes.Equal(got, want) {
		t.Errorf("the probe's 1.19.4 installed %d bytes, want the 1.x artifact's %d (errors %v)",
			len(got), len(want), log.errors)
	}
}

// TestVariantGate_AStoreAnswerWinsOverTheProbe is the rule the orchestration of
// #2108 asked to have stated: a passive answer that parses and selects an
// artifact is not confirmed by a probe (shouldConfirmByProbe), so the store
// decides in both directions.
//
//   - store 2.1.4, probe 1.19.4 (what cliprobe resolves first on a host whose
//     brew still links 1.x): the 2.x artifact. Red-first: before #2108 2.1.4
//     was refused, the probe ran to double-check the refusal, and its 1.19.4
//     installed the 1.x file.
//   - store 1.19.4, probe 2.1.4: the 1.x artifact. A LOCK — a 1.x answer was
//     never re-probed — and the row that goes red if the probe is consulted
//     for a selecting answer, which the probe-only-upward rule alone would not
//     catch. The store is the CLI that last ran; a 2.x binary on PATH that has
//     not yet opened the store has not migrated it, and the #1372 loop follows
//     the store within one pass once it does.
//
// That is the direction #2104 item 3 makes safe: 1.x hangs on a store 2.x has
// migrated, so a store 2.x wrote is not one a 1.x CLI is running against.
func TestVariantGate_AStoreAnswerWinsOverTheProbe(t *testing.T) {
	for _, c := range []struct {
		store, probe string
		want         func(*testing.T) []byte
	}{
		{"2.1.4", "1.19.4", v2Rendering},
		{"1.19.4", "2.1.4", v1Rendering},
	} {
		t.Run("store_"+c.store, func(t *testing.T) {
			want := c.want(t)
			path := isolateOpenCodeConfig(t)
			svc, log := newGateService()
			p := opencodeHooksAt(t, c.store)
			p.Writes.Version.Probe = probeSaying(c.probe)

			grant(svc, p)

			if got := installed(t, path); !bytes.Equal(got, want) {
				t.Errorf("store %s / probe %s installed %d bytes, want the store's artifact's %d (errors %v)",
					c.store, c.probe, len(got), len(want), log.errors)
			}
		})
	}
}

// TestVariantGate_AnUnreadableStoreDefersToTheProbe is a LOCK: it passed before
// #2108 too. The other half of the same rule — a passive answer that does not
// parse is confirmed by the probe, and the probe's 1.19.4 installs the 1.x
// artifact byte for byte.
func TestVariantGate_AnUnreadableStoreDefersToTheProbe(t *testing.T) {
	want := v1Rendering(t)
	path := isolateOpenCodeConfig(t)
	svc, log := newGateService()
	p := opencodeHooksAt(t, "not a version")
	p.Writes.Version.Probe = probeSaying("1.19.4")

	grant(svc, p)

	if got := installed(t, path); !bytes.Equal(got, want) {
		t.Errorf("store unreadable / probe 1.19.4 installed %d bytes, want the 1.x artifact's %d (errors %v)",
			len(got), len(want), log.errors)
	}
}

// TestVariantGate_BetweenTheArtifactsWritesNothing pins that #2108 did not
// stretch either artifact over the gap: at 2.0.0, a 2.x older than the 2.x
// artifact's measured floor, nothing is written into a clean tree. That half is
// a LOCK — #2105 already refused 2.0.0. The reason naming the 2.x floor is new:
// before #2108 there was no 2.x artifact to point the user at.
func TestVariantGate_BetweenTheArtifactsWritesNothing(t *testing.T) {
	path := isolateOpenCodeConfig(t)
	svc, log := newGateService()

	grant(svc, opencodeHooksAt(t, "2.0.0"))

	if got := installed(t, path); got != nil {
		t.Errorf("opencode 2.0.0 installed %d bytes; neither artifact is known to load there", len(got))
	}
	if !log.errorMentioning("2.0.0", "2.1.4") {
		t.Errorf("no logged refusal naming 2.0.0 and the 2.1.4 floor; errors were %v", log.errors)
	}
}

// TestVariantGate_ReverifyRewritesAOneXFileAfterAnUpgrade is red-first, and it is
// the path a running daemon takes when the user upgrades under it: the 1.x
// plugin is on disk and current for 1.x, the installed opencode is now 2.1.4,
// and one #1372 pass runs. Verify is the SELECTED artifact's, so the 1.x file
// reads Stale, and the repair — through the real service, behind the real
// consent — writes the 2.x artifact. Before #2108 the loop verified with the
// permission's own (1.x) Verify, read the file Intact, and left it until the
// next daemon start.
func TestVariantGate_ReverifyRewritesAOneXFileAfterAnUpgrade(t *testing.T) {
	want := v2Rendering(t)
	path := isolateOpenCodeConfig(t)
	if _, err := opencode.EnsureHooksInstalled(); err != nil {
		t.Fatalf("seed the 1.x plugin: %v", err)
	}

	decl := opencode.Agent()
	for i := range decl.Permissions {
		if decl.Permissions[i].Key == opencode.PermissionKeyHooks {
			decl.Permissions[i] = opencodeHooksAt(t, "2.1.4")
		}
	}
	svc, log := newGateService()
	svc.agents = []agent.Agent{decl}
	svc.mu.Lock()
	svc.set.Put(opencode.AdapterName, opencode.PermissionKeyHooks, permission.StateGranted)
	svc.set.Put(opencode.AdapterName, opencode.PermissionKeyDatabase, permission.StateGranted)
	svc.mu.Unlock()

	v := NewHookEntryVerifier(HookEntryReverifyConfig{
		Interval:    time.Minute,
		BackoffBase: time.Minute,
		BackoffMax:  time.Minute,
		Agents:      []agent.Agent{decl},
		Granted:     svc.Granted,
		Repair:      svc.RepairGrantedHookInstall,
		Log:         log,
	})
	v.RunPassForTest(time.Unix(1_800_000_000, 0))

	if got := installed(t, path); !bytes.Equal(got, want) {
		t.Errorf("after one re-verification pass under opencode 2.1.4 the plugin is %d bytes, want the "+
			"2.x artifact's %d (errors %v)", len(got), len(want), log.errors)
	}
	if snap := v.Snapshot(); snap.Outcomes[ReverifyRepaired] != 1 {
		t.Errorf("outcomes = %v, want one repair — the 1.x file under a 2.x opencode is damage", snap.Outcomes)
	}
}

// TestVariantGate_AProbeCannotLowerARefusedStoreAnswer is review finding 1's
// red-first test. The store says the user runs 3.0.0 — or a 2.x older than the
// 2.x artifact's floor — and the gate refuses it. The probe then runs to
// confirm the refusal, and on a host whose brew still links 1.x it answers
// 1.19.4 (cliprobe resolves /opt/homebrew/bin first, #2105 correction 1).
// Taking that answer installed the 1.x plugin for a 2.x or 3.x opencode, the
// file #2104 item 10 measured being refused on every load. A passive answer is
// the LAST-RUN version, so it can be stale only downward; a probe naming an
// older version is reading another install than the one that wrote the store.
func TestVariantGate_AProbeCannotLowerARefusedStoreAnswer(t *testing.T) {
	for _, store := range []string{"3.0.0", "2.0.5"} {
		t.Run(store, func(t *testing.T) {
			path := isolateOpenCodeConfig(t)
			svc, log := newGateService()
			p := opencodeHooksAt(t, store)
			p.Writes.Version.Probe = probeSaying("1.19.4")

			grant(svc, p)

			if got := installed(t, path); got != nil {
				t.Errorf("store %s / probe 1.19.4 installed %d bytes — the probe lowered the version "+
					"to a major the store says is not running (errors %v)", store, len(got), log.errors)
			}
			if !log.errorMentioning(store) {
				t.Errorf("no logged refusal naming the store's %s; errors were %v", store, log.errors)
			}
		})
	}
}

// TestVariantGate_AProbeCanRaiseAStaleStoreAnswer is a LOCK for the direction
// the probe exists for: the store's newest session predates an upgrade, so the
// probe's newer answer wins — below the floor (1.10.0 → 1.19.4) and between the
// artifacts (2.0.5 → 2.1.4).
func TestVariantGate_AProbeCanRaiseAStaleStoreAnswer(t *testing.T) {
	for store, c := range map[string]struct {
		probe string
		want  func(*testing.T) []byte
	}{
		"1.10.0": {"1.19.4", v1Rendering},
		"2.0.5":  {"2.1.4", v2Rendering},
	} {
		t.Run(store, func(t *testing.T) {
			want := c.want(t)
			path := isolateOpenCodeConfig(t)
			svc, log := newGateService()
			p := opencodeHooksAt(t, store)
			p.Writes.Version.Probe = probeSaying(c.probe)

			grant(svc, p)

			if got := installed(t, path); !bytes.Equal(got, want) {
				t.Errorf("store %s / probe %s installed %d bytes, want %d (errors %v)",
					store, c.probe, len(got), len(want), log.errors)
			}
		})
	}
}

// verifierFor builds a #1372 loop over decl wired to svc, with opencode's hooks
// and database permissions granted.
func verifierFor(t *testing.T, svc *PermissionService, decl agent.Agent, log *gateLog) *HookEntryVerifier {
	t.Helper()
	svc.agents = []agent.Agent{decl}
	svc.mu.Lock()
	svc.set.Put(opencode.AdapterName, opencode.PermissionKeyHooks, permission.StateGranted)
	svc.set.Put(opencode.AdapterName, opencode.PermissionKeyDatabase, permission.StateGranted)
	svc.mu.Unlock()
	return NewHookEntryVerifier(HookEntryReverifyConfig{
		Interval:    time.Minute,
		BackoffBase: time.Minute,
		BackoffMax:  time.Minute,
		Agents:      []agent.Agent{decl},
		Granted:     svc.Granted,
		Repair:      svc.RepairGrantedHookInstall,
		Log:         log,
	})
}

// opencodeAgentObserving is opencode's registration with its hooks permission's
// version source replaced by *observed (read at every resolution) and probe.
func opencodeAgentObserving(t *testing.T, observed *string, probe []string) agent.Agent {
	t.Helper()
	decl := opencode.Agent()
	for i := range decl.Permissions {
		if decl.Permissions[i].Key == opencode.PermissionKeyHooks {
			p := opencodeHooksAt(t, "")
			p.Writes.Version.Observed = func() string { return *observed }
			p.Writes.Version.Probe = probe
			decl.Permissions[i] = p
		}
	}
	return decl
}

// TestVariantGate_AnUnknownVersionKeepsACurrentInstall is review finding 2's
// red-first test. The 2.x plugin is installed and current; for one pass the
// version cannot be read (a probe that cannot run under launchd's PATH, a store
// read that times out). Reporting that as damage ran a repair into the
// unknown-version refusal and left it as the permission's effect error — the
// wizard's "granted but NOT applied" and a disarmed #1368 watchdog — for an
// install that is present and working, until a restart or a re-grant.
func TestVariantGate_AnUnknownVersionKeepsACurrentInstall(t *testing.T) {
	want := v2Rendering(t)
	path := isolateOpenCodeConfig(t)
	if _, err := opencode.EnsureHooksInstalledV2(); err != nil {
		t.Fatalf("seed the 2.x plugin: %v", err)
	}
	observed := ""
	decl := opencodeAgentObserving(t, &observed, probeSaying(""))
	svc, log := newGateService()
	v := verifierFor(t, svc, decl, log)

	v.RunPassForTest(time.Unix(1_800_000_000, 0))
	observed = "2.1.4"
	v.RunPassForTest(time.Unix(1_800_000_000, 0).Add(2 * time.Hour))

	if got := installed(t, path); !bytes.Equal(got, want) {
		t.Errorf("the current 2.x plugin changed across an unknown-version pass (%d bytes, want %d)", len(got), len(want))
	}
	svc.mu.Lock()
	reason := svc.effectErrs[effectKey(opencode.AdapterName, opencode.PermissionKeyHooks)]
	svc.mu.Unlock()
	if reason != "" || !svc.HookChannelReady(opencode.AdapterName) {
		t.Errorf("after one unknown-version pass the permission reads effect error %q, channel ready %v — "+
			"a present, current install is reported not applied", reason, svc.HookChannelReady(opencode.AdapterName))
	}
	if snap := v.Snapshot(); snap.Outcomes[ReverifyRepairFailed] != 0 || snap.Outcomes[ReverifyRepaired] != 0 {
		t.Errorf("outcomes = %v, want no repair at all — nothing was damaged", snap.Outcomes)
	}
}

// TestVariantGate_ReverifyAtTheNextMajorRemovesOnceThenRests is red-first, for
// #2105's residue on the variant path: the installed opencode is now 3.0.0,
// above every artifact, and the 2.x plugin is still there. One pass removes it
// (a ceiling refusal, recorded once as the failed repair it is), and the next
// pass finds nothing left to change. Before the review fix every later pass
// read every event Missing and ran the same refusal again on each back-off.
func TestVariantGate_ReverifyAtTheNextMajorRemovesOnceThenRests(t *testing.T) {
	path := isolateOpenCodeConfig(t)
	if _, err := opencode.EnsureHooksInstalledV2(); err != nil {
		t.Fatalf("seed the 2.x plugin: %v", err)
	}
	observed := "3.0.0"
	decl := opencodeAgentObserving(t, &observed, nil)
	svc, log := newGateService()
	v := verifierFor(t, svc, decl, log)

	v.RunPassForTest(time.Unix(1_800_000_000, 0))
	if got := installed(t, path); got != nil {
		t.Fatalf("the 2.x plugin survived a pass under opencode 3.0.0 (%d bytes; errors %v)", len(got), log.errors)
	}
	v.RunPassForTest(time.Unix(1_800_000_000, 0).Add(3 * time.Hour))

	snap := v.Snapshot()
	if snap.Outcomes[ReverifyRepairFailed] != 1 || snap.Outcomes[ReverifyIntact] != 1 {
		t.Errorf("outcomes = %v, want the removal once (repair_failed:1, its refusal recorded) and then "+
			"intact:1 — with the file gone a repair can change nothing", snap.Outcomes)
	}
}

// TestVariantGate_ReverifyBelowTheFloorLeavesTheFileAlone is red-first: below
// every artifact's floor a refusal removes nothing (#2105), so a repair could
// only record that refusal again on every back-off. The pass leaves the file
// and attempts no repair.
func TestVariantGate_ReverifyBelowTheFloorLeavesTheFileAlone(t *testing.T) {
	want := v1Rendering(t)
	path := isolateOpenCodeConfig(t)
	if _, err := opencode.EnsureHooksInstalled(); err != nil {
		t.Fatalf("seed the 1.x plugin: %v", err)
	}
	observed := "1.10.0"
	decl := opencodeAgentObserving(t, &observed, nil)
	svc, log := newGateService()
	v := verifierFor(t, svc, decl, log)

	v.RunPassForTest(time.Unix(1_800_000_000, 0))

	if got := installed(t, path); !bytes.Equal(got, want) {
		t.Errorf("a pass under opencode 1.10.0 changed the plugin (%d bytes, want %d)", len(got), len(want))
	}
	if snap := v.Snapshot(); snap.Outcomes[ReverifyRepairFailed] != 0 || snap.Outcomes[ReverifyRepaired] != 0 {
		t.Errorf("outcomes = %v, want no repair — below the floor a repair can only record its refusal again (errors %v)",
			snap.Outcomes, log.errors)
	}
}

// TestVariantGate_AnUnknownVersionAtStartKeepsACurrentInstall is the apply-time
// half of the same finding: PermissionService.Start re-runs the granted effect
// while the version cannot be read, and the 2.x plugin on disk is current.
// Refusing recorded an effect error no later intact pass clears; keeping the
// installed artifact writes nothing and records nothing.
func TestVariantGate_AnUnknownVersionAtStartKeepsACurrentInstall(t *testing.T) {
	want := v2Rendering(t)
	path := isolateOpenCodeConfig(t)
	if _, err := opencode.EnsureHooksInstalledV2(); err != nil {
		t.Fatalf("seed the 2.x plugin: %v", err)
	}
	svc, log := newGateService()
	p := opencodeHooksAt(t, "")
	p.Writes.Version.Probe = probeSaying("")

	grant(svc, p)

	if got := installed(t, path); !bytes.Equal(got, want) {
		t.Errorf("the current 2.x plugin changed under an unknown version (%d bytes, want %d)", len(got), len(want))
	}
	svc.mu.Lock()
	reason := svc.effectErrs[effectKey("test-agent", "hooks")]
	svc.mu.Unlock()
	if reason != "" {
		t.Errorf("EffectError = %q with a current install on disk, want none (errors %v)", reason, log.errors)
	}
}

// TestVariantGate_AnUpgradeIsNotReportedAsTampering is review finding 3's
// red-first test. The #1372 rewrite of a current 1.x plugin after the installed
// opencode became 2.x is the designed upgrade path, not damage: it must not be
// logged as "Something outside irrlicht rewrote that file".
func TestVariantGate_AnUpgradeIsNotReportedAsTampering(t *testing.T) {
	isolateOpenCodeConfig(t)
	if _, err := opencode.EnsureHooksInstalled(); err != nil {
		t.Fatalf("seed the 1.x plugin: %v", err)
	}
	observed := "2.1.4"
	decl := opencodeAgentObserving(t, &observed, nil)
	svc, log := newGateService()
	v := verifierFor(t, svc, decl, log)

	v.RunPassForTest(time.Unix(1_800_000_000, 0))

	if log.errorMentioning("Something outside irrlicht rewrote that file") {
		t.Errorf("the 1.x → 2.x rewrite was logged as tampering: %v", log.errors)
	}
	log.mu.Lock()
	infos := strings.Join(log.infos, "\n")
	log.mu.Unlock()
	if !strings.Contains(infos, "2.1.4") {
		t.Errorf("no info line names the version the artifact was rewritten for; infos were %s", infos)
	}
}

// unreadablePlugin seeds the 2.x plugin and makes it unreadable, restoring the
// mode at cleanup so the temp tree can be removed.
func unreadablePlugin(t *testing.T, path string) {
	t.Helper()
	if _, err := opencode.EnsureHooksInstalledV2(); err != nil {
		t.Fatalf("seed the 2.x plugin: %v", err)
	}
	if err := os.Chmod(path, 0); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("the plugin is still readable at mode 0 (running as root?) — this test cannot make it unreadable")
	}
}

// TestVariantGate_ReverifyCannotReadThePluginAtTheNextMajor is red-first for
// the follow-up review's finding 1: above every artifact the loop decides from
// whether a file of ours is there, and a Verify that could not read the file
// was taken as "none there" — the pass read intact, said nothing, and removed
// nothing. Inability to look must read as unreadable, as it already does
// inside a variant's range.
func TestVariantGate_ReverifyCannotReadThePluginAtTheNextMajor(t *testing.T) {
	path := isolateOpenCodeConfig(t)
	unreadablePlugin(t, path)
	observed := "3.0.0"
	decl := opencodeAgentObserving(t, &observed, nil)
	svc, log := newGateService()
	v := verifierFor(t, svc, decl, log)

	v.RunPassForTest(time.Unix(1_800_000_000, 0))

	if snap := v.Snapshot(); snap.Outcomes[ReverifyUnreadable] != 1 {
		t.Errorf("outcomes = %v, want unreadable:1 — a plugin the pass could not read is not an intact one", snap.Outcomes)
	}
}

// TestVariantGate_AnUnknownVersionCannotReadThePlugin is red-first for the same
// finding on the install path: with the version unknown, the keep rule reads
// the installed file, and a read that fails must say so in the refusal rather
// than read as "nothing installed".
func TestVariantGate_AnUnknownVersionCannotReadThePlugin(t *testing.T) {
	path := isolateOpenCodeConfig(t)
	unreadablePlugin(t, path)
	svc, log := newGateService()
	p := opencodeHooksAt(t, "")
	p.Writes.Version.Probe = probeSaying("")

	grant(svc, p)

	svc.mu.Lock()
	reason := svc.effectErrs[effectKey("test-agent", "hooks")]
	svc.mu.Unlock()
	if !strings.Contains(reason, "permission denied") {
		t.Errorf("EffectError = %q, want it to carry the failed read of the installed plugin (errors %v)", reason, log.errors)
	}
}
