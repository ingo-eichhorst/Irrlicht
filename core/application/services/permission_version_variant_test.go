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
	real := realOpenCodeGate(t)
	p.Writes.Version.Observed = real.Observed
	p.Writes.Version.ObservedRequires = real.ObservedRequires
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
	real := p.Writes.Version.Observed
	p.Writes.Version.Observed = func() string {
		calls++
		if real == nil { // a declaration with no store reader: nothing to read
			return ""
		}
		return real()
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

// TestVariantGate_AStoreAnswerWinsOverTheProbe is red-first, and it is the rule
// the orchestration of #2108 asked to have stated: the store says 2.1.4, the
// probe would say 1.19.4 (what cliprobe resolves first on a host whose brew
// still links 1.x). A passive answer that parses and selects an artifact is not
// confirmed by a probe (shouldConfirmByProbe), so the store wins and the 2.x
// artifact is installed. That is the direction #2104 item 3 measured to be
// right: 1.x hangs on a store 2.x has migrated, so a store 2.x wrote is not one
// a 1.x CLI is running against. Before #2108 2.1.4 was refused, the probe ran
// to double-check the refusal, and its 1.19.4 installed the 1.x file.
func TestVariantGate_AStoreAnswerWinsOverTheProbe(t *testing.T) {
	want := v2Rendering(t)
	path := isolateOpenCodeConfig(t)
	svc, log := newGateService()
	p := opencodeHooksAt(t, "2.1.4")
	p.Writes.Version.Probe = probeSaying("1.19.4")

	grant(svc, p)

	if got := installed(t, path); !bytes.Equal(got, want) {
		t.Errorf("store 2.1.4 / probe 1.19.4 installed %d bytes, want the 2.x artifact's %d (errors %v)",
			len(got), len(want), log.errors)
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
