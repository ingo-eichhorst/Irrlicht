package agent

import (
	"fmt"
	"strings"

	"irrlicht/core/domain/permission"
	"irrlicht/core/pkg/cliversion"
)

// Agent is the registration record each inbound agent adapter exports.
// It collapses identity, process recognition, session source, write-back
// control, and consent-gated permissions into five orthogonal axes. Each
// adapter package's Agent() constructor returns one value; the daemon wires
// per-adapter behavior off it via the map projections in
// core/adapters/inbound/agents (Parsers, PIDDiscoverers, ProcessNames,
// SubagentCounters, MetricsProviders) and the Source-variant dispatch
// in core/cmd/irrlichd/wiring.go.
type Agent struct {
	Identity    Identity
	Process     Process
	Source      Source
	Permissions []Permission
}

// Permission declares one consent-gated capability of an adapter: what
// it touches, what feature it unlocks, and (for modify-kind entries) how
// to apply and undo the modification. The daemon exercises nothing — no
// install, no read — until the user grants the permission (issue #570).
// At most ONE observe-kind permission per adapter is meaningful: the gate that
// enforces observe consent resolves an adapter to its first observe-kind
// permission, so a second would never be consulted. An adapter that reads an
// additional file names it in the existing observe permission's Touches/Detail
// (the user-visible contract) rather than declaring another one.
type Permission struct {
	Key             string          // stable identifier, e.g. "hooks", "transcripts"
	Kind            permission.Kind // modify (writes a file) or observe (reads only)
	Title           string          // short label for the wizard row
	FeatureUnlocked string          // what granting enables
	Touches         string          // what the daemon reads/writes when granted
	Detail          string          // expanded (i) text: exact paths/commands, how to undo

	// Apply performs the modification on grant; Remove undoes it on
	// revoke. Nil for observe-kind permissions — their effect (starting
	// and stopping the agent's watchers) is owned by the daemon wiring.
	Apply  func() error
	Remove func() error

	// Writes is non-nil for every permission whose Apply closure writes a
	// shared, user-owned file — a file that follows $HOME rather than
	// IRRLICHT_HOME, and that therefore outlives the daemon that wrote it. It
	// carries what callers OUTSIDE the adapter need: which file, and how to
	// take irrlicht's content back out.
	Writes *ManagedUserFile

	// Advisory declares paths this permission's Apply is KNOWN to cause to be
	// written, that ManagedUserFile cannot represent (issue #1748). The
	// motivating case: kiro-cli/hooks's Apply shells out to the real
	// `kiro-cli` binary, and that child process writes its OWN identity store
	// ($HOME/Library/Application Support/kiro-cli/data.sqlite3) as a side
	// effect of merely launching — measured true even when the install itself
	// fails. Also cannot hold this because Also's whole contract (#1718)
	// assumes what ManagedUserFile assumes everywhere else: the content is
	// irrlicht's, so Uninstall can remove it and a snapshot/restore can safely
	// stand in for "don't touch it". Neither holds for an agent CLI's own
	// database:
	//
	//   - It carries no irrlicht content, so Uninstall is meaningless for it.
	//   - Where it lives is platform-specific and has only been verified on
	//     one OS (kiro-cli's own resolver may put it somewhere else entirely
	//     on another).
	//   - It is a live external store; blindly snapshotting and restoring one
	//     is a corruption risk, not a protection.
	//
	// So Advisory entries are declared for VISIBILITY only:
	// --print-advisory-files reports them separately from
	// --print-managed-files, and the recording rig warns that a recording
	// touched them without attempting to back them up or restore them. See
	// agents.AdvisoryFiles and core/cmd/irrlichd/managedwrites_test.go's
	// static arm, which now recognizes an Advisory resolver as declared the
	// same way it already recognizes Writes.Path/Also.
	Advisory []AdvisoryWrite
}

// AdvisoryWrite is one path a permission's Apply is known to write that holds
// no irrlicht-authored content — see Permission.Advisory for why it is a
// separate category from ManagedUserFile rather than another Also entry.
type AdvisoryWrite struct {
	// Path resolves the absolute path the write is known to land at. Unlike
	// ManagedUserFile.Path, a resolution error here is tolerated by every
	// consumer (agents.AdvisoryFiles skips it) rather than treated as fatal:
	// an advisory declaration is explicitly allowed to be scoped to a
	// platform where the write has actually been observed, and "unverified
	// elsewhere" is not the same defect as "our own required config failed to
	// resolve".
	Path func() (string, error)
	// Reason documents what the path is and why it is advisory rather than a
	// Writes.Also entry — shown verbatim by --print-advisory-files and the
	// recording rig's warning.
	Reason string
}

// HooksPermissionKey is the permission key under which an adapter installs
// irrlicht's hooks into an agent-owned config file. It is declared here rather
// than restated per adapter because two things narrow on it from outside the
// adapters: `irrlichd --uninstall-hooks`, which must touch the hook entries and
// nothing else, and the registry-wide version-floor tripwire. A per-adapter
// spelling is a string two packages have to agree on by convention (#1383).
const HooksPermissionKey = "hooks"

// InstructionsPermissionKey is the permission key under which an adapter writes
// irrlicht's managed instruction blocks into an agent's user-level instruction
// file (~/.claude/CLAUDE.md today). Declared here for the same reason
// HooksPermissionKey is: `irrlichd --uninstall-task-eta` narrows the
// managed-file projection to it from OUTSIDE the adapter, so a per-adapter
// spelling would be a string two packages agree on by convention.
//
// The narrowing is what keeps that command's name true in both directions — it
// must not revoke the hook entries, and --uninstall-hooks must not revoke this
// (#1437, the sibling of #1425).
const InstructionsPermissionKey = "instructions"

// AccountAPIPermissionKey is the shared permission key a provider's
// account-quota permission declares (issue #2003), covering the credential
// read AND the outbound account-API call TOGETHER as one consent row — the
// high-level design settled in #2003's triage comment answers the question
// this file's own doc comment above raises (only the first observe-kind
// permission per adapter is consulted by ObserveGranted): a provider's
// account-quota permission is declared on its OWN pseudo-adapter Agent value
// (Identity.Name distinct from the provider's real coding-agent adapter,
// e.g. "muse-account-api"), appended to the consent catalog the same way
// core/adapters/inbound/orchestrators/gastown/permission.go and
// core/adapters/inbound/agents/processlifecycle/kitty_permission.go are —
// never as a second Permission entry on the provider's own agent.Agent,
// where it would silently never be consulted.
//
// It is Kind: permission.KindObserve (a credential read plus a network call
// is monitoring, not a filesystem modification) with NON-NIL Apply/Remove —
// the same shape gastown's PermissionKeyState uses for an observe-kind
// permission that still needs an explicit start/stop effect. Apply registers
// the provider with the daemon-wide poller
// (core/application/services.AccountPoller); Remove calls
// AccountPoller.Revoke(providerName), which cancels every in-flight fetch
// for that provider and wipes its cached observations (issue #2003 §1.1:
// revocation must stop in-flight work and prevent a later publication of a
// result that work already produced).
//
// #2003 adds no real provider, so this key is declared but not yet
// appended to any consent catalog entry — #2007 (Muse, the epic's own work
// package B) is the first adapter to construct a Permission with this key and
// wire it into core/cmd/irrlichd/consentcatalog.go, following the gastown
// shape: Kind: permission.KindObserve, a non-nil Apply/Remove pair, and (per
// docs/testing-contracts.md's managed-user-files bullet) a name in
// core/cmd/irrlichd/managedfiles_test.go's applyWritesNoUserFile list,
// because Apply here starts a poller registration rather than writing any
// user-owned file. Confirmed by reading that file directly (not assumed —
// and corrected by review, #2003, which caught this comment first citing
// the similarly-named managedwrites_test.go instead): "gastown/state" is
// already there with the reason "Apply starts and stops the Gas Town
// watcher; it reads ~/gt and writes nothing" — the identical shape an
// account-API Apply needs stated for itself.
const AccountAPIPermissionKey = "account-api"

// ManagedUserFile is the outside-the-adapter half of a permission that writes a
// shared user-owned file (#1357, widened by #1383).
//
// Two places have to enumerate those files. The daemon's `--uninstall-hooks`
// must leave nothing behind pointing at a dead port. And the onboarding
// recorder backs them up before running a grant-all daemon against the user's
// real $HOME — grant-all exercises EVERY modify permission's Apply closure, so
// the recorder's protected set is "every managed user file", not "every hook
// config": it is what stops a recording leaving irrlicht's managed blocks in
// the user's own ~/.claude/CLAUDE.md and ~/.config/kitty/kitty.conf.
//
// Both lists used to be hand-maintained literals, correct only for as long as
// the adapter set stayed frozen. Declaring the file here — next to the
// Apply/Remove closures that write it — makes both a projection of the consent
// catalog, so a new adapter is covered by existing over being remembered.
//
// The two consumers read DIFFERENT slices of the same declaration and that is
// deliberate: the recorder protects everything, while `--uninstall-hooks`
// narrows to HooksPermissionKey so it never revokes a capability the user did
// not ask it to touch.
type ManagedUserFile struct {
	// Path resolves the ABSOLUTE path of the file this permission writes,
	// honoring the same env overrides the adapter's own installer honors
	// (e.g. CODEX_HOME, XDG_CONFIG_HOME). It must resolve the real path
	// rather than a display string: the recorder backs up and restores
	// exactly what it returns.
	Path func() (string, error)
	// Uninstall removes irrlicht's content from that file, reporting whether
	// it modified anything. Required, not optional: a permission that can
	// write a user-owned file it cannot take itself back out of is the state
	// this declaration exists to make impossible.
	Uninstall func() (bool, error)

	// Verify reports what our entries look like in that file RIGHT NOW,
	// without writing anything (issue #1372).
	//
	// It exists because the install is not a one-shot fact. An agent whose own
	// UI rewrites its settings can delete our entries between the grant and
	// the next hook: gemini-cli's writer is sync-by-omission — it drops every
	// key absent from the snapshot the CLI took at startup, recursively — and
	// one of its write paths (saveModelChange) is not even user-initiated. The
	// symptom is indistinguishable from every other silent hook failure: a
	// permission still reading `granted`, no error anywhere, and waiting
	// detection quietly degraded.
	//
	// Read-only is the contract, not an implementation detail. The repair is a
	// separate step run through the permission service, so that the write
	// stays behind the same consent gate the original install passed, and so
	// that a verifier can never become a way to write to a config the user
	// withdrew consent for (#570).
	Verify func() (HookEntryStatus, error)

	// Version declares the upstream CLI version floor this install requires
	// (issue #1365). Nil means the permission declares no floor and installs
	// unconditionally — which is what Claude Code's hooks did for their whole
	// life, and what a new hook adapter would inherit by default, so a nil on a
	// HooksPermissionKey permission is a decision worth stating rather than an
	// omission (TestEveryHookInstallDeclaresAVersionFloor enforces it there).
	// Permissions that patch a config file irrlicht owns end-to-end — the
	// CLAUDE.md instruction blocks, the kitty remote-control block — depend on
	// no upstream CLI feature and leave it nil.
	//
	// With Variants declared, Version is the ENVELOPE: Min is the lowest
	// variant's floor, Max the highest variant's ceiling, and Probe, Observed
	// and ObservedRequires say how the installed version is learnt, once for
	// every variant.
	Version *VersionGate

	// Variants declares one artifact per upstream major for a hook install
	// whose upstream changed its extension format between majors (#2108):
	// opencode 2.x refuses the 1.x plugin file on every load (measured, #2104
	// item 10), and 1.x's loader rejects an export that is neither a function
	// nor an object carrying server() (read out of the 1.14.50 bundle — see
	// opencode's TestShippedPluginExportsOnlyAFunction), which the 2.x shape
	// is, so a single file can no longer serve both. Each variant
	// has its own version range, its own event list, and the closures that
	// install and verify it; every variant writes the same Path, so an upgrade
	// is a rewrite in place and Uninstall stays one rule.
	//
	// Empty is the single-artifact shape every other adapter declares, and
	// nothing about it changes: Apply, Verify and Version describe the one
	// install. Non-empty, PermissionService resolves the installed version
	// once, selects the variant whose range holds it (SelectVariant), and runs
	// THAT variant's Apply; the #1372 re-verification checks THAT variant's
	// Verify. The permission's own Apply and Writes.Verify are still declared
	// — the registry tripwires require them, and the managed-writes grader and
	// the per-adapter gating contract run them directly — but PermissionService
	// runs neither for a variant declaration (runClosureEffect,
	// HookEntryVerifier.verifyTarget).
	Variants []HookVariant

	// Also resolves zero or more ADDITIONAL shared, user-owned files this
	// permission's single Apply closure writes, beyond Path (issue #1718).
	//
	// Path stays what every existing narrowed projection (agents.HookConfigs,
	// `--uninstall-hooks`) means by "the file this permission writes" — adding
	// this field changes that meaning for nobody, and all eight declaration
	// sites that existed before it compile and behave unchanged with it left
	// nil. It exists because mistral-vibe's hooks install is the first whose
	// Apply genuinely spans two files: the hook entries themselves
	// ($VIBE_HOME/hooks.toml) and a separate boolean gate
	// (enable_experimental_hooks in $VIBE_HOME/config.toml) that must also be
	// true or the entries are never read. Neither #1449's grant-all refusal nor
	// the recorder's full backup/restore sweep (agents.ManagedUserFiles) can see
	// a write this field does not name — an undeclared secondary write is
	// invisible to both, which is the exact shape of the incident #1449 exists
	// to prevent, so both MUST resolve every entry here with the same rigor as
	// Path (core/application/services/permission_shared_config_gate.go,
	// core/adapters/inbound/agents/managedfiles.go).
	//
	// Deliberately not folded into Path becoming a slice: Path is read by name
	// in enough places (log lines, the narrowed uninstall commands' per-file
	// reporting) that widening its type would touch every one of them for a
	// property only two adapters need. An adapter with a second file appends
	// one resolver here instead.
	Also []func() (string, error)

	// RealFixture declares this permission's committed, real-world config
	// document (issue #1756) — a document CAPTURED from the agent's own
	// output (or the shared file it manages), not one any test constructed.
	//
	// #1753 shipped because every hook-install test in the tree graded
	// against a config its OWN test wrote: a document whose shape the test
	// author already knew how to handle. mistral-vibe's real config.toml
	// (core/adapters/inbound/agents/vibe/testdata/real-config-2.19.1.toml,
	// captured for #1755) carries twelve multi-line arrays no hand-written
	// fixture ever had; hooktoml's splicer refused all of them, Apply failed,
	// and consent kept reading granted. Nil means no such document is
	// declared — which TestEveryHookInstallDeclaresARealConfigFixture
	// (core/adapters/inbound/agents/hookrealfixture_test.go) flags for any
	// hooks-installing adapter not named in that test's own explicit,
	// reasoned gap list, so an omission is a loud failure rather than a
	// silent one.
	RealFixture *RealConfigFixture
}

// RealConfigFixture is one committed, real-world config fixture (issue
// #1756): a maintainer-captured copy of a document the AGENT ITSELF
// produced (or manages, for a shared settings file), secrets redacted,
// exercised by the declaring adapter's own install/uninstall tests in
// addition to whatever synthetic documents those tests construct by hand.
type RealConfigFixture struct {
	// Path is the fixture file, relative to the declaring adapter's own
	// package directory — e.g. "testdata/real-config-2.19.1.toml". Read by
	// TestEveryHookInstallDeclaresARealConfigFixture to confirm the file
	// actually exists and is not a stub, and referenced (as a literal) by
	// the adapter's own guard test that reads it.
	Path string

	// CLIVersion is the upstream CLI version that produced the captured
	// document, so a later reader can tell how stale the evidence is — the
	// same staleness problem docs/replay-testing.md names for replay
	// goldens. Required whenever Path is set: an unstamped fixture is
	// evidence nobody can date.
	CLIVersion string
}

// HookEntryStatus is one read-only look at an install (issue #1372): which of
// the events the adapter installs are no longer represented in the config file
// the way we would write them today.
//
// The two buckets are kept apart because they are different accidents, even
// though the repair for both is the same call. Missing means nothing carrying
// our sentinel is in that event's array at all — somebody deleted it, which is
// the gemini-cli sync-by-omission case. Stale means an entry of ours IS there
// but no longer canonical — most often pointing at a port this daemon is not
// listening on (#1178), which a user reading the file would not spot as broken.
type HookEntryStatus struct {
	// Missing are event names with no irrlicht entry left in the file.
	Missing []string
	// Stale are event names whose irrlicht entry is present but not in the
	// shape (or at the endpoint) we would install today.
	Stale []string
}

// Intact reports whether the install needs nothing done to it.
func (s HookEntryStatus) Intact() bool {
	return len(s.Missing) == 0 && len(s.Stale) == 0
}

// Damage renders the finding for a log line, e.g. "missing: Stop, Notification;
// stale: PreToolUse". Empty for an intact install, so a caller never logs a
// verdict with nothing behind it.
func (s HookEntryStatus) Damage() string {
	var parts []string
	if len(s.Missing) > 0 {
		parts = append(parts, "missing: "+strings.Join(s.Missing, ", "))
	}
	if len(s.Stale) > 0 {
		parts = append(parts, "stale: "+strings.Join(s.Stale, ", "))
	}
	return strings.Join(parts, "; ")
}

// VersionGate is a declared minimum upstream-CLI version for a hook install
// (issue #1365).
//
// It is data, not behaviour, and that is the whole point. Codex used to carry
// a private codexSupportsHooks/parseCodexVersion pair and its own
// minHookMajor/Minor/Patch constants; Claude Code carried nothing and wrote
// seven hook entries into ~/.claude/settings.json whatever version was
// installed. A third adapter joins the scheme by declaring two literals here —
// a version string and the argv that prints one — and PermissionService
// enforces it for every adapter identically.
type VersionGate struct {
	// Min is the lowest upstream CLI version at which EVERY event this
	// permission installs is known to the CLI. It is a whole-install floor,
	// not a per-event one: the event set never varies WITHIN an artifact. A
	// permission whose artifact differs by major declares one HookVariant per
	// artifact (#2108), each with its own floor and event list, and this gate
	// is then their envelope.
	Min string

	// Max is an EXCLUSIVE ceiling: the first upstream CLI version this
	// install is known NOT to work at (issue #2105). Empty means no ceiling.
	// It exists for a new upstream major
	// that rejects the artifact this install writes — opencode 2.x refuses the
	// 1.x plugin file on every load — so "the CLI is new enough" stops being
	// the only question. Like Min, it gates the whole install, never single
	// events.
	Max string

	// Probe is the argv that asks the installed CLI its version, e.g.
	// {"claude", "--version"}. Used when Observed is nil or yields nothing.
	// Must be a compile-time literal — it is executed.
	Probe []string

	// Observed, when non-nil, supplies the installed version WITHOUT running
	// anything. Codex sets it: the adapter already captures cli_version from
	// every session header, so the newest transcript answers the question for
	// free and works even when the binary is not on the daemon's PATH. It is
	// consulted first; an empty return falls through to Probe. An adapter with
	// no such signal leaves it nil and gets the probe.
	Observed func() string

	// ObservedRequires, when set, names a permission key of the same adapter
	// that must be granted before Observed is consulted (#2108). An Observed
	// that reads something only that permission discloses — opencode's reads
	// the session database, which only its `database` permission covers —
	// declares it here, and PermissionService skips Observed while that key is
	// not granted, falling through to Probe as if Observed were nil. Empty
	// means Observed reads nothing beyond what the gated permission itself
	// discloses, which is every adapter that declared one before this field.
	ObservedRequires string
}

// VersionBound names which side of a VersionGate refused an install.
type VersionBound int

const (
	// WithinBounds: the install may proceed — the version clears every
	// declared bound, or could not be read (unknown fails open).
	WithinBounds VersionBound = iota
	// BelowFloor: the installed CLI is older than Min.
	BelowFloor
	// AtOrAboveCeiling: the installed CLI is at or above Max — or, for a
	// variant declaration, at or above one variant's ceiling and below the
	// next one's floor (SelectVariant).
	AtOrAboveCeiling
	// VersionUnknown: a variant declaration (#2108) could not read the
	// installed version, so it cannot tell which artifact to write. Only
	// SelectVariant reports it: a single-artifact gate fails open on an
	// unknown version instead (cliversion.AtLeast).
	VersionUnknown
)

// Permits reports whether an install may proceed at the given installed
// version, and when it may not, why.
//
// Pure comparison — resolving `installed` (reading a transcript, running a
// binary) belongs to the caller, which keeps process execution out of the
// domain. An unknown or unparseable version permits the install: see
// cliversion.AtLeast for why unknown fails open rather than closed.
func (g *VersionGate) Permits(installed string) (bool, string) {
	bound, why := g.Check(installed)
	return bound == WithinBounds, why
}

// Check is Permits that also says WHICH bound refused, so a caller can act on
// the difference (#2105): PermissionService removes an earlier install on a
// ceiling refusal and never on a floor refusal.
func (g *VersionGate) Check(installed string) (VersionBound, string) {
	if g == nil {
		return WithinBounds, ""
	}
	// The unknown-fails-open decision is AtLeast's and is encoded there once:
	// it reports ok=true, known=false for anything it could not read — an
	// undeclared (empty) bound included, so neither branch needs its own
	// emptiness guard. Re-deriving that here would mean two places decide the
	// direction and the contract arm that claims to pin it would only be
	// pinning this copy. The ceiling therefore refuses only on known && ok.
	if ok, _ := cliversion.AtLeast(installed, g.Min); !ok {
		return BelowFloor, fmt.Sprintf(
			"installed CLI is %s, which is older than the %s required by the hook events this installs; "+
				"nothing was written — upgrade the CLI and grant again",
			installed, g.Min)
	}
	if ok, known := cliversion.AtLeast(installed, g.Max); known && ok {
		return AtOrAboveCeiling, fmt.Sprintf(
			"installed CLI is %s, and this hook install supports only versions below %s%s; "+
				"nothing was written",
			installed, g.Max, unsupportedMajor(g.Max))
	}
	return WithinBounds, ""
}

// Bounded reports whether the gate declares any bound at all — a floor, a
// ceiling, or both. It is the one answer to "is there a gate to enforce?", so
// a caller does not re-derive it from the fields: PermissionService asked
// `Min != ""` before the ceiling existed (#2105), which would have skipped a
// ceiling-only gate without a word.
func (g *VersionGate) Bounded() bool {
	return g != nil && (g.Min != "" || g.Max != "")
}

// unsupportedMajor renders " (N.x is not supported yet)" for a ceiling that is
// a major boundary, which is the case a ceiling exists for — the user reads
// "2.x" more readily than "2.0.0". Any other ceiling renders nothing extra.
func unsupportedMajor(max string) string {
	v, ok := cliversion.Parse(max)
	if !ok || v.Minor != 0 || v.Patch != 0 {
		return ""
	}
	return fmt.Sprintf(" (%d.x is not supported yet)", v.Major)
}

// HookVariant is one per-major artifact of a hook install (#2108): the range
// of upstream CLI versions it installs at, the events it subscribes to, and
// the closures that install and verify it. See ManagedUserFile.Variants.
type HookVariant struct {
	// Min is the lowest CLI version this artifact installs at (inclusive) and
	// Max the first one it does not (exclusive). The same convention as
	// VersionGate's: Min is the version the artifact was measured against
	// rather than one bisected backward, and Max is the first version known to
	// reject it.
	Min string
	Max string

	// Events is every hook event this artifact subscribes to — the installer's
	// own slice, never a copy, so the consent copy and the contracts that
	// grade it read the list the artifact is built from.
	Events []string

	// Since maps each of Events to the CLI version it is known to exist at.
	// Min must cover every entry (contracttesting.AssertHookVersionGate).
	Since map[string]string

	// Apply writes this artifact at ManagedUserFile.Path, rewriting another
	// variant's file there in place. Verify reports, read-only, whether the
	// file there is THIS artifact as it would be written now: another
	// variant's file reads Stale, which is what makes the #1372 re-verification
	// rewrite it after an upgrade.
	Apply  func() error
	Verify func() (HookEntryStatus, error)
}

// gate is the variant's range as a VersionGate, so a variant's bounds are
// compared by the one comparison every single-artifact gate uses.
func (v *HookVariant) gate() *VersionGate {
	return &VersionGate{Min: v.Min, Max: v.Max}
}

// SelectVariant decides what an install at the installed version does (#2108):
// which variant it writes, or which bound refused it and why. A declaration
// with no Variants answers exactly as VersionGate.Check does and selects
// nothing — its own Apply is the install.
//
// For a variant declaration, in order:
//
//   - The envelope (Version) is checked first, with Check's own rules: below
//     its floor is BelowFloor, at or above its ceiling is AtOrAboveCeiling.
//   - An unreadable version is VersionUnknown. Check fails open on it, and a
//     single artifact may: the one file it writes is the only one there is.
//     Two artifacts cannot, because guessing the major wrong writes exactly
//     the file the other major rejects — opencode 2.x's `failed to load
//     plugin` on every load, measured in #2104 item 10.
//   - A version inside a variant's range selects that variant.
//   - A version between two variants — at or above one's ceiling, below the
//     next one's floor — is AtOrAboveCeiling of the lower one: that lower
//     artifact is the file an earlier install may have left, and its ceiling
//     says the CLI rejects it, so the removal a ceiling refusal triggers is
//     the one PermissionService already makes (#2105).
//
// The variants' order is irrelevant, and so is whether they are disjoint:
// overlap is a declaration error contracttesting.AssertHookVersionGate
// reports, and this returns the first match rather than deciding between two.
func (f *ManagedUserFile) SelectVariant(installed string) (*HookVariant, VersionBound, string) {
	if f == nil {
		return nil, WithinBounds, ""
	}
	if bound, why := f.Version.Check(installed); bound != WithinBounds || len(f.Variants) == 0 {
		return nil, bound, why
	}
	if _, ok := cliversion.Parse(installed); !ok {
		return nil, VersionUnknown, fmt.Sprintf(
			"the installed CLI's version %s, so which of the %d hook artifacts this permission "+
				"installs (%s) it loads is unknown; nothing was written — a wrong guess writes the file "+
				"the other major rejects",
			unreadable(installed), len(f.Variants), f.variantRanges())
	}
	inside, lower, upper := f.placeAmongVariants(installed)
	if inside != nil {
		return inside, WithinBounds, ""
	}
	if lower == nil {
		return nil, BelowFloor, fmt.Sprintf(
			"installed CLI is %s, which is older than %s, the floor of the oldest hook artifact this "+
				"permission installs; nothing was written — upgrade the CLI and grant again",
			installed, upper.Min)
	}
	next := ""
	if upper != nil {
		next = fmt.Sprintf(", and older than %s, where the next one starts", upper.Min)
	}
	return nil, AtOrAboveCeiling, fmt.Sprintf(
		"installed CLI is %s, which is at or above %s, the ceiling of the hook artifact for %s%s; "+
			"nothing was written",
		installed, lower.Max, lower.Range(), next)
}

// placeAmongVariants locates installed among the variants: the one whose range
// holds it, or else its neighbours — the variant with the highest ceiling at or
// below it (lower) and the one with the lowest floor above it (upper).
func (f *ManagedUserFile) placeAmongVariants(installed string) (inside, lower, upper *HookVariant) {
	for i := range f.Variants {
		v := &f.Variants[i]
		switch bound, _ := v.gate().Check(installed); bound {
		case WithinBounds:
			return v, nil, nil
		case AtOrAboveCeiling:
			if lower == nil || cliversion.Less(lower.Max, v.Max) {
				lower = v
			}
		case BelowFloor:
			if upper == nil || cliversion.Less(v.Min, upper.Min) {
				upper = v
			}
		}
	}
	return nil, lower, upper
}

// variantRanges renders every variant's range, e.g. "1.14.50 up to 2.0.0,
// 2.1.4 up to 3.0.0", for a refusal that has to name them all.
func (f *ManagedUserFile) variantRanges() string {
	ranges := make([]string, len(f.Variants))
	for i := range f.Variants {
		ranges[i] = f.Variants[i].Range()
	}
	return strings.Join(ranges, ", ")
}

// Range renders the variant's version range for a refusal or a log line,
// e.g. "2.1.4 up to 3.0.0", or "2.1.4 or newer" with no ceiling.
func (v *HookVariant) Range() string {
	if v.Max == "" {
		return v.Min + " or newer"
	}
	return v.Min + " up to " + v.Max
}

// VariantEvents is every variant's events, each once, in declaration order —
// the union the consent copy must name and what a check that cannot tell
// which variant applies reports.
func (f *ManagedUserFile) VariantEvents() []string {
	var events []string
	seen := map[string]bool{}
	for _, v := range f.Variants {
		for _, e := range v.Events {
			if !seen[e] {
				seen[e] = true
				events = append(events, e)
			}
		}
	}
	return events
}

// unreadable renders why a version could not be used, for VersionUnknown's
// refusal: an empty one was never learnt, anything else did not parse.
func unreadable(installed string) string {
	if installed == "" {
		return "could not be learnt"
	}
	return fmt.Sprintf("%q is not a version", installed)
}

// Identity is the always-required adapter metadata served via
// GET /api/v1/agents. Frontends key off Name; DisplayName + icons are
// purely presentational.
type Identity struct {
	Name         string // adapter label on events, e.g. "claude-code"
	DisplayName  string // human-readable label, e.g. "Claude Code"
	IconSVGLight string // raw <svg>…</svg> markup, light theme
	IconSVGDark  string // raw <svg>…</svg> markup, dark theme
}

// SkipsPreSession reports whether the scanner mints no pre-session for a
// matched process with this argv: one ExcludeArgv rejects or SessionHostArgv
// recognizes.
func (p Process) SkipsPreSession(argv []string) bool {
	return (p.ExcludeArgv != nil && p.ExcludeArgv(argv)) ||
		(p.SessionHostArgv != nil && p.SessionHostArgv(argv))
}

// Process bundles the two universal process-related contracts every
// adapter must declare: how to recognize the agent's OS processes, and
// how to map a session (by cwd + transcript path) back to a single PID.
type Process struct {
	Match         ProcessMatcher
	PIDForSession PIDDiscoverFunc
	// SharedPIDOwner is optional. It keeps an existing same-adapter root only
	// when that root independently proves it still owns the shared PID.
	// Declaring it also makes the session detector admit a stale transcript
	// that reaches admission as a new session (the startup scan, or a backfill
	// after consent is granted) when PIDForSession names a live process holding
	// it open (#2081). So an adapter declaring it must have a PIDForSession that
	// is a transcript-writer probe, never a cwd match: a cwd match would admit
	// every stale transcript in a directory one of its processes runs in. Its
	// Source must also declare ParentSessionIDFromPath, because that admission
	// excludes subagents only through the event's ParentSessionID.
	//
	// Both requirements bind only an adapter whose transcripts can be stale.
	// A DB-backed key — a TranscriptPath carrying "?session=", which a
	// ProcessOwnedStore adapter's watcher emits (opencode, #2107) — never is:
	// the session detector's isStaleTranscript answers false for it, so its
	// admitStaleTranscript admits on its first line and never asks the
	// stale-transcript writer probe; and such a watcher's EventNewSession
	// carries the session's ParentSessionID itself (opencode: emitNewSession,
	// from session_v2.parent_id). Read in core/application/services
	// (helpers.go, session_detector_activity.go) and in opencode's watcher.go,
	// and pinned by TestAdmitStaleTranscript_DBBackedKeyNeverAsksTheWriterProbe.
	SharedPIDOwner SharedPIDOwnerFunc
	// ReleasedPID is optional. It ends a root session whose PID is still alive
	// but has provably released the session's transcript, for an agent whose
	// sessions are hosted by a process that outlives them, so no exit ever
	// arrives (#2080). Declaring it also makes the session detector follow a
	// session's transcript to a newer file the watcher maps to the same
	// session id, because the probe means something only for the file the
	// agent writes now.
	ReleasedPID ReleasedPIDFunc
	// LauncherPID is optional. For a session bound to a host process, it
	// names the process whose environment and tty feed the session's
	// launcher instead (#2083); see LauncherPIDFunc. The session's PID is
	// untouched.
	LauncherPID LauncherPIDFunc

	// ExcludeArgv, when non-nil, lets an adapter reject a matched process by
	// inspecting its argv. The process scanner consults it after a PID
	// matches Match: returning true means "this is the agent's binary but not
	// a session" (e.g. a background daemon / wrapper that runs the same
	// binary) and no pre-session is minted. The format-specific argv shapes
	// live in the adapter package; the scanner stays generic. A nil argv
	// (unreadable, e.g. hardened-runtime) is passed through, so an adapter's
	// predicate must default to *not* excluding when it can't tell.
	ExcludeArgv func(argv []string) bool

	// SessionHostArgv, when non-nil, recognizes a matched process that hosts
	// this agent's sessions on behalf of the processes a user talks to — the
	// shared codex app-server (#2077), opencode 2.x's service (#2105). The
	// scanner mints no pre-session for it, as for ExcludeArgv. Unlike
	// ExcludeArgv, a root session legitimately binds to it, so nothing reaps a
	// session for being bound to it as infrastructure. With a LauncherPID also
	// declared, a root bound to it retires the pre-session its client left
	// behind (#2082); with none declared, a root bound to it captures no
	// launcher, because the host's env is the spawning client's (#2107). With
	// no ReleasedPID declared, the host's being alive no longer keeps an idle
	// ready root from the ready-session TTL (#2107): the host outlives its
	// sessions and nothing else ends one. A nil argv must not match, as for
	// ExcludeArgv.
	SessionHostArgv func(argv []string) bool

	// RequireKnownHost, when true, gates session admission on the bound
	// process's OS-level ancestry: a candidate PID whose parent chain doesn't
	// resolve to a recognized terminal emulator or IDE is rejected, and no
	// session is created for it. Unlike ExcludeArgv (adapter-specific argv
	// shapes), the ancestry check itself is generic and shared — this is a
	// plain opt-in flag, not a predicate. Added for issue #784: a third-party
	// menu-bar app (CodexBar) kept an Antigravity `agy` CLI process running in
	// the background for quota polling, with no distinguishing argv or cwd
	// signal, so identity has to come from who launched the process instead.
	RequireKnownHost bool
}
