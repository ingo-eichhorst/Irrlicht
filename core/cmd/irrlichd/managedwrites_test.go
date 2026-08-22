// managedwrites_test.go closes the gap issue #1741 was filed about, one file
// over from managedfiles_test.go's TestEveryModifyPermissionDeclaresTheFileItWrites.
//
// That test asks whether a modify-kind permission with a non-nil Apply HAS a
// Writes declaration. It never asks whether the declaration COVERS every file
// that Apply actually writes, and the difference has produced two real defects:
//
//   - #1718/#1731 — kiro-cli's and mistral-vibe's installs each wrote a SECOND
//     shared user file no declaration named. Fixed by adding
//     agent.ManagedUserFile.Also.
//   - #1739 — kiro-cli's recordPriorDefaultAgentOnce writes
//     $KIRO_HOME/.irrlicht-prior-default.json, undeclared even AFTER Also
//     existed. Not inert: that function deliberately never overwrites, so a
//     copy left behind by a recording is what a LATER real grant reads instead
//     of the operator's actual chat.defaultAgent.
//
// An undeclared file is invisible to `--print-managed-files`, therefore to the
// recording rig's snapshot_managed_files backup, and therefore to #1449's
// PermissionService.sharedConfigRefusal. All three read the declaration.
//
// # Two arms, because one mechanism cannot reach every permission
//
// RUNTIME (TestEveryModifyPermissionDeclaresEveryFileItsApplyWrites) is the
// arm that carries the weight: it runs each Apply against a scratch $HOME and
// grades the files that actually changed. That is the only thing that sees a
// path built inline with filepath.Join inside Apply — the shape #1741 measured
// the obvious static rule to miss entirely — and it needs no per-adapter
// knowledge, so a NEW modify permission is graded by existing rather than by
// someone remembering this file.
//
// STATIC (TestExternalInstallerPackagesDeclareEveryPathResolver) exists for the
// one permission the runtime arm must not execute. kiro-cli's Apply shells out
// to the real kiro-cli binary (`agent create`, `agent set-default`), and
// running it inside `go test ./core/...` was measured, not assumed, to be
// non-deterministic in two independent ways:
//
//   - The binary is present on a developer machine and absent on a CI runner.
//     With it absent, EnsureHooksInstalled fails at its first step and writes
//     NOTHING — the check would pass having graded nothing, which is exactly
//     the "inability to look reads as a clean result" failure
//     docs/testing-philosophy.md is about.
//   - With it present, the CHILD process writes its own state: measured on
//     2026-08-22, `kiro-cli agent create irrlicht --from kiro_default` under a
//     scratch $HOME created
//     $HOME/Library/Application Support/kiro-cli/data.sqlite3 (see the PR body
//     for the raw output). Which further files it writes depends on whether
//     the developer is logged in — a verdict nobody controls.
//
// So kiro-cli's Apply is not run. Its permission is named in
// applyRunsAnExternalCLI with that reason, and the static arm grades its
// package instead: every package-level `func() (string, error)` there must be
// one the declaration names, or be named in notAManagedFilePath with why. The
// declared side is DERIVED from production by reflecting the function names out
// of Writes.Path/Writes.Also, so it cannot drift from a hand-kept list, and the
// walk is vacuity-guarded by requiring every reflected name to be found in the
// package it claims to have parsed.
//
// #1741 measured the static rule at roughly one false positive per adapter —
// every adapter has a home-ROOT resolver with that exact signature — which is
// why it is scoped to the packages the runtime arm cannot execute rather than
// run catalog-wide. One exemption today, not eight.
//
// # What NEITHER arm covers
//
// Stated here rather than left to be discovered; the corpus in
// managedwrites_shapes_test.go pins each of these that a case can express.
//
//   - A write to an absolute path that follows neither $HOME nor any
//     environment variable (a hard-coded /etc/... say) lands outside the walked
//     scratch tree and is invisible to the runtime arm. The static arm sees it
//     only if it goes through a package-level resolver.
//   - A write that leaves size, mtime AND content byte-identical is not a
//     change any snapshot diff can see.
//   - A permission that creates only a DIRECTORY (no file in it) is not
//     reported: the walk records non-directory entries.
//   - gastown/state's Apply is the no-op declaredConsentCatalog passes, not the
//     watcher the daemon wires, so running it grades the no-op. It declares no
//     Writes and is already covered by applyWritesNoUserFile next door.
//   - The static arm reads FuncDecls. A resolver held in a package-level var
//     (`var p = func() (string, error) {…}`) is not seen — fail-closed in the
//     wrong direction here, since it would read as "no undeclared resolver", so
//     it is pinned as a want:false corpus row rather than left implied. It is
//     the same declared limit core/architecture_hookbody_test.go and
//     contracttesting/seam_walk_corpus_test.go carry for their own walks.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"irrlicht/core/domain/agent"
)

// ---------------------------------------------------------------------------
// Declarations
// ---------------------------------------------------------------------------

// externalInstaller describes a permission whose Apply spawns an agent's own
// CLI, which is why the runtime arm must not execute it. Binary is checked
// against the permission's OWN declared version probe, so an entry cannot name
// a tool the permission has nothing to do with.
type externalInstaller struct {
	// Binary is the executable Apply spawns. It must equal the first element
	// of the permission's Writes.Version.Probe — the same constant the adapter
	// resolves the binary from — so this map cannot claim a shellout the
	// declaration does not corroborate.
	Binary string
	// Reason is why running it inside `go test` is not acceptable.
	Reason string
}

// applyRunsAnExternalCLI names every permission whose Apply the runtime arm
// refuses to execute, with the reason. Keys are existence-checked in both
// directions: an entry naming a permission that does not exist, or one whose
// Apply is nil, fails — an exemption for an obligation nobody had reads as
// coverage.
//
// It is deliberately a list rather than a derived predicate. "Does this
// package contain exec.Command" is derivable and wrong: processlifecycle
// contains several and kitty's Apply spawns none of them, so the derived
// version would wave through the one permission in that package that a scratch
// home CAN grade. Reachability from Apply is the property, and nothing short of
// a call-graph walk decides it — so this is a claim someone wrote down and a
// reviewer can check, the same trade applyWritesNoUserFile makes next door.
var applyRunsAnExternalCLI = map[string]externalInstaller{
	"kiro-cli/hooks": {
		Binary: "kiro-cli",
		Reason: "EnsureHooksInstalled shells out to `kiro-cli agent create` and " +
			"`kiro-cli agent set-default` (kiroctl.go). Measured 2026-08-22: with the " +
			"binary ABSENT the closure fails at its first step and writes nothing, so the " +
			"runtime arm would grade an empty set; with it PRESENT the child writes its own " +
			"state under $HOME/Library/Application Support/kiro-cli/, and how much more it " +
			"writes depends on whether the developer is logged in. Either way the verdict " +
			"stops being a property of this repo. Graded by the static arm instead",
	},
}

// notAManagedFilePath names package-level (string, error) resolvers in an
// external-installer package that legitimately name no managed user file, with
// the reason. Keys are "<package>.<func>" and are existence-checked: an entry
// the walk does not find is stale and fails.
//
// #1741 measured this class at roughly one per adapter — the home ROOT every
// other path in the package is built on. Scoping the static arm to the packages
// the runtime arm cannot execute is what keeps this map at one entry.
var notAManagedFilePath = map[string]string{
	"kirocli.kiroHome": "resolves $KIRO_HOME itself — the directory every other resolver in " +
		"the package joins onto, not a file anything writes",
}

// scratchAllowances are the subtrees of the scratch home whose contents are not
// graded, with the reason. Deliberately NOT existence-checked: this is a
// BOUNDARY, not a finding, so an allowance that matches nothing is the ordinary
// case rather than a stale entry.
func scratchAllowances(sh scratchHome) []allowance {
	rel, err := filepath.Rel(sh.Root, sh.IrrlichtHome)
	if err != nil {
		rel = filepath.Base(sh.IrrlichtHome)
	}
	return []allowance{{
		Root: rel,
		Reason: "IRRLICHT_HOME — the daemon's OWN state, which agent.ManagedUserFile is " +
			"defined against rather than a member of (a managed file follows $HOME and " +
			"outlives the daemon; this tree does neither)",
	}}
}

// ---------------------------------------------------------------------------
// The runtime arm
// ---------------------------------------------------------------------------

// TestEveryModifyPermissionDeclaresEveryFileItsApplyWrites runs each modify
// permission's Apply against a scratch $HOME and grades the files that changed
// against what the permission declares.
//
// Two obligations, and both directions are load-bearing:
//
//   - Every file Apply WROTE is declared. This is #1741 itself — an undeclared
//     write is invisible to --print-managed-files, to the recording rig's
//     backup, and to #1449's refusal.
//   - Every file the permission DECLARES was written. This is the vacuity
//     guard, and it is the one that makes a misconfigured sandbox loud instead
//     of clean: if $HOME had not taken, every closure would write to the real
//     home, the scratch diff would be EMPTY, and a check that only looked for
//     undeclared writes would report a flawless sweep.
//
// Contract test — it passes by construction against a correct catalog, so its
// whole value is that it can fail. Seen red by deleting mistral-vibe's Also
// entry and by neutering kitty's write (see the PR body); the corpus in
// managedwrites_shapes_test.go carries the committed mutation evidence for the
// grading itself.
func TestEveryModifyPermissionDeclaresEveryFileItsApplyWrites(t *testing.T) {
	sh := newScratchHome(t)

	// Built AFTER the scratch home is in place: three adapters compute a static
	// Source.Dir at Agent() construction time, so a registry built earlier is
	// frozen against the developer's real environment (the same ordering
	// righome.registryAdapterAfterEnv documents).
	catalog := declaredConsentCatalog()
	if len(catalog) == 0 {
		t.Fatal("empty consent catalog — this check would pass vacuously")
	}

	// FAIL CLOSED. Every declared path in the WHOLE catalog is resolved and
	// proven to land inside the scratch tree before a single closure runs.
	//
	// #1449's PermissionService.sharedConfigRefusal asks the same lexical
	// question, and is deliberately not what runs here: it refuses one Apply at
	// a time, from inside the service, so the first eight closures would have
	// executed before the ninth's escape was noticed. A test that executes
	// installers needs containment established for all of them up front, and it
	// needs it without an IRRLICHT_ALLOW_SHARED_CONFIG_WRITES escape hatch in
	// reach.
	graded := gradablePermissions(t, catalog)
	assertEveryDeclaredPathIsContained(t, sh, graded)

	allowed := scratchAllowances(sh)
	for _, g := range graded {
		t.Run(g.id, func(t *testing.T) { applyAndGrade(t, sh, g, allowed) })
	}

	if len(graded) == 0 {
		t.Fatal("no permission was executed — every modify permission is exempt, so this " +
			"check graded nothing")
	}
}

// assertEveryDeclaredPathIsContained is the fail-closed precondition, and it
// runs over the WHOLE set before any closure does — see the call site for why
// #1449's per-Apply guard is not what runs here.
func assertEveryDeclaredPathIsContained(t *testing.T, sh scratchHome, graded []gradablePermission) {
	t.Helper()
	for _, g := range graded {
		for _, f := range g.declared {
			if f.err != nil {
				t.Fatalf("%s: resolving %s failed (%v) — a test that cannot name the file a "+
					"closure is about to write must not run that closure", g.id, f.label, f.err)
			}
			if !filepath.IsAbs(f.abs) || !pathUnderDir(sh.Root, f.abs) {
				t.Fatalf("%s declares %s = %q, which is OUTSIDE the scratch home %q. "+
					"Refusing to run any Apply: this test would be writing to a shared config "+
					"the developer actually uses", g.id, f.label, f.abs, sh.Root)
			}
		}
	}
}

// applyAndGrade runs one permission's Apply and reports both directions.
func applyAndGrade(t *testing.T, sh scratchHome, g gradablePermission, allowed []allowance) {
	t.Helper()
	before := walkScratch(t, sh.Root)
	applyErr := g.perm.Apply()
	after := walkScratch(t, sh.Root)

	if applyErr != nil {
		t.Fatalf("Apply returned %v — the closure was cut short, so the file set below "+
			"is whatever it managed before failing and grading it would report a "+
			"partial sweep as a clean one. If this permission's install genuinely "+
			"cannot complete in a scratch home, name it in applyRunsAnExternalCLI "+
			"with the reason", applyErr)
	}

	v := gradeWrites(treeDiff(before, after), g.declaredRel(sh.Root), allowed)
	for _, p := range v.Undeclared {
		t.Errorf("Apply wrote %q, which %s declares in neither Writes.Path nor "+
			"Writes.Also. That file is invisible to --print-managed-files, so the "+
			"recording rig never backs it up and #1449's grant-all refusal never sees "+
			"it (#1741). Declare it, or explain it as an allowance", p, g.id)
	}
	for _, p := range v.Missing {
		t.Errorf("%s declares %q but running Apply changed nothing there. Either the "+
			"declaration resolves to the wrong file, or the closure no longer writes "+
			"it — and if NEITHER is true, the scratch home is not where the writes "+
			"went, which makes every 'no undeclared write' result above meaningless",
			g.id, p)
	}
}

// gradablePermission is one permission the runtime arm will execute, with its
// declaration already resolved.
type gradablePermission struct {
	id       string
	perm     agent.Permission
	declared []declaredFile
}

// declaredFile is one entry of a permission's Writes, resolved.
type declaredFile struct {
	label string // "Writes.Path" / "Writes.Also[0]"
	abs   string
	err   error
}

func (g gradablePermission) declaredRel(root string) []string {
	out := make([]string, 0, len(g.declared))
	for _, f := range g.declared {
		rel, err := filepath.Rel(root, f.abs)
		if err != nil {
			continue
		}
		out = append(out, rel)
	}
	return out
}

// gradablePermissions selects the permissions the runtime arm executes and
// validates every exemption's key while it is at it: an entry in
// applyRunsAnExternalCLI that names no real permission, or one whose Binary
// disagrees with the permission's own version probe, is a claim of coverage
// nobody can check.
func gradablePermissions(t *testing.T, catalog []agent.Agent) []gradablePermission {
	t.Helper()

	seen := map[string]bool{}
	var out []gradablePermission
	for _, a := range catalog {
		for _, p := range a.Permissions {
			if p.Apply == nil {
				continue
			}
			id := a.Identity.Name + "/" + p.Key
			seen[id] = true
			if ext, skip := applyRunsAnExternalCLI[id]; skip {
				assertExternalInstallerAgrees(t, id, ext, p)
				continue
			}
			out = append(out, gradablePermission{id: id, perm: p, declared: resolveDeclared(p)})
		}
	}

	for id := range applyRunsAnExternalCLI {
		if !seen[id] {
			t.Errorf("applyRunsAnExternalCLI names %q, which is not a permission with an Apply "+
				"closure in the consent catalog — an exemption for an obligation that does not "+
				"exist reads as coverage", id)
		}
	}
	return out
}

// assertExternalInstallerAgrees ties an exemption to the permission's own
// declaration. Without it the map is free text: any permission could be waved
// out of the runtime arm by asserting it shells out to something.
func assertExternalInstallerAgrees(t *testing.T, id string, ext externalInstaller, p agent.Permission) {
	t.Helper()
	if strings.TrimSpace(ext.Reason) == "" {
		t.Errorf("applyRunsAnExternalCLI[%q] carries no reason", id)
	}
	if p.Writes == nil || p.Writes.Version == nil || len(p.Writes.Version.Probe) == 0 {
		t.Errorf("applyRunsAnExternalCLI[%q] claims Apply spawns %q, but the permission "+
			"declares no Writes.Version.Probe to corroborate it", id, ext.Binary)
		return
	}
	if got := p.Writes.Version.Probe[0]; got != ext.Binary {
		t.Errorf("applyRunsAnExternalCLI[%q] names binary %q, but the permission's own version "+
			"probe runs %q — one of the two is stale and a reader cannot tell which",
			id, ext.Binary, got)
	}
}

// resolveDeclared resolves Writes.Path and every Writes.Also entry, keeping the
// resolution error rather than dropping it: a resolver that fails is a file we
// cannot prove is inside the scratch home, which is the fail-closed case.
func resolveDeclared(p agent.Permission) []declaredFile {
	if p.Writes == nil {
		return nil
	}
	var out []declaredFile
	if p.Writes.Path != nil {
		abs, err := p.Writes.Path()
		out = append(out, declaredFile{label: "Writes.Path", abs: abs, err: err})
	}
	for i, also := range p.Writes.Also {
		abs, err := also()
		out = append(out, declaredFile{label: fmt.Sprintf("Writes.Also[%d]", i), abs: abs, err: err})
	}
	return out
}

// ---------------------------------------------------------------------------
// The scratch home
// ---------------------------------------------------------------------------

// scratchHome is the tree every closure under test is confined to.
type scratchHome struct {
	// Root is what the walk covers.
	Root string
	// Home is $HOME, inside Root.
	Home string
	// IrrlichtHome is $IRRLICHT_HOME, also inside Root so a write there is
	// SEEN and explained by an allowance rather than landing outside the walk.
	IrrlichtHome string
}

// envKeptDuringScrub names the environment variables the scratch home leaves
// alone. Everything else is emptied.
//
// An ALLOW-list, not a deny-list, and that is the whole point. A deny-list has
// to enumerate every override an adapter might honour — CODEX_HOME,
// COPILOT_HOME, KIRO_HOME, VIBE_HOME, XDG_CONFIG_HOME, CLAUDE_CONFIG_DIR today
// — and the day a new adapter reads a new one, its Apply writes to the
// developer's real config while this test believes it is sandboxed. Emptying
// everything means an unknown override resolves to "" and the adapter falls
// back to its $HOME-relative default, which IS the scratch home.
func envKeptDuringScrub(name string) bool {
	switch name {
	case "PATH", "TMPDIR", "TMP", "TEMP":
		return true
	}
	// The go tooling's own variables. No adapter path resolution reads one, and
	// clearing them buys nothing.
	return strings.HasPrefix(name, "GO")
}

// scrubCanary is set, then swept up by the scrub itself, so "the scrub ran" is
// observed rather than assumed. Without it a scrub loop that silently did
// nothing would leave the developer's real overrides in place and the test
// would report on their real config.
const scrubCanary = "IRRLICHT_MANAGEDWRITES_SCRUB_CANARY"

// newScratchHome builds the tree and points the process at it.
func newScratchHome(t *testing.T) scratchHome {
	t.Helper()
	root := t.TempDir()
	sh := scratchHome{
		Root:         root,
		Home:         filepath.Join(root, "home"),
		IrrlichtHome: filepath.Join(root, "irrlicht-home"),
	}
	for _, dir := range []string{sh.Home, sh.IrrlichtHome} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("creating %s: %v", dir, err)
		}
	}

	t.Setenv(scrubCanary, filepath.Join(string(filepath.Separator), "definitely", "not", "the", "scratch", "home"))
	for _, kv := range os.Environ() {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || envKeptDuringScrub(name) {
			continue
		}
		t.Setenv(name, "")
	}
	if got := os.Getenv(scrubCanary); got != "" {
		t.Fatalf("the environment scrub left %s=%q — it did not run, so every per-agent home "+
			"override the developer exports is still in effect and the closures below would "+
			"write to their real config", scrubCanary, got)
	}

	t.Setenv("HOME", sh.Home)
	t.Setenv("IRRLICHT_HOME", sh.IrrlichtHome)

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("os.UserHomeDir after pointing HOME at the scratch home: %v", err)
	}
	if home != sh.Home {
		t.Fatalf("os.UserHomeDir() = %q, want the scratch home %q — every $HOME-anchored "+
			"resolver below would land in the developer's real home", home, sh.Home)
	}
	return sh
}

// ---------------------------------------------------------------------------
// Snapshot / diff / grading — the pure half, driven by the corpus
// ---------------------------------------------------------------------------

// fileState is what one walk records for one path. Content AND mtime AND size:
// a rewrite that happens to produce identical bytes still moves mtime, so
// hashing alone would miss it, and hashing is what tells two same-sized
// same-second writes apart.
type fileState struct {
	Size    int64
	ModNano int64
	Sum     string
	Mode    fs.FileMode
}

// walkScratch records every non-directory entry under root, keyed relative to
// it. Symlinks are recorded by their target rather than followed: a closure
// that plants a link into the user's home has written something, and following
// it would hash whatever it points at instead.
func walkScratch(t *testing.T, root string) map[string]fileState {
	t.Helper()
	out := map[string]fileState{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		st := fileState{Size: info.Size(), ModNano: info.ModTime().UnixNano(), Mode: info.Mode()}
		if d.Type()&fs.ModeSymlink != 0 {
			target, linkErr := os.Readlink(p)
			if linkErr != nil {
				return linkErr
			}
			st.Sum = "-> " + target
		} else if d.Type().IsRegular() {
			data, readErr := os.ReadFile(p) // #nosec G304 -- path comes from walking a t.TempDir()
			if readErr != nil {
				return readErr
			}
			sum := sha256.Sum256(data)
			st.Sum = hex.EncodeToString(sum[:])
		}
		out[rel] = st
		return nil
	})
	if err != nil {
		t.Fatalf("walking the scratch home %s: %v — a walk that cannot read the tree reports "+
			"the same empty diff as a closure that wrote nothing", root, err)
	}
	return out
}

// treeDiff returns every path whose state differs between two walks — created,
// modified, or REMOVED. Removal counts: a permission that deletes a shared user
// file has written to it in every sense this declaration is about.
func treeDiff(before, after map[string]fileState) []string {
	changed := map[string]bool{}
	for p, now := range after {
		if was, existed := before[p]; !existed || was != now {
			changed[p] = true
		}
	}
	for p := range before {
		if _, still := after[p]; !still {
			changed[p] = true
		}
	}
	out := make([]string, 0, len(changed))
	for p := range changed {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// allowance is a subtree of the scratch home whose contents are not graded.
type allowance struct {
	Root   string
	Reason string
}

// writeVerdict is what gradeWrites found.
type writeVerdict struct {
	// Undeclared are observed paths in neither the declared set nor an
	// allowance — issue #1741 itself.
	Undeclared []string
	// Missing are declared paths nothing wrote — the vacuity direction.
	Missing []string
	// Allowed counts, per allowance root, how many observed paths it absorbed,
	// so a caller that wants to check an allowance is not dead can.
	Allowed map[string]int
}

// gradeWrites is the whole judgement, as a pure function so the corpus in
// managedwrites_shapes_test.go can drive it with inputs the live catalog cannot
// produce. All three inputs are scratch-relative paths.
func gradeWrites(observed, declared []string, allowed []allowance) writeVerdict {
	v := writeVerdict{Allowed: map[string]int{}}
	for _, a := range allowed {
		v.Allowed[a.Root] = 0
	}

	isDeclared := make(map[string]bool, len(declared))
	for _, d := range declared {
		isDeclared[d] = true
	}

	seen := map[string]bool{}
	for _, p := range observed {
		if isDeclared[p] {
			seen[p] = true
			continue
		}
		if root, ok := allowanceFor(p, allowed); ok {
			v.Allowed[root]++
			continue
		}
		v.Undeclared = append(v.Undeclared, p)
	}
	for _, d := range declared {
		if !seen[d] {
			v.Missing = append(v.Missing, d)
		}
	}
	sort.Strings(v.Undeclared)
	sort.Strings(v.Missing)
	return v
}

// allowanceFor reports which allowance absorbs p, if any.
func allowanceFor(p string, allowed []allowance) (string, bool) {
	for _, a := range allowed {
		if pathUnderRel(a.Root, p) {
			return a.Root, true
		}
	}
	return "", false
}

// pathUnderRel reports whether the relative path p is root itself or lies
// beneath it. Segment-aware on purpose: a string-prefix test would let an
// allowance for "Library/App" absorb "Library/Apple.txt", which is a different
// file entirely.
func pathUnderRel(root, p string) bool {
	root = filepath.Clean(root)
	p = filepath.Clean(p)
	return p == root || strings.HasPrefix(p, root+string(filepath.Separator))
}

// pathUnderDir is pathUnderRel for absolute paths, and is the containment rule
// the fail-closed precondition uses. Deliberately lexical, for the same reason
// services.pathInsideRoot is: the input is our own resolver's output rather
// than a caller's, and the only direction this can err in is refusing to run,
// which is the safe one.
func pathUnderDir(dir, p string) bool {
	if !filepath.IsAbs(dir) || !filepath.IsAbs(p) {
		return false
	}
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(p))
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// ---------------------------------------------------------------------------
// The static arm
// ---------------------------------------------------------------------------

// TestExternalInstallerPackagesDeclareEveryPathResolver grades the packages the
// runtime arm refuses to execute: every package-level `func() (string, error)`
// in one of them must be a resolver the permission's own declaration names, or
// be named in notAManagedFilePath with why.
//
// That signature is what #1741 measured, including its false-positive rate. The
// reason it is worth running HERE and not catalog-wide is arithmetic: scoped to
// the external-installer packages it costs one exemption, and catalog-wide it
// would cost one per adapter while adding nothing the runtime arm does not
// already cover more strongly.
//
// Contract test — it passes by construction; seen red by deleting
// priorDefaultStatePath from kirocli's Writes.Also (see the PR body), which is
// #1739 exactly.
func TestExternalInstallerPackagesDeclareEveryPathResolver(t *testing.T) {
	catalog := declaredConsentCatalog()
	permByID := map[string]agent.Permission{}
	for _, a := range catalog {
		for _, p := range a.Permissions {
			permByID[a.Identity.Name+"/"+p.Key] = p
		}
	}

	if len(applyRunsAnExternalCLI) == 0 {
		t.Skip("no permission is exempt from the runtime arm, so there is nothing for the " +
			"static fallback to grade")
	}

	usedExemptions := map[string]bool{}
	for id := range applyRunsAnExternalCLI {
		p, ok := permByID[id]
		if !ok {
			continue // reported by the runtime arm's own key check
		}
		t.Run(id, func(t *testing.T) {
			gradeExternalInstallerPackage(t, id, p, usedExemptions)
		})
	}

	for key, reason := range notAManagedFilePath {
		if !usedExemptions[key] {
			t.Errorf("notAManagedFilePath names %q (%q), which the walk did not find in any "+
				"external-installer package — a stale exemption reads as coverage", key, reason)
		}
	}
}

func gradeExternalInstallerPackage(t *testing.T, id string, p agent.Permission, usedExemptions map[string]bool) {
	t.Helper()

	declared := declaredResolverNames(p)
	if len(declared) == 0 {
		t.Fatalf("%s declares no path resolver at all, so this arm has nothing to anchor on", id)
	}
	pkgDir, pkgName, err := packageDirOfResolver(declared[0])
	if err != nil {
		t.Fatalf("%s: %v", id, err)
	}

	found, err := packageLevelPathResolvers(pkgDir)
	if err != nil {
		t.Fatalf("%s: parsing %s: %v — a walk that cannot read the package reports the same "+
			"empty result as a package with nothing wrong in it", id, pkgDir, err)
	}
	if len(found) == 0 {
		t.Fatalf("%s: the walk found no package-level (string, error) resolver in %s at all, "+
			"yet the declaration names %d — the walk is looking at the wrong place",
			id, pkgDir, len(declared))
	}

	isDeclared := map[string]bool{}
	for _, qualified := range declared {
		name := qualified[strings.LastIndex(qualified, ".")+1:]
		isDeclared[name] = true
		// Vacuity guard: every name the declaration reflects out MUST be one
		// the walk saw, or the two are describing different code.
		if !containsString(found, name) {
			t.Errorf("%s declares resolver %q, which the walk did not find in %s — the "+
				"declaration and the walk disagree about which package this permission "+
				"lives in, so every verdict below is about the wrong files", id, name, pkgDir)
		}
	}

	for _, name := range found {
		if isDeclared[name] {
			continue
		}
		key := pkgName + "." + name
		if _, exempt := notAManagedFilePath[key]; exempt {
			usedExemptions[key] = true
			continue
		}
		t.Errorf("%s.%s is a package-level path resolver that %s declares in neither "+
			"Writes.Path nor Writes.Also. If its Apply writes that file, it is invisible to "+
			"--print-managed-files, to the recording rig's backup and to #1449's grant-all "+
			"refusal (#1739). Declare it, or add %q to notAManagedFilePath with the reason "+
			"it names no managed file", pkgName, name, id, key)
	}
}

// declaredResolverNames reflects the fully qualified function names out of a
// permission's Writes, so the "declared" side of the static arm is derived from
// production rather than restated. A closure or method value yields a name the
// package walk cannot match, which surfaces as the vacuity failure above rather
// than as silence.
func declaredResolverNames(p agent.Permission) []string {
	if p.Writes == nil {
		return nil
	}
	var out []string
	add := func(f func() (string, error)) {
		if f == nil {
			return
		}
		fn := runtime.FuncForPC(reflect.ValueOf(f).Pointer())
		if fn == nil {
			return
		}
		out = append(out, fn.Name())
	}
	add(p.Writes.Path)
	for _, also := range p.Writes.Also {
		add(also)
	}
	return out
}

// packageDirOfResolver turns "irrlicht/core/adapters/.../kirocli.foo" into the
// directory holding that package and its short name. The module is
// "irrlicht/core" rooted at core/, so the import path's first segment is
// dropped to reach a repo-relative directory.
func packageDirOfResolver(qualified string) (dir, pkgName string, err error) {
	dot := strings.LastIndex(qualified, ".")
	if dot < 0 {
		return "", "", fmt.Errorf("reflected resolver name %q has no package qualifier", qualified)
	}
	importPath := qualified[:dot]
	const modulePrefix = "irrlicht/"
	if !strings.HasPrefix(importPath, modulePrefix) {
		return "", "", fmt.Errorf("reflected resolver name %q is not in this module", qualified)
	}
	rel := strings.TrimPrefix(importPath, modulePrefix)
	// This test's own package is core/cmd/irrlichd.
	dir = filepath.Join("..", "..", "..", filepath.FromSlash(rel))
	if _, statErr := os.Stat(dir); statErr != nil {
		return "", "", fmt.Errorf("resolver %q maps to %s, which does not exist: %w", qualified, dir, statErr)
	}
	return dir, path7Base(rel), nil
}

func path7Base(importPath string) string {
	if i := strings.LastIndex(importPath, "/"); i >= 0 {
		return importPath[i+1:]
	}
	return importPath
}

// packageLevelPathResolvers returns the names of every package-level
// `func() (string, error)` declared in dir's non-test files, sorted.
//
// FuncDecls only, and with no receiver: a method is not something a
// ManagedUserFile can name, and a func literal in a package-level var hangs off
// a ValueSpec rather than a FuncDecl. Both are pinned as want:false rows in the
// corpus rather than left as folklore.
func packageLevelPathResolvers(dir string) ([]string, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			out = append(out, pathResolversIn(file)...)
		}
	}
	sort.Strings(out)
	return out, nil
}

// pathResolversIn names the package-level `func() (string, error)` FuncDecls in
// one file. A receiver disqualifies: a method is not something a
// ManagedUserFile field can hold.
func pathResolversIn(file *ast.File) []string {
	var out []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil {
			continue
		}
		if isPathResolverSignature(fn.Type) {
			out = append(out, fn.Name.Name)
		}
	}
	return out
}

// isPathResolverSignature reports whether ft is exactly `() (string, error)`.
func isPathResolverSignature(ft *ast.FuncType) bool {
	if ft == nil || ft.Results == nil {
		return false
	}
	if ft.Params != nil && len(ft.Params.List) != 0 {
		return false
	}
	if ft.TypeParams != nil && len(ft.TypeParams.List) > 0 {
		return false
	}
	var results []string
	for _, f := range ft.Results.List {
		id, ok := f.Type.(*ast.Ident)
		if !ok {
			return false
		}
		n := 1
		if len(f.Names) > 1 {
			n = len(f.Names)
		}
		for i := 0; i < n; i++ {
			results = append(results, id.Name)
		}
	}
	return len(results) == 2 && results[0] == "string" && results[1] == "error"
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
