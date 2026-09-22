package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
)

// dateRe is the evidence date format. A citation without a date cannot be
// re-checked against a provider that has since changed its response, which is
// the whole reason #1977 §5 requires one.
var dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

// knownPlatforms is the closed GOOS set a route may claim.
var knownPlatforms = []string{"darwin", "linux", "windows"}

// ValidateRepo is the strict pass over replaydata/providers/: schema, closed
// vocabularies, fixture replay, and the shared obligations #2003/#2007 already
// exercise in Go. It returns every violation it found, sorted, and reports the
// inability to look as loudly as it reports a violation.
//
// It does NOT decide whether the tree ought to exist — that is the caller's
// scoping question, answered differently by `of provider verify` (which was
// asked about providers, so an absent tree is a finding) and by `of validate`
// (which runs against small fixture trees that legitimately have none).
func ValidateRepo(repoRoot string) []Finding {
	res := load(repoRoot)
	findings := res.Findings
	for _, p := range res.Providers {
		findings = append(findings, verifyOne(repoRoot, p)...)
	}
	findings = append(findings, statusGapFindings(repoRoot, res.Providers)...)
	sortFindings(findings)
	return findings
}

func sortFindings(f []Finding) {
	sort.Slice(f, func(i, j int) bool {
		if f[i].Path != f[j].Path {
			return f[i].Path < f[j].Path
		}
		return f[i].Message < f[j].Message
	})
}

// verifyOne runs every check against one manifest.
func verifyOne(repoRoot string, p Loaded) []Finding {
	var out []Finding
	at := p.RelDir + "/" + ManifestFile
	add := func(format string, args ...any) {
		out = append(out, Finding{Path: at, Message: fmt.Sprintf(format, args...)})
	}

	verifyIdentity(p, add)
	verifyProducts(p.Manifest, add)
	verifyObservation(repoRoot, p.Manifest, add)
	verifyAuth(p.Manifest, add)
	verifyPlatforms(p.Manifest, add)
	out = append(out, verifyFixtures(p)...)
	verifyCapabilities(p.Manifest, add)
	verifyEvidence(repoRoot, p.Manifest, add)
	return out
}

func verifyIdentity(p Loaded, add func(string, ...any)) {
	verifyTopLevelKeys(p.Raw, add)
	if p.Manifest.SchemaVersion != SchemaVersion {
		add("schema_version is %d, expected %d", p.Manifest.SchemaVersion, SchemaVersion)
	}
	verifyManifestID(p, add)
	if p.Manifest.DisplayName == "" {
		add("missing display_name")
	}
}

// verifyTopLevelKeys enforces the closed field set. Nested objects are covered
// separately, by load.go's unknownNestedFieldFindings.
func verifyTopLevelKeys(raw map[string]json.RawMessage, add func(string, ...any)) {
	for k := range raw {
		if !inSet(k, manifestKeys) {
			add("unexpected field %q (allowed: %s)", k, oneOf(manifestKeys))
		}
	}
}

func verifyManifestID(p Loaded, add func(string, ...any)) {
	switch id := p.Manifest.ID; {
	case id == "":
		add("missing id")
	case !idRe.MatchString(id):
		add("id %q is not a kebab slug", id)
	case id != p.ID:
		add("id %q does not match its directory name %q", id, p.ID)
	}
}

func verifyProducts(m Manifest, add func(string, ...any)) {
	if len(m.Products) == 0 {
		add("products is empty — a provider entry names at least one billed product")
		return
	}
	seen := map[string]bool{}
	for _, pr := range m.Products {
		switch {
		case pr.ID == "":
			add("a product has no id")
		case !idRe.MatchString(pr.ID):
			add("product id %q is not a kebab slug", pr.ID)
		case seen[pr.ID]:
			add("duplicate product id %q", pr.ID)
		}
		seen[pr.ID] = true
		if pr.Name == "" {
			add("product %q has no name", pr.ID)
		}
		if pr.QuotaScope == "" {
			add("product %q has no quota_scope — #1977 §3 wants the account or quota scope a product applies to", pr.ID)
		}
	}
}

func verifyObservation(repoRoot string, m Manifest, add func(string, ...any)) {
	o := m.Observation
	if !inSet(o.Strategy, Strategies) {
		add("observation.strategy %q is not one of: %s", o.Strategy, oneOf(Strategies))
	}
	verifyImplementation(repoRoot, o, add)
}

// verifyImplementation is #2008 §1.4 as a check: a manifest may configure a
// reviewed strategy, but it may not CLAIM one that is not there. Every path a
// code-backed route names has to exist.
func verifyImplementation(repoRoot string, o Observation, add func(string, ...any)) {
	impl := o.Implementation
	if !inSet(impl.Kind, ImplKinds) {
		add("observation.implementation.kind %q is not one of: %s", impl.Kind, oneOf(ImplKinds))
		return
	}
	if impl.Kind == ImplDataOnly {
		if impl.Package != "" {
			add("a data-only route names a package (%q) — a route with code of its own is code-backed", impl.Package)
		}
		if len(impl.Tests) > 0 || len(impl.MutationFixtures) > 0 {
			add("a data-only route names tests or mutation fixtures — the code it would exercise is someone else's")
		}
		if impl.PermissionName != "" || impl.PermissionKey != "" {
			// Same reason as the package: a consent row belongs to the code
			// that declares it. A data-only manifest naming one reads to the
			// next person as evidence that the row exists.
			add("a data-only route names a permission (%q/%q) — the consent row belongs to the adapter that declares it",
				impl.PermissionName, impl.PermissionKey)
		}
		return
	}
	verifyCodeBacked(repoRoot, impl, add)
}

func verifyCodeBacked(repoRoot string, impl Implementation, add func(string, ...any)) {
	switch {
	case impl.Package == "":
		add("a code-backed route names no package")
	case !safeRelPath(impl.Package):
		add("observation.implementation.package %q is not a safe repo-relative path", impl.Package)
	case !dirExists(filepath.Join(repoRoot, impl.Package)):
		add("observation.implementation.package %q does not exist — a code-backed claim names code that is there", impl.Package)
	}
	if len(impl.Tests) == 0 {
		add("a code-backed route names no tests — #1977 §8: new behaviour needs implementation AND tests")
	}
	verifyImplPaths(repoRoot, "test", impl.Tests, add)
	verifyImplPaths(repoRoot, "mutation fixture", impl.MutationFixtures, add)
}

// verifyImplPaths requires every path an implementation names to exist. A
// named file that is not there is the "cannot look" case, not a clean run.
func verifyImplPaths(repoRoot, label string, paths []string, add func(string, ...any)) {
	for _, f := range paths {
		if !safeRelPath(f) {
			add("%s %q is not a safe repo-relative path", label, f)
			continue
		}
		if !fileExists(filepath.Join(repoRoot, f)) {
			add("%s %q does not exist", label, f)
		}
	}
}

func verifyAuth(m Manifest, add func(string, ...any)) {
	if !inSet(m.Authentication.Method, AuthMethods) {
		add("authentication.method %q is not one of: %s", m.Authentication.Method, oneOf(AuthMethods))
	}
	if len(m.CredentialResolvers) == 0 {
		add("credential_resolvers is empty — say %q when the route needs no credential of its own", ResolverNone)
		return
	}
	for _, r := range m.CredentialResolvers {
		if !inSet(r.Kind, ResolverKinds) {
			add("credential resolver kind %q is not one of: %s", r.Kind, oneOf(ResolverKinds))
			continue
		}
		if r.Kind != ResolverNone && r.Location == "" {
			add("credential resolver %q names no location", r.Kind)
		}
	}
}

func verifyPlatforms(m Manifest, add func(string, ...any)) {
	if len(m.Platforms) == 0 {
		add("platforms is empty")
		return
	}
	for _, p := range m.Platforms {
		if !inSet(p, knownPlatforms) {
			add("platform %q is not one of: %s", p, oneOf(knownPlatforms))
		}
	}
}

// verifyFixtures checks every committed fixture: it is present, it parses as a
// JSON object, and its TOP-LEVEL keys sit inside the redaction allowlist.
//
// Deliberately not more than that, and worth saying so because "fixture
// replay" would suggest otherwise: no fixture is fed to a parser here, and
// nested values are never typed. The third check is the one that earns its
// keep — it is the #2003/#2007 redaction contract re-exercised on data, and a
// fixture is the one place a non-approved field can reach a committed file.
func verifyFixtures(p Loaded) []Finding {
	var out []Finding
	at := p.RelDir + "/" + ManifestFile
	add := func(format string, args ...any) {
		out = append(out, Finding{Path: at, Message: fmt.Sprintf(format, args...)})
	}
	m := p.Manifest
	if len(m.Fixtures) == 0 {
		add("fixtures is empty — #1977 §8 asks for a manifest plus redacted input and output fixtures")
	}
	for _, f := range m.Fixtures {
		out = append(out, verifyFixture(p, f)...)
	}
	return out
}

func verifyFixture(p Loaded, f Fixture) []Finding {
	at := p.RelDir + "/" + ManifestFile
	fail := func(format string, args ...any) []Finding {
		return []Finding{{Path: at, Message: fmt.Sprintf(format, args...)}}
	}
	if f.Name == "" {
		return fail("a fixture has no name")
	}
	if !inSet(f.Kind, FixtureKinds) {
		return fail("fixture %q kind %q is not one of: %s", f.Name, f.Kind, oneOf(FixtureKinds))
	}
	if !safeRelPath(f.Path) {
		return fail("fixture %q path %q is not a safe path relative to the provider directory", f.Name, f.Path)
	}
	b, err := os.ReadFile(filepath.Join(p.Dir, f.Path))
	if err != nil {
		// The loud-failure case issue #2008 §7 names: a verifier that cannot
		// read its fixture must fail, not report nothing found.
		return fail("fixture %q cannot be read at %s: %v", f.Name, f.Path, err)
	}
	var body map[string]json.RawMessage
	if err := json.Unmarshal(b, &body); err != nil {
		return fail("fixture %q at %s is not a JSON object: %v", f.Name, f.Path, err)
	}
	return redactionFindings(at, f, body, p.Manifest.Redaction)
}

func redactionFindings(at string, f Fixture, body map[string]json.RawMessage, r Redaction) []Finding {
	if len(r.AllowedFields) == 0 {
		return []Finding{{
			Path:    at,
			Message: "redaction.allowed_fields is empty — a fixture with no allowlist is a fixture nothing checks",
		}}
	}
	keys := make([]string, 0, len(body))
	for k := range body {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	var out []Finding
	for _, k := range keys {
		if !inSet(k, r.AllowedFields) {
			out = append(out, Finding{
				Path: at,
				Message: fmt.Sprintf("fixture %q carries field %q, which is outside redaction.allowed_fields (%s)",
					f.Name, k, oneOf(r.AllowedFields)),
			})
		}
	}
	return out
}

// verifyCapabilities requires EXACTLY the four axes. A manifest that could
// omit an axis could omit the inconvenient one, which is what the
// "unassessed" state exists to say out loud instead.
func verifyCapabilities(m Manifest, add func(string, ...any)) {
	for id := range m.Capabilities {
		if !IsValidAxis(id) {
			add("capability axis %q is not one of: %s", id, oneOf(AxisIDs()))
		}
	}
	for _, id := range AxisIDs() {
		verifyOneCapability(id, m.Capabilities, add)
	}
}

func verifyOneCapability(id string, caps map[string]Capability, add func(string, ...any)) {
	c, ok := caps[id]
	if !ok {
		add("capability axis %q is not declared — every manifest declares all four, %q included", id, ClaimUnassessed)
		return
	}
	if !IsValidClaim(c.Claim) {
		add("capability %q claims %q, which is not one of: %s", id, c.Claim, oneOf(ClaimStates))
	}
}

func verifyEvidence(repoRoot string, m Manifest, add func(string, ...any)) {
	ids := map[string]bool{}
	for _, e := range m.Evidence {
		verifyEvidenceEntry(repoRoot, e, ids, add)
		ids[e.ID] = true
	}
	for _, axis := range AxisIDs() {
		for _, ref := range m.Capabilities[axis].Evidence {
			if !ids[ref] {
				add("capability %q cites evidence %q, which no evidence entry declares", axis, ref)
			}
		}
	}
}

func verifyEvidenceEntry(repoRoot string, e EvidenceEntry, seen map[string]bool, add func(string, ...any)) {
	switch {
	case e.ID == "":
		add("an evidence entry has no id")
	case seen[e.ID]:
		add("duplicate evidence id %q", e.ID)
	}
	if !inSet(e.Kind, EvidenceKinds) {
		add("evidence %q kind %q is not one of: %s", e.ID, e.Kind, oneOf(EvidenceKinds))
	}
	if !dateRe.MatchString(e.Date) {
		add("evidence %q date %q is not YYYY-MM-DD", e.ID, e.Date)
	}
	if !safeRelPath(e.Ref) {
		add("evidence %q ref %q is not a safe repo-relative path", e.ID, e.Ref)
		return
	}
	p := filepath.Join(repoRoot, e.Ref)
	if !fileExists(p) && !dirExists(p) {
		// Not "nothing found": the citation named something and it is gone.
		add("evidence %q cites %s, which does not exist", e.ID, e.Ref)
	}
}
