package main

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"sort"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/inbound/agents/processlifecycle"
	gastownadapter "irrlicht/core/adapters/inbound/orchestrators/gastown"
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// TestPrintManagedFilesCoversEveryDeclaredFile pins the second half of
// #1357's coverage requirement, widened to #1383's file set: the recording rig
// reads its protected file set from `irrlichd --print-managed-files`, so
// anything the consent catalog declares and this output omits is a file a
// grant-all recording daemon would rewrite in the user's real $HOME and never
// hand back.
//
// Contract test — it passes by construction; it was seen red by making
// printManagedFiles emit only the first entry (see the PR body).
func TestPrintManagedFilesCoversEveryDeclaredFile(t *testing.T) {
	configs, err := agents.ManagedUserFiles(declaredConsentCatalog())
	if err != nil {
		t.Fatalf("agents.ManagedUserFiles(declaredConsentCatalog()): %v", err)
	}
	if len(configs) == 0 {
		t.Fatal("no permission declares a managed user file — this check would pass vacuously")
	}

	var buf bytes.Buffer
	if err := printManagedFiles(&buf); err != nil {
		t.Fatalf("printManagedFiles: %v", err)
	}

	printed := linesOf(t, buf.String())

	want := map[string]bool{}
	for _, c := range configs {
		want[c.Path] = true
		if !printed[c.Path] {
			t.Errorf("%s/%s declares %q, which --print-managed-files does not list: "+
				"a recording would rewrite that file and never restore it (#1383)", c.Adapter, c.Key, c.Path)
		}
	}
	for p := range printed {
		if !want[p] {
			t.Errorf("--print-managed-files listed %q, which no permission declares", p)
		}
	}
}

// linesOf splits the flag's output into a set, failing on a repeated line: the
// rig keys its backups by line index, so one file listed twice would be backed
// up under two indices and restored twice.
func linesOf(t *testing.T, out string) map[string]bool {
	t.Helper()
	got := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		if got[line] {
			t.Errorf("printed %q twice", line)
		}
		got[line] = true
	}
	return got
}

// fakePermissionStore is an in-memory consent store, so the coverage check
// never reads or writes the developer's real permissions.json.
type fakePermissionStore struct {
	set   permission.Set
	saved permission.Set
}

func (f *fakePermissionStore) Load() (permission.Set, error) { return f.set, nil }
func (f *fakePermissionStore) Save(s permission.Set) error   { f.saved = s.Clone(); return nil }

// TestUninstallHookConfigsCoversEveryConfig pins the first half: --uninstall-hooks
// must run EVERY declared uninstaller and record EVERY matching permission as
// denied. Missing one leaves that agent's hook entries in the user's real config
// permanently, firing on every turn at a dead port; and a hooks permission left
// "granted" reinstalls them on the next daemon start via the Apply closure,
// silently reverting the uninstall (#570).
//
// Contract test — driven by synthetic configs so it stays true of an adapter set
// that has not been written yet. Seen red by truncating the loop to the first
// config (see the PR body).
func TestUninstallHookConfigsCoversEveryConfig(t *testing.T) {
	var called []string
	configs := []agents.ManagedUserFile{
		{Adapter: "alpha", Key: "hooks", Path: "/tmp/alpha/settings.json",
			Uninstall: func() (bool, error) { called = append(called, "alpha"); return true, nil }},
		{Adapter: "beta", Key: "hooks", Path: "/tmp/beta/hooks.json",
			Uninstall: func() (bool, error) { called = append(called, "beta"); return false, nil }},
		{Adapter: "gamma", Key: "hooks", Path: "/tmp/gamma/settings.json",
			Uninstall: func() (bool, error) { called = append(called, "gamma"); return true, nil }},
	}

	store := &fakePermissionStore{set: permission.Set{}}
	for _, c := range configs {
		store.set.Put(c.Adapter, c.Key, permission.StateGranted)
	}

	var out bytes.Buffer
	uninstallHookConfigs(&out, configs, store)

	sort.Strings(called)
	if got, want := strings.Join(called, ","), "alpha,beta,gamma"; got != want {
		t.Errorf("uninstalled %q, want %q — an adapter whose uninstaller never runs keeps its hook entries forever (#1357)", got, want)
	}

	if store.saved == nil {
		t.Fatal("granted hooks permissions were never persisted as denied")
	}
	for _, c := range configs {
		if st := store.saved.Get(c.Adapter, c.Key); st != permission.StateDenied {
			t.Errorf("%s/%s is %q after uninstall, want %q — a still-granted hooks permission reinstalls on the next start", c.Adapter, c.Key, st, permission.StateDenied)
		}
	}

	// Each file is named in the report, so a CODEX_HOME user is told which
	// file was actually cleaned rather than the default one.
	for _, c := range configs {
		if !strings.Contains(out.String(), c.Path) {
			t.Errorf("the uninstall report never mentions %q:\n%s", c.Path, out.String())
		}
	}
}

// TestUninstallHookConfigsLeavesUngrantedPermissionsAlone is a lock: uninstall
// records an opt-out only where consent had actually been given, so a pending
// permission is not silently turned into a denial the wizard will stop asking
// about.
func TestUninstallHookConfigsLeavesUngrantedPermissionsAlone(t *testing.T) {
	configs := []agents.ManagedUserFile{
		{Adapter: "alpha", Key: "hooks", Path: "/tmp/alpha/settings.json",
			Uninstall: func() (bool, error) { return false, nil }},
	}
	store := &fakePermissionStore{set: permission.Set{}}

	uninstallHookConfigs(&bytes.Buffer{}, configs, store)

	if store.saved != nil {
		t.Errorf("persisted %v for a permission that was never granted", store.saved)
	}
}

// TestWriteManagedFilePathsDeduplicates pins the dedup the rig depends on: it
// keys its backups by line index, so one file listed twice would be backed up
// under two indices and restored twice. Driven directly so the invariant is
// pinned independently of what the catalog happens to declare — but since #1383
// declared claudecode's statusline as well as its hooks, both resolving to
// ~/.claude/settings.json, the SHIPPED catalog now produces the duplicate too,
// and TestPrintManagedFilesCoversEveryDeclaredFile's linesOf catches it there.
func TestWriteManagedFilePathsDeduplicates(t *testing.T) {
	var buf bytes.Buffer
	writeManagedFilePaths(&buf, []agents.ManagedUserFile{
		{Adapter: "alpha", Key: "hooks", Path: "/tmp/shared/settings.json"},
		{Adapter: "beta", Key: "hooks", Path: "/tmp/shared/settings.json"},
		{Adapter: "gamma", Key: "hooks", Path: "/tmp/gamma/hooks.json"},
	})
	got := strings.Split(strings.TrimSpace(buf.String()), "\n")
	want := []string{"/tmp/shared/settings.json", "/tmp/gamma/hooks.json"}
	if len(got) != len(want) {
		t.Fatalf("printed %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestUninstallHookConfigsSurvivesOneFailingAdapter pins the fix for a failure
// that used to end the whole command: hookjson refuses to rewrite a config it
// cannot parse, so a single hand-edited ~/.claude/settings.json aborted the
// process before any later adapter was cleaned and before ANY hooks permission
// was recorded denied — leaving them granted, which re-installs on the next
// daemon start via the Apply closure (#570).
func TestUninstallHookConfigsSurvivesOneFailingAdapter(t *testing.T) {
	var called []string
	configs := []agents.ManagedUserFile{
		{Adapter: "alpha", Key: "hooks", Path: "/tmp/alpha/settings.json",
			Uninstall: func() (bool, error) {
				called = append(called, "alpha")
				return false, errors.New("invalid character '}' looking for beginning of object key string")
			}},
		{Adapter: "beta", Key: "hooks", Path: "/tmp/beta/hooks.json",
			Uninstall: func() (bool, error) { called = append(called, "beta"); return true, nil }},
	}
	store := &fakePermissionStore{set: permission.Set{}}
	for _, c := range configs {
		store.set.Put(c.Adapter, c.Key, permission.StateGranted)
	}

	var out bytes.Buffer
	failed := uninstallHookConfigs(&out, configs, store)

	if failed != 1 {
		t.Errorf("failed = %d, want 1 — the caller needs a non-zero exit", failed)
	}
	if strings.Join(called, ",") != "alpha,beta" {
		t.Errorf("uninstalled %q, want %q — a failing adapter must not stop the ones after it", called, "alpha,beta")
	}
	if store.saved == nil {
		t.Fatal("no permission was recorded denied — the still-granted hooks reinstall on the next start")
	}
	if st := store.saved.Get("beta", "hooks"); st != permission.StateDenied {
		t.Errorf("beta/hooks = %q, want %q", st, permission.StateDenied)
	}
	if !strings.Contains(out.String(), "warning: failed to uninstall hooks from /tmp/alpha/settings.json") {
		t.Errorf("the failure is not reported to the user:\n%s", out.String())
	}
}

// TestEveryModifyPermissionDeclaresTheFileItWrites is the #1383 contract, and
// the reason the declaration is no longer hooks-shaped.
//
// A recording daemon runs IRRLICHT_PERMISSION_MODE=grant-all against the user's
// REAL $HOME, so it exercises EVERY modify permission's Apply closure — not
// just the ones that install hooks. Any file such a closure writes that the
// catalog does not declare is a file the recording rig cannot back up and
// cannot hand back: after the recorder is gone the user is left carrying
// content they may have explicitly denied.
//
// The rule keys on **a non-nil Apply**, not on "declares hooks" and not on the
// permission's Kind: Apply is exactly what grant-all runs, while Kind is the
// label the adapter author picked for the wizard row. Keying on Kind would let
// a future permission that writes $HOME escape by being declared observe-kind —
// the same shape as #1383 point 4, one field over.
//
// A permission whose Apply writes nothing therefore has to be named here with
// its reason, rather than falling out silently. Today there is exactly one:
// gastown/state, whose Apply starts a filesystem watcher. agent.ControlPermission
// is not in the list because its Apply is nil — it falls outside by
// construction, which is the shape to prefer.
//
// It runs over the FULL consent catalog — agents.All() PLUS the three
// daemon-wide declarations — because that is what the wizard offers and
// therefore what grant-all grants. Scoping it to the adapter slice is how the
// kitty config patch stayed invisible to both lists (#1383 point 4).
//
// Defect test: seen red on origin/main for claude-code/statusline,
// claude-code/instructions and kitty/remote-control (see the PR body).
func TestEveryModifyPermissionDeclaresTheFileItWrites(t *testing.T) {
	catalog := declaredConsentCatalog()
	if len(catalog) == 0 {
		t.Fatal("empty consent catalog — this check would pass vacuously")
	}

	checked := 0
	for _, a := range catalog {
		for _, p := range a.Permissions {
			if p.Apply == nil {
				continue
			}
			id := a.Identity.Name + "/" + p.Key
			if reason, exempt := applyWritesNoUserFile[id]; exempt {
				if p.Writes != nil {
					t.Errorf("%s is listed as writing no user file (%q) but declares one — "+
						"remove it from applyWritesNoUserFile", id, reason)
				}
				continue
			}
			checked++
			if p.Writes == nil {
				t.Errorf("%s has an Apply closure but declares no agent.ManagedUserFile: "+
					"a grant-all recording daemon runs that closure against the user's real $HOME and the recording rig "+
					"has no way to learn which file to hand back (#1383). If it genuinely writes no shared user file, "+
					"add it to applyWritesNoUserFile with the reason", id)
			}
		}
	}
	if checked == 0 {
		t.Fatal("every permission with an Apply closure is exempt — the check is vacuous")
	}
}

// applyWritesNoUserFile names every permission whose Apply closure runs under
// grant-all yet writes no shared, user-owned file, with the reason.
//
// It is an explicit list on purpose. The alternative — inferring the exemption
// from permission.Kind — is what let gastown/state sit outside the rule
// invisibly: it is observe-kind, so a Kind-based predicate skipped it without
// anyone deciding that it should be skipped. An entry here is a claim someone
// wrote down and a reviewer can check; a missing entry is a test failure rather
// than silence.
var applyWritesNoUserFile = map[string]string{
	"gastown/state": "Apply starts and stops the Gas Town watcher; it reads ~/gt and writes nothing",
}

// TestPrintManagedFilesNamesTheFilesTheIssueFound is the concrete arm of the
// contract above: the two files #1383 was filed about have to actually appear
// in what the rig reads. The general rule can be satisfied by a declaration
// that resolves somewhere else entirely; this one says which paths, so a wrong
// resolver is a failure rather than a coverage tick.
//
// Defect test: seen red on origin/main, whose --print-hook-configs listed only
// ~/.claude/settings.json and ~/.codex/hooks.json (see the PR body).
func TestPrintManagedFilesNamesTheFilesTheIssueFound(t *testing.T) {
	var buf bytes.Buffer
	if err := printManagedFiles(&buf); err != nil {
		t.Fatalf("printManagedFiles: %v", err)
	}
	out := buf.String()

	for _, want := range []struct{ label, suffix string }{
		{"claude-code/instructions", "/.claude/CLAUDE.md"},
		{"kitty/remote-control", "/kitty/kitty.conf"},
	} {
		found := false
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if strings.HasSuffix(line, want.suffix) {
				found = true
			}
		}
		if !found {
			t.Errorf("--print-managed-files never names a %s file, so a grant-all recording leaves %s's "+
				"managed block in the user's real file (#1383):\n%s", want.suffix, want.label, out)
		}
	}
}

// TestConsentCatalogMatchesThePermissionWizard pins the composition itself.
// The projection is only as complete as the catalog it runs over, and the
// catalog the flag paths build has to be the same one setupPermissionService
// hands the permission service — two lists that can disagree about which
// declarations exist is #1383 point 4 in miniature. It replaces
// TestNonAgentPermissionDeclarationsDeclareNoHooks, whose premise (the
// non-agent entries are NOT projected, so they must declare nothing) the fix
// dissolves.
//
// Lock: it passes by construction as long as both callers go through
// consentCatalog. It was seen red by giving the flag path its own
// agents.All()-only list (see the PR body).
//
// Its companion below is what actually binds setupPermissionService to this
// composition; on its own, this test only checks the names the flag path
// happens to produce.
func TestConsentCatalogMatchesThePermissionWizard(t *testing.T) {
	got := map[string]bool{}
	for _, a := range declaredConsentCatalog() {
		got[a.Identity.Name] = true
	}
	for _, a := range agents.All() {
		if !got[a.Identity.Name] {
			t.Errorf("the consent catalog omits the %q adapter", a.Identity.Name)
		}
	}
	for _, name := range []string{gastownadapter.Name, processlifecycle.LauncherName, processlifecycle.KittyName} {
		if !got[name] {
			t.Errorf("the consent catalog omits the daemon-wide %q declaration, which setupPermissionService "+
				"offers in the wizard — anything it writes would be invisible to --print-managed-files (#1383)", name)
		}
	}
}

// TestUninstallHooksReadsOnlyTheHooksSlice is the counterpart on the other
// side of the widening: --uninstall-hooks must keep meaning what its name says.
// The catalog now also declares the CLAUDE.md instruction blocks and the kitty
// remote-control block; running their uninstallers from this command would
// revoke capabilities the user asked nothing about, and record their consent as
// denied so the wizard stops offering them.
//
// Lock relative to origin/main (where the projection was hooks-only by
// construction); a regression test relative to this branch, where the narrowing
// is a deliberate filter that can be removed. Seen red by making uninstallHooks
// read agents.ManagedUserFiles instead of agents.HookConfigs (see the PR body).
func TestUninstallHooksReadsOnlyTheHooksSlice(t *testing.T) {
	catalog := declaredConsentCatalog()
	hooks, err := agents.HookConfigs(catalog)
	if err != nil {
		t.Fatalf("agents.HookConfigs: %v", err)
	}
	if len(hooks) == 0 {
		t.Fatal("no hooks permission in the catalog — this check would pass vacuously")
	}
	for _, h := range hooks {
		if h.Key != agent.HooksPermissionKey {
			t.Errorf("HookConfigs returned %s/%s, which is not a hooks permission — "+
				"--uninstall-hooks would revoke something the user never asked it to", h.Adapter, h.Key)
		}
	}

	all, err := agents.ManagedUserFiles(catalog)
	if err != nil {
		t.Fatalf("agents.ManagedUserFiles: %v", err)
	}
	if len(all) <= len(hooks) {
		t.Errorf("ManagedUserFiles returned %d entries and HookConfigs %d — the projection is still hooks-only (#1383)",
			len(all), len(hooks))
	}
}

// TestUninstallTaskEtaReadsOnlyTheInstructionsSlice is the mirror image, and
// the reason InstructionConfigs is a projection rather than a direct call to
// claudecode.UninstallInstructionBlocks (#1437): now that --uninstall-task-eta
// records consent as denied, reaching for the wrong slice would silently revoke
// the hook installs and the kitty patch too.
//
// A LOCK on the narrowing. It passes by construction against a correct
// projection; its value is that it fails the moment somebody widens the filter.
func TestUninstallTaskEtaReadsOnlyTheInstructionsSlice(t *testing.T) {
	catalog := declaredConsentCatalog()
	instructions, err := agents.InstructionConfigs(catalog)
	if err != nil {
		t.Fatalf("agents.InstructionConfigs: %v", err)
	}
	if len(instructions) == 0 {
		t.Fatal("no instructions permission in the catalog — every check below would pass vacuously")
	}

	t.Run("every entry is an instructions permission", func(t *testing.T) {
		for _, c := range instructions {
			if c.Key != agent.InstructionsPermissionKey {
				t.Errorf("InstructionConfigs returned %s/%s, which is not an instructions permission — "+
					"--uninstall-task-eta would revoke something the user never asked it to", c.Adapter, c.Key)
			}
		}
	})

	t.Run("it is strictly narrower than the full projection", func(t *testing.T) {
		all, err := agents.ManagedUserFiles(catalog)
		if err != nil {
			t.Fatalf("agents.ManagedUserFiles: %v", err)
		}
		if len(all) <= len(instructions) {
			t.Errorf("ManagedUserFiles returned %d entries and InstructionConfigs %d — the narrowing is not narrowing",
				len(all), len(instructions))
		}
	})

	// Disjointness is asserted rather than assumed. Each command denies exactly
	// the permissions its projection names, so an overlap would mean one of them
	// revokes the other's capability — the failure both narrowings exist to
	// prevent, in the one shape neither single-sided check above can see.
	t.Run("it is disjoint from the hooks slice", func(t *testing.T) {
		hooks, err := agents.HookConfigs(catalog)
		if err != nil {
			t.Fatalf("agents.HookConfigs: %v", err)
		}
		inHooks := make(map[string]bool, len(hooks))
		for _, h := range hooks {
			inHooks[h.Adapter+"/"+h.Key] = true
		}
		for _, c := range instructions {
			if inHooks[c.Adapter+"/"+c.Key] {
				t.Errorf("%s/%s is in BOTH HookConfigs and InstructionConfigs — "+
					"the two uninstall commands would revoke each other's consent", c.Adapter, c.Key)
			}
		}
	})
}

// TestPermissionWizardIsBuiltFromConsentCatalog closes the gap the test above
// leaves open: it asserts the list the permission SERVICE receives is built by
// consentCatalog, rather than trusting the doc comment that says so.
//
// The projections are only as complete as the catalog they run over, and
// nothing else in the tree connects the two. A future daemon-wide declaration
// appended at the call site — `append(consentCatalog(...), somethingNew())`, or
// a second inline literal, which is exactly how the kitty entry lived before
// this PR — would be offered by the wizard, granted by grant-all, and written by its
// Apply closure, while every other test here stayed green because they all read
// the same incomplete declaredConsentCatalog().
//
// Scanning the source is the same technique TestAllHookEvents_CoversEveryConstant
// uses, and for the same reason: a hand-kept restatement of "these two lists
// agree" is what the issue is about. There is no runtime handle to assert on —
// PermissionService does not expose the slice it was constructed with.
func TestPermissionWizardIsBuiltFromConsentCatalog(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package source: %v", err)
	}

	var rhs []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				assign, ok := n.(*ast.AssignStmt)
				if !ok {
					return true
				}
				for i, lhs := range assign.Lhs {
					id, ok := lhs.(*ast.Ident)
					if !ok || id.Name != permissionAgentsVar {
						continue
					}
					if i < len(assign.Rhs) {
						rhs = append(rhs, callee(assign.Rhs[i]))
					}
				}
				return true
			})
		}
	}

	if len(rhs) != 1 {
		t.Fatalf("found %d assignments to %s (%v), want exactly 1 — the list handed to "+
			"PermissionService must have one origin, or --print-managed-files projects a "+
			"different catalog than the wizard offers (#1383)", len(rhs), permissionAgentsVar, rhs)
	}
	if rhs[0] != "consentCatalog" {
		t.Errorf("%s is assigned from %q, want a bare consentCatalog(...) call — anything "+
			"wrapping or extending it is a declaration the wizard offers, grant-all grants, "+
			"and the recording rig never protects (#1383)", permissionAgentsVar, rhs[0])
	}
}

// permissionAgentsVar is the local in setupPermissionService holding the
// catalog handed to PermissionService. Named here so a rename breaks this test
// loudly (zero assignments found) rather than silently making it vacuous.
const permissionAgentsVar = "permissionAgents"

// callee renders the shape of an expression for the assertion above: the
// function name for a plain call, and something deliberately unmatchable for
// anything else, so a wrapped or extended call cannot read as a bare one.
func callee(e ast.Expr) string {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return fmt.Sprintf("%T (not a call)", e)
	}
	id, ok := call.Fun.(*ast.Ident)
	if !ok {
		return fmt.Sprintf("%T (not a plain function call)", call.Fun)
	}
	return id.Name
}
