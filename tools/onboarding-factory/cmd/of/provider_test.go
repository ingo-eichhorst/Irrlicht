package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/tools/onboarding-factory/internal/provider"
)

// repoRootFromTest resolves this repository's root from the package
// directory, the way scenario_recipes_test.go resolves replaydata/agents. It
// fails loudly rather than skipping: a test that silently stops looking at the
// shipped catalog would report a clean run for a catalog nobody read.
func repoRootFromTest(t *testing.T) string {
	t.Helper()
	root := filepath.Join("..", "..", "..", "..")
	if _, err := os.Stat(filepath.Join(root, "replaydata", "providers")); err != nil {
		t.Fatalf("cannot find replaydata/providers from the test's working directory: %v", err)
	}
	return root
}

// TestTheRealMetaManifestHasNoQuotaAndValidates is the ticket's fourth
// mutation fixture in its load-bearing form: the shape that must not be
// rejected is expressed in the manifest that actually ships.
//
// The Muse account API answers 200 with {is_subs_active, subs_tier_name} and
// no usage field at all. A schema requiring a quota block could not express
// that, and "a schema that rejects Muse" is the failure mode #2008 exists to
// avoid. Three separate assertions, because each could pass while another
// regressed: the manifest carries no quota field, its quota axis is the
// off-ladder source-specific-unavailable state, and the tree validates.
func TestTheRealMetaManifestHasNoQuotaAndValidates(t *testing.T) {
	root := repoRootFromTest(t)

	b, err := os.ReadFile(filepath.Join(root, "replaydata", "providers", "meta", "manifest.json"))
	if err != nil {
		t.Fatalf("the shipped meta manifest must be readable: %v", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatalf("the shipped meta manifest must parse: %v", err)
	}
	for _, forbidden := range []string{"quota", "quota_windows", "usage"} {
		if _, ok := raw[forbidden]; ok {
			t.Errorf("the manifest schema grew a top-level %q field; quota is an AXIS, not a field, "+
				"and a provider that reports none must stay expressible", forbidden)
		}
	}

	var m provider.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("the shipped meta manifest must decode: %v", err)
	}
	if got := m.Capabilities["quota_windows"].Claim; got != provider.ClaimSourceUnavailable {
		t.Errorf("meta's quota_windows claim is %q, want %q — the live probe found no usage field, "+
			"which is a finding about the route, not an unassessed axis", got, provider.ClaimSourceUnavailable)
	}

	if findings := provider.ValidateRepo(root); len(findings) != 0 {
		for _, f := range findings {
			t.Errorf("%s: %s", f.Path, f.Message)
		}
		t.Fatal("the shipped provider catalog must verify clean")
	}
}

// TestProviderVerifyAndStatusPassOnTheShippedCatalog runs both verbs through
// the CLI against the real repository, so the commands and not just the
// package are exercised.
func TestProviderVerifyAndStatusPassOnTheShippedCatalog(t *testing.T) {
	root := repoRootFromTest(t)
	for _, verb := range []string{"verify", "status"} {
		code, out, errs := runOf("provider", verb, "--repo-root", root)
		if code != exitOK {
			t.Fatalf("of provider %s on the shipped catalog: exit=%d\nstdout:\n%s\nstderr:\n%s", verb, code, out, errs)
		}
	}

	// status must actually have rendered the three shipped providers — an
	// empty table would exit 0 too, which is the shape a misread --repo-root
	// produces.
	_, out, _ := runOf("provider", "status", "--repo-root", root)
	for _, want := range []string{"anthropic", "openai", "meta", "0 capability gap(s)"} {
		if !strings.Contains(out, want) {
			t.Errorf("of provider status output should mention %q; got:\n%s", want, out)
		}
	}
}

// TestProviderVerifyFailsOnAnAbsentTree pins the scoping decision that
// separates this command from `of validate`: `of provider verify` was ASKED
// about providers, so "there is no tree" answers its question and must not be
// reported as a clean run.
func TestProviderVerifyFailsOnAnAbsentTree(t *testing.T) {
	code, _, errs := runOf("provider", "verify", "--repo-root", t.TempDir())
	if code != exitFail {
		t.Fatalf("of provider verify on a tree-less root: want exit %d, got %d", exitFail, code)
	}
	if !strings.Contains(errs, "cannot read the provider tree") {
		t.Errorf("it should say it could not read the tree; stderr:\n%s", errs)
	}
}

// TestProviderStatusExitsNonZeroOnAGap: the report is a gate, not a reading.
// #2008's completion criterion is that an unearned claim is reported rather
// than believed, and an exit code is what makes that hold in CI.
func TestProviderStatusExitsNonZeroOnAGap(t *testing.T) {
	root := filepath.Join("..", "..", "internal", "provider", "testdata", "unearned-claim")
	if _, err := os.Stat(filepath.Join(root, "replaydata", "providers")); err != nil {
		t.Fatalf("the committed unearned-claim fixture is missing: %v", err)
	}
	code, out, _ := runOf("provider", "status", "--repo-root", root)
	if code != exitFail {
		t.Fatalf("of provider status on an unearned claim: want exit %d, got %d\n%s", exitFail, code, out)
	}
	if !strings.Contains(out, "GAP") || !strings.Contains(out, "1 capability gap(s)") {
		t.Errorf("the gap should be visible in the table; got:\n%s", out)
	}
}

// TestTheShippedProviderCatalogExists is the tripwire that pays for
// validateProviders being scoped by existence alone. `of validate` cannot
// demand a provider tree of every root it is pointed at — its own fixtures
// have none — so the demand is made here instead, of THIS repository, where it
// can be made unconditionally.
//
// It is one of the two mechanisms holding the tree in place; the other is the
// replaydata deletion guard's replaydata/providers/* arm, exercised by
// tools/lib/replaydata-deletion-guard_test.sh.
func TestTheShippedProviderCatalogExists(t *testing.T) {
	root := repoRootFromTest(t)
	if !provider.Exists(root) {
		t.Fatal("replaydata/providers/ is gone; the provider catalog is a committed fixture set, not a scratch directory")
	}
	report := provider.Status(root)
	if len(report.Providers) == 0 {
		t.Fatal("the provider tree holds no providers; an empty catalog would scope `of validate`'s provider arm down to nothing")
	}
}

// TestValidateSkipsProvidersOnASmallFixtureTree is a LOCK, not red-first
// evidence: it passes on main today by construction. It pins the scoping
// predicate — a fixture tree with no provider catalog is not failed for
// lacking one. Without it, every existing `of validate` fixture would break,
// which is exactly what a first attempt at a stronger predicate did: keying
// the gate on adapters.json failed six tests in validate_maturity_test.go,
// because maturityRepo writes that file.
func TestValidateSkipsProvidersOnASmallFixtureTree(t *testing.T) {
	root := validRepo(t)
	if provider.Exists(root) {
		t.Fatal("fixture error: the provider tree must be absent for this lock")
	}
	if code, _, errs := runOf("validate", "--repo-root", root); code != exitOK {
		t.Fatalf("a fixture tree with no provider catalog must still pass `of validate`; exit=%d\nstderr:\n%s", code, errs)
	}
}

// TestValidateReportsAProviderFindingFromTheFixtureTree closes the wiring
// loop: a provider violation has to surface through `of validate`, not only
// through `of provider verify`. Uses the committed corrupt-schema fixture, so
// the mutation this asserts on is the same one internal/provider asserts on.
func TestValidateReportsAProviderFindingFromTheFixtureTree(t *testing.T) {
	root := filepath.Join("..", "..", "internal", "provider", "testdata", "corrupt-schema")
	code, _, errs := runOf("validate", "--repo-root", root)
	if code != exitFail {
		t.Fatalf("of validate on the corrupt-schema fixture: want exit %d, got %d", exitFail, code)
	}
	if !strings.Contains(errs, "replaydata/providers/acme/manifest.json") {
		t.Errorf("the finding should carry the provider path; stderr:\n%s", errs)
	}
}

// TestProviderUsageErrors pins the domain's two usage paths.
func TestProviderUsageErrors(t *testing.T) {
	code, _, _ := runOf("provider")
	if code != exitUsage {
		t.Errorf("bare `of provider`: want exit %d, got %d", exitUsage, code)
	}
	code, _, errs := runOf("provider", "invent")
	if code != exitUsage {
		t.Errorf("unknown verb: want exit %d, got %d", exitUsage, code)
	}
	if !strings.Contains(errs, "verb must be verify or status") {
		t.Errorf("it should name the two verbs; stderr:\n%s", errs)
	}
}

// TestProviderStatusJSONCarriesClaimedAndEarned pins the machine-readable
// rendering, which is what a later ticket's tooling will read.
func TestProviderStatusJSONCarriesClaimedAndEarned(t *testing.T) {
	code, out, _ := runOf("provider", "status", "--json", "--repo-root", repoRootFromTest(t))
	if code != exitOK {
		t.Fatalf("exit=%d\n%s", code, out)
	}
	var report provider.StatusReport
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("status --json must be decodable: %v\n%s", err, out)
	}
	if len(report.Providers) != 3 {
		t.Fatalf("want the three shipped providers, got %d", len(report.Providers))
	}
	for _, p := range report.Providers {
		if len(p.Axes) != len(provider.AxisIDs()) {
			t.Errorf("%s reports %d axes, want all %d", p.ID, len(p.Axes), len(provider.AxisIDs()))
		}
	}
}
