package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file holds the committed mutation fixtures issue #2008 §7 requires,
// plus one lock.
//
// A verifier has no "before the fix" to run red, so four of the fixtures under
// testdata/ mutate the thing the verifier protects — a provider manifest or
// one of its fixtures — and each test asserts the SPECIFIC finding, never
// merely "some finding". A fixture that went red because its path was mistyped
// would pass a `len(findings) > 0` assertion vacuously, which is the failure
// mode these tests exist to avoid.
//
// testdata/no-quota is the exception and is labelled as such below: it is a
// LOCK, asserting that a shape KEEPS validating, and it passes by construction.
// It is here because it is what the ticket must not get wrong, not because it
// is red-first evidence.
//
// Every mutation below was run against the verifier as this file was written,
// and the messages asserted are the ones it printed.

// mutationRoot returns the repo root of one committed fixture tree. It fails
// loudly when the tree is not there: a testdata directory that vanished must
// not read as a mutation the verifier cleared.
func mutationRoot(t *testing.T, name string) string {
	t.Helper()
	root := filepath.Join("testdata", name)
	if _, err := os.Stat(filepath.Join(root, "replaydata", "providers", "acme", "manifest.json")); err != nil {
		t.Fatalf("mutation fixture %q is missing its manifest: %v", name, err)
	}
	return root
}

// messages joins the findings so an assertion can name the exact wording.
func messages(findings []Finding) string {
	var b strings.Builder
	for _, f := range findings {
		b.WriteString(f.Path)
		b.WriteString(": ")
		b.WriteString(f.Message)
		b.WriteString("\n")
	}
	return b.String()
}

func wantFindings(t *testing.T, name string, findings []Finding, want ...string) {
	t.Helper()
	got := messages(findings)
	if len(findings) == 0 {
		t.Fatalf("%s: the verifier reported nothing; a mutated manifest must go red", name)
	}
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("%s: finding should mention %q; got:\n%s", name, w, got)
		}
	}
}

// Mutation 1 (#2008 §7): a corrupted manifest schema goes red. Three separate
// corruptions in one fixture — an unknown top-level key, a schema_version the
// package does not read, and a claim state outside the closed vocabulary —
// because each is a different way the closed field set can be widened by hand.
func TestVerifyGoesRedOnCorruptedManifestSchema(t *testing.T) {
	findings := ValidateRepo(mutationRoot(t, "corrupt-schema"))
	wantFindings(t, "corrupt-schema", findings,
		`unexpected field "quota"`,
		"schema_version is 2, expected 1",
		`capability "quota_windows" claims "probably-fine"`,
	)
}

// Mutation 2 (#2008 §7): a manifest claiming a capability its fixtures do not
// earn is REPORTED, not believed. This is #1977 §11.11 as a test — the
// evidence is a fixture, and live-verified is earned only by a live probe or a
// recording.
func TestVerifyReportsAClaimItsEvidenceDoesNotEarn(t *testing.T) {
	root := mutationRoot(t, "unearned-claim")
	findings := ValidateRepo(root)
	wantFindings(t, "unearned-claim", findings,
		`capability "billing_mode" claims "live-verified" but earns "fixture-verified"`,
		"live-verified is earned only by live-probe, recording",
	)

	// And the status report says the same thing, so the gap is visible in the
	// command a reader runs as well as in the gate.
	report := Status(root)
	if report.Gaps != 1 {
		t.Fatalf("status should report exactly one gap, got %d: %+v", report.Gaps, report.Providers)
	}
	if got := report.Providers[0].Axes[0]; !got.Gap || got.Earned != ClaimFixtureVerified {
		t.Errorf("billing_mode should be a gap earning %q, got %+v", ClaimFixtureVerified, got)
	}
}

// Mutation 3 (#2008 §7): removing a fixture a manifest references makes the
// verifier fail LOUDLY rather than report nothing found. AGENTS.md: absence of
// a finding and inability to look must never produce the same output.
//
// The fixture keeps a SECOND, readable fixture alongside the missing one, so
// the verifier is provably able to read fixtures in general — a verifier that
// had simply stopped reading them would pass this test if the only fixture
// were the absent one.
func TestVerifyFailsLoudlyOnAFixtureItCannotRead(t *testing.T) {
	findings := ValidateRepo(mutationRoot(t, "missing-fixture"))
	wantFindings(t, "missing-fixture", findings,
		`fixture "quota" cannot be read at fixtures/quota.json`,
	)
	if strings.Contains(messages(findings), `fixture "response"`) {
		t.Errorf("the readable fixture must not be reported; findings:\n%s", messages(findings))
	}
}

// LOCK (passes by construction, not red-first evidence), and the one this
// ticket must not get wrong: a provider with a billing mode and NO quota is a
// first-class valid shape.
//
// Muse is why. Its account API answers 200 with an active subscription, a named
// tier, and no usage field of any kind, so a schema that required a quota block
// could not express the first account API Irrlicht shipped. The fixture is that
// shape in the small; TestTheRealMetaManifestHasNoQuotaAndValidates pins it on
// the manifest that actually ships.
func TestVerifyAcceptsAProviderWithNoQuota(t *testing.T) {
	root := mutationRoot(t, "no-quota")
	if findings := ValidateRepo(root); len(findings) != 0 {
		t.Fatalf("a provider with a billing mode and no quota must validate; findings:\n%s", messages(findings))
	}
	report := Status(root)
	if report.Gaps != 0 {
		t.Fatalf("a no-quota provider must report no gap; got %d", report.Gaps)
	}
	quota := report.Providers[0].Axes[1]
	if quota.Axis != "quota_windows" || quota.Claimed != ClaimSourceUnavailable || quota.Earned != ClaimSourceUnavailable {
		t.Errorf("quota_windows should be %q claimed and earned, got %+v", ClaimSourceUnavailable, quota)
	}
}

// Mutation 5: a committed fixture carrying a field outside the manifest's
// redaction allowlist goes red. This is the #2003/#2007 redaction contract
// re-exercised on data — the Go allowlist decode protects the running daemon,
// and this protects the committed evidence.
func TestVerifyGoesRedOnAFixtureOutsideTheRedactionAllowlist(t *testing.T) {
	findings := ValidateRepo(mutationRoot(t, "redaction-leak"))
	wantFindings(t, "redaction-leak", findings,
		`fixture "response" carries field "user_email", which is outside redaction.allowed_fields`,
	)
}

// An off-ladder claim still has to name where it was observed. #1977 §5: a
// failed or blocked probe is not a capability verdict, so "this route reports
// no quota" is a finding about a route and cites one.
func TestVerifyRejectsAnUnevidencedSourceUnavailableClaim(t *testing.T) {
	root := t.TempDir()
	m := loadFixtureManifest(t, "no-quota")
	cap := m.Capabilities["quota_windows"]
	cap.Evidence = nil
	m.Capabilities["quota_windows"] = cap
	writeTree(t, root, m, map[string]any{"plan": "acme-pro", "active": true})

	wantFindings(t, "unevidenced-source-unavailable", ValidateRepo(root),
		`capability "quota_windows" claims "source-specific-unavailable" but earns "unassessed"`,
		"cites no observation",
	)
}

// The same arm, one step subtler: a SOURCE citation is evidence, and it used
// to satisfy the off-ladder claim. It must not — reading a parser tells you
// what the parser handles, never what the endpoint answered.
func TestVerifyRejectsASourceOnlySourceUnavailableClaim(t *testing.T) {
	root := t.TempDir()
	m := loadFixtureManifest(t, "no-quota")
	m.Evidence = append(m.Evidence, EvidenceEntry{
		ID: "acme-parser", Kind: EvidenceSource, Date: "2026-09-22",
		Ref: "replaydata/providers/acme/manifest.json",
	})
	cap := m.Capabilities["quota_windows"]
	cap.Evidence = []string{"acme-parser"}
	m.Capabilities["quota_windows"] = cap
	writeTree(t, root, m, map[string]any{"plan": "acme-pro", "active": true})

	wantFindings(t, "source-only-source-unavailable", ValidateRepo(root),
		"a source citation alone does not observe a route")
}

// A source citation alone earns NOTHING on the ladder either. Without this,
// `fixture-verified` could rest on having read a file that describes the
// route rather than on a fixture that exercises it.
func TestVerifyRejectsAFixtureClaimBackedOnlyByASource(t *testing.T) {
	root := t.TempDir()
	m := loadFixtureManifest(t, "no-quota")
	m.Evidence = append(m.Evidence, EvidenceEntry{
		ID: "acme-parser", Kind: EvidenceSource, Date: "2026-09-22",
		Ref: "replaydata/providers/acme/manifest.json",
	})
	cap := m.Capabilities["billing_mode"]
	cap.Evidence = []string{"acme-parser"}
	m.Capabilities["billing_mode"] = cap
	writeTree(t, root, m, map[string]any{"plan": "acme-pro", "active": true})

	wantFindings(t, "source-only-fixture-claim", ValidateRepo(root),
		`capability "billing_mode" claims "fixture-verified" but earns "unassessed"`)
}

// An evidence entry whose ref is gone must not keep earning its claim in the
// status report. `of provider status` exits non-zero on a gap, so a dead
// citation that still counted would make "could not look" and "nothing to
// find" print identically.
func TestStatusDoesNotLetADeadCitationEarnAClaim(t *testing.T) {
	root := t.TempDir()
	m := loadFixtureManifest(t, "no-quota")
	m.Evidence = append(m.Evidence, EvidenceEntry{
		ID: "acme-ghost", Kind: EvidenceLiveProbe, Date: "2026-09-22",
		Ref: "replaydata/providers/acme/fixtures/deleted.json",
	})
	cap := m.Capabilities["account_identity"]
	cap.Claim = ClaimLiveVerified
	cap.Evidence = []string{"acme-ghost"}
	m.Capabilities["account_identity"] = cap
	writeTree(t, root, m, map[string]any{"plan": "acme-pro", "active": true})

	report := Status(root)
	if report.Gaps != 1 {
		t.Fatalf("a dead citation must leave its claim unearned; gaps=%d %+v", report.Gaps, report.Providers)
	}
	wantFindings(t, "dead-citation", ValidateRepo(root),
		`evidence "acme-ghost" cites replaydata/providers/acme/fixtures/deleted.json, which does not exist`,
		`capability "account_identity" claims "live-verified" but earns "unassessed"`)
}

// A claim outside the closed vocabulary must read as a gap. claimRank answers
// -1 for an unknown token, so the rank comparison alone would call it "not
// above what it earns" and report nothing.
func TestStatusReportsAnOffVocabularyClaimAsAGap(t *testing.T) {
	root := filepath.Join("testdata", "corrupt-schema")
	report := Status(root)
	if report.Gaps == 0 {
		t.Fatalf("an off-vocabulary claim must be a gap; %+v", report.Providers)
	}
	var found bool
	for _, ax := range report.Providers[0].Axes {
		if ax.Axis == "quota_windows" && ax.Gap && strings.Contains(ax.Reason, "is not one of") {
			found = true
		}
	}
	if !found {
		t.Errorf("quota_windows should be a gap naming the closed set; %+v", report.Providers[0].Axes)
	}
}

// A field the schema does not define ANYWHERE is reported, not only one at the
// top level. A misspelled optional field is the case that matters: every
// required-field check still passes while the claim it carried is dropped.
func TestVerifyGoesRedOnAMisspelledNestedField(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(Root(root), "acme", "fixtures")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(mutationRoot(t, "no-quota"), "replaydata", "providers", "acme", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	mutated := strings.Replace(string(b), `"destination_keys"`, `"destination_kesy"`, 1)
	if mutated == string(b) {
		t.Fatal("fixture error: the field to misspell was not found")
	}
	if err := os.WriteFile(filepath.Join(Root(root), "acme", ManifestFile), []byte(mutated), 0o644); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(dir, "response.json"), map[string]any{"plan": "acme-pro", "active": true})

	wantFindings(t, "misspelled-nested-field", ValidateRepo(root),
		`nested field "destination_kesy" is not part of the manifest schema`)
}

// An absent tree, an unreadable tree and an empty tree are three different
// answers, and none of them is silence.
func TestVerifyFailsLoudlyWhenItCannotReadTheTree(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		wantFindings(t, "absent", ValidateRepo(t.TempDir()), "cannot read the provider tree")
	})
	t.Run("not a directory", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "replaydata"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "replaydata", "providers"), []byte("not a tree"), 0o644); err != nil {
			t.Fatal(err)
		}
		wantFindings(t, "not-a-directory", ValidateRepo(root), "cannot read the provider tree")
	})
	t.Run("empty", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(Root(root), 0o755); err != nil {
			t.Fatal(err)
		}
		wantFindings(t, "empty", ValidateRepo(root),
			"holds no provider directories",
			"an empty catalog and an unread one must not look alike")
	})
}

// A code-backed route may not claim code that is not there — #2008 §1.4, "a
// manifest is not a licence to skip code".
func TestVerifyRejectsACodeBackedRouteWithoutCodeOrTests(t *testing.T) {
	root := t.TempDir()
	m := loadFixtureManifest(t, "no-quota")
	m.Observation.Implementation = Implementation{
		Kind:    ImplCodeBacked,
		Package: "core/adapters/outbound/doesnotexist",
	}
	writeTree(t, root, m, map[string]any{"plan": "acme-pro", "active": true})

	wantFindings(t, "code-backed-without-code", ValidateRepo(root),
		`observation.implementation.package "core/adapters/outbound/doesnotexist" does not exist`,
		"a code-backed route names no tests",
	)
}

// A data-only route may not name a permission either. The consent row belongs
// to the code that declares it, and a manifest naming one reads to the next
// person as evidence that the row exists.
func TestVerifyRejectsADataOnlyRouteNamingAPermission(t *testing.T) {
	root := t.TempDir()
	m := loadFixtureManifest(t, "no-quota")
	m.Observation.Implementation = Implementation{
		Kind:           ImplDataOnly,
		PermissionName: "acme-account-api",
		PermissionKey:  "account-api",
	}
	writeTree(t, root, m, map[string]any{"plan": "acme-pro", "active": true})

	wantFindings(t, "data-only-permission", ValidateRepo(root),
		"a data-only route names a permission")
}

// A path that escapes the tree is a finding rather than an empty read. The
// filesystem readers elsewhere in the factory answer a "..'-bearing path with
// an EMPTY result, which would read here as a fixture that checked out clean.
func TestVerifyRejectsAnEscapingFixturePath(t *testing.T) {
	root := t.TempDir()
	m := loadFixtureManifest(t, "no-quota")
	m.Fixtures = []Fixture{{Name: "escape", Kind: FixtureResponse, Path: "../../../etc/hosts"}}
	writeTree(t, root, m, map[string]any{"plan": "acme-pro", "active": true})

	wantFindings(t, "escaping-path", ValidateRepo(root),
		`fixture "escape" path "../../../etc/hosts" is not a safe path`)
}

// loadFixtureManifest reads one committed fixture's manifest so a test can
// vary a single field from a shape that is known to validate.
func loadFixtureManifest(t *testing.T, name string) Manifest {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(mutationRoot(t, name), "replaydata", "providers", "acme", "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

// writeTree materialises one provider under root, with its response fixture.
func writeTree(t *testing.T, root string, m Manifest, response map[string]any) {
	t.Helper()
	dir := filepath.Join(Root(root), m.ID)
	if err := os.MkdirAll(filepath.Join(dir, "fixtures"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(dir, ManifestFile), m)
	writeJSON(t, filepath.Join(dir, "fixtures", "response.json"), response)
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
