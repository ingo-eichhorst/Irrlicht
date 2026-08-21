// hookcontracts_test.go is the registry-wide tripwire for issue #1740: an
// adapter that installs hooks into a user's config and receives them on the
// daemon's local, unauthenticated port must WIRE the contract families that
// grade those two acts, and until this file existed nothing said so.
//
// # The gap
//
// docs/testing-contracts.md names the hazard inside the path-confinement
// bullet, and it is general to every family:
//
//	a contract can fail only for an adapter that WIRED it, so a receiver
//	nobody wired it for is invisible to it — which is how the statusline
//	endpoint shipped unconfined in the first place.
//
// Two obligations over hooks-declaring adapters are already immune to it,
// because what they ask for is a struct FIELD an adapter declares in
// production code: TestEveryHookInstallDeclaresAVerifier (#1372) and
// TestEveryHookInstallDeclaresAVersionFloor (#1365) both walk All() and fail
// for an adapter that omitted the declaration, before that adapter has a test
// of its own.
//
// The families in requiredWirings() below have no field to walk. They are
// wired as a TEST CALL in the adapter's own test package, so their only
// enforcement was that whoever added the adapter remembered to copy an
// existing adapter's test files. "Harder to detect" is not "less important":
// the three #1740 was filed about are the ones whose absence is least visible
// in production — an unconfined caller-supplied path (#1361/#1389), entries
// pointing at a dead port with the permission still reading granted (#1178),
// and content written to a user's config that the consent copy never named
// (#1356/#570).
//
// # Which option this is, and what was rejected
//
// #1740 sketched two, a static walk and a registration seam, and called the
// seam stronger because it grades what actually EXECUTED rather than what is
// textually present. The seam was investigated and rejected on a fact about
// the go tool, not on cost:
//
//   - A registration seam means contracttesting.AssertX records "I ran for
//     adapter P", and a registry-walking test asserts the recorded set covers
//     every hooks-declaring adapter. But `go test ./core/...` compiles ONE
//     TEST BINARY PER PACKAGE. The recorder would run in six adapter processes
//     and the asserting test in a seventh, so in-process state cannot reach it.
//
//   - Carrying it across processes means a file. That file is a cache, and it
//     is a cache the reader cannot validate: a stale entry from an earlier run
//     makes a wiring someone has since DELETED read as covered, which is the
//     precise failure mode #1740 exists to end, reintroduced with a longer
//     half-life. Nothing in the reader can distinguish "P registered during
//     this invocation" from "P registered last Tuesday" — `go test` gives the
//     asserting binary no roster of which sibling packages ran, and the go
//     build cache legitimately skips re-running an unchanged package's tests,
//     so "P did not register" is also the ordinary answer for a cached pass.
//     A seam that is wrong in the green direction on a stale file and wrong in
//     the red direction on a cache hit is not stronger than a static walk; it
//     is a worse oracle wearing an execution-grading label.
//
//   - The remaining way to genuinely grade execution is to invert: have ONE
//     package construct every adapter's receiver and installer and run the
//     contracts itself. That requires each adapter to declare its test fixture
//     — a handler, a confiner, roots, a payload builder — in PRODUCTION code,
//     which is the API smell #1390 removed rather than tested ("a contract
//     that grows a sub-test to police an API the same PR invented is a signal
//     to remove the API"). Not taken.
//
// So: the static walk, with its limits stated below rather than implied, and
// built to be worth more than the grep #1740 assumed a static walk would be.
// It is an AST + type walk, not a text match, which removes both weaknesses
// the issue named:
//
//   - A reference in a COMMENT is not an AST node, so it cannot satisfy this.
//   - A DEAD test cannot satisfy it either: a call must be reachable, through
//     package-local calls, from a function `go test` actually runs. A helper
//     nothing calls is reported separately (Unreached), so the failure says
//     "you have a dead wiring" rather than "you have nothing".
//
// It is also stronger than a grep in the other direction — it resolves the
// callee's TYPE, so `import ct "…/contracttesting"; ct.AssertHookPathConfined`
// counts (a grep for "contracttesting.AssertHookPathConfined" would not), and
// a local helper that merely shares the NAME does not.
//
// # What it does NOT cover
//
// Stated here rather than left to be discovered, and pinned as executable
// corpus rows in hookcontracts_shapes_test.go wherever a row can express it:
//
//   - PACKAGE GRANULARITY, which is the big one and is MEASURED rather than
//     reasoned about. The walk asks whether the adapter's test package calls a
//     family's entry point, never which RECEIVER or which PERMISSION that call
//     was for. Two real instances in this tree, both established by mutation
//     while #1740 was being built:
//
//     Replacing the AssertHookReceiverPermissionGated call in claudecode's
//     hooks_test.go with a local no-op left that adapter's row GREEN, because
//     claudecode's statusline receiver wires the same family in the same
//     package. claudecode hosts two receivers and #1361's incident was the
//     second one, so this is precisely the shape that started all of it: this
//     walk does not close it. hookjson.DecodeConfined and
//     core/architecture_hookbody_test.go do, by making an unconfined body read
//     fail to build — which is why that pair, not this walk, is the real
//     enforcement for confinement.
//
//     AssertPermissionGated is excluded from requiredWirings() for the same
//     reason (see unenforceableHere): claudecode's package calls it for its
//     INSTRUCTIONS permission, so a row demanding it would be satisfied by that
//     call while claudecode's hooks install has no such wiring at all.
//
//     What the walk DOES still catch, and what #1740 is actually about, is the
//     new adapter that wires a family nowhere at all.
//
//   - A test that is SKIPPED (t.Skip) or a call guarded by a constant false
//     condition still counts. Reachability here is syntactic; it does not
//     evaluate.
//
//   - A wiring reached through a func literal held in a PACKAGE-LEVEL VAR is
//     NOT seen, because the literal's body hangs off a ValueSpec rather than a
//     FuncDecl. That direction is fail-closed — it reports a real wiring as
//     missing, which is loud — and it is the same declared limit
//     contracttesting/seam_walk_corpus_test.go pins for its own walk.
//
//   - A wiring reached through a helper in ANOTHER package (other than the
//     adapter's own external test package, which is merged in) is not seen.
//     Fail-closed again.
//
//   - An entry point taken as a VALUE and called later (`f :=
//     contracttesting.AssertX; f(t, r)`) is not seen. Fail-closed. Requiring a
//     call expression is what makes `var _ = contracttesting.AssertX` — a
//     reference that executes nothing — not count.
//
//   - It cannot say the wiring is CORRECT. That is what the contract families
//     themselves are for; this only says one is present and runs.
package agents

import (
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"maps"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"golang.org/x/tools/go/packages"

	"irrlicht/core/domain/agent"
)

// contracttestingPkg is the package whose exported entry points a hooks
// adapter must call. Spelled as a string rather than reached through an import
// for the same reason architecture_hookbody_test.go spells its chokepoint out:
// this file must be able to REPORT that the package or a function moved, and a
// rule that resolved the symbol would stop compiling instead — a worse failure
// mode for a guard whose job is to stay legible when the thing it guards
// breaks. assertRequiredWiringsExist turns that string back into a checked
// fact on every run.
const contracttestingPkg = "irrlicht/core/internal/contracttesting"

// adapterTreePrefix is the import path every adapter package lives under. Used
// only to sanity-check a derived package path, so a declaration composed out of
// some shared helper cannot silently redirect the walk at a package that holds
// no adapter's tests.
const adapterTreePrefix = "irrlicht/core/adapters/inbound/agents/"

// requiredWiring is one contracttesting entry point every hooks-declaring
// adapter must call from its own test package.
//
// Why is not decoration. A row here costs a future adapter author real work,
// so each states what the family's ABSENCE looks like in production — which is
// the argument for demanding it, and the thing to re-read before deleting a
// row.
type requiredWiring struct {
	Fn     string
	Family string
	Why    string
}

// requiredWirings is the set. Seven rows, and every hooks-declaring adapter in
// the tree satisfies all seven the day this landed — which is exactly the
// condition the mutation rule in docs/testing-philosophy.md exists for, and why
// hookcontracts_shapes_test.go commits the detector's discriminating cases
// rather than leaving the evidence in a PR body.
//
// #1740 names the first three. The other four have the identical shape — wired
// as a test call, enforced by nothing — and were included by measuring rather
// than assumed out: every hooks-declaring adapter already wires all seven, so
// they cost nothing today and cover four more families tomorrow.
//
// That claim needs no separately-maintained figure, because THIS TEST is what
// produces it: "every hooks-declaring adapter wires every row" is true exactly
// when TestEveryHookInstallWiresItsContractFamilies is green. The roster it
// runs over is derived from All() rather than written down, so neither the
// count of adapters nor the count of rows can drift away from what was
// measured. To see the per-adapter breakdown directly:
//
//	for f in $(git grep -ho 'func Assert[A-Za-z]*' core/internal/contracttesting/ | sed 's/^func //'); do
//	  echo "== $f"; git grep -l "contracttesting.$f" -- 'core/adapters/inbound/agents/*/*_test.go'
//	done
func requiredWirings() []requiredWiring {
	return []requiredWiring{
		{
			Fn:     "AssertHookPathConfined",
			Family: "hook path confinement (#1361, #1389)",
			Why: "the transcript_path in an inbound hook body is caller-supplied on a local " +
				"unauthenticated endpoint; unwired, a receiver can forward it unconfined and " +
				"anything downstream opens whatever the caller named",
		},
		{
			Fn:     "AssertHookEndpointFollowsBindAddr",
			Family: "hook endpoint follows bind addr (#1178, #1453)",
			Why: "unwired, an install can write a hardcoded :7837 into the user's real config " +
				"and leave it pointing at a dead port with the permission still reading granted",
		},
		{
			Fn:     "AssertHookDisclosureMatchesInstalled",
			Family: "hook disclosure matches installed (#1356, #570)",
			Why: "the consent copy IS the #570 contract; unwired, entries land in the user's " +
				"config that the text they consented to never named",
		},
		{
			Fn:     "AssertHookVersionGate",
			Family: "hook version floors (#1365)",
			Why: "TestEveryHookInstallDeclaresAVersionFloor proves a floor is DECLARED and " +
				"parseable; only this proves the declared floor actually refuses a CLI one " +
				"patch below itself, with a reason naming both versions",
		},
		{
			Fn:     "AssertUnknownHookEventObserved",
			Family: "unrecognized hook events (#1364)",
			Why: "unwired, an upstream event rename is answered 200 and dispatched nowhere, " +
				"with the permission green and nothing anywhere to look at",
		},
		{
			Fn:     "AssertHookReceiptObserved",
			Family: "hook receipts (#1368)",
			Why: "unwired, a working receiver that forgets ObserveHookReceipt is DEMOTED by " +
				"the liveness watchdog — the one receiving-side failure that is a false " +
				"accusation rather than a silence",
		},
		{
			Fn:     "AssertHookReceiverPermissionGated",
			Family: "hook receiver permission gating (#1475, #1488)",
			Why: "unwired, nothing grades the receiver against its own declared permission " +
				"set; copilot's receiver reached #1475 with no wiring at all",
		},
	}
}

// unenforceableHere names contracttesting entry points with the SAME
// wired-as-a-test-call shape as requiredWirings() which this tripwire
// deliberately does NOT demand, each with the reason.
//
// An omission with no entry is indistinguishable from an omission nobody
// noticed, which is #1740 reproduced inside its own fix. So the omissions are
// written down — and, because a reason written once rots, each row's PREMISE is
// re-checked every run by assertExclusionsStillHold.
//
// The single row is here because of this walk's package granularity, measured
// rather than reasoned: claude-code's test package calls AssertPermissionGated
// for its INSTRUCTIONS permission (instructioninstaller_test.go), so a
// requiredWirings() row for it would be satisfied by that call while
// claude-code's HOOKS install has no such wiring at all — a rule that reads as
// coverage and is not, which is worse than a stated gap. The gap itself (five
// of six hooks adapters wire the family for their hooks install; claude-code
// does not) is a finding about claude-code reported in PR #1740, not something
// to paper over with a green row here.
func unenforceableHere() map[string]string {
	return map[string]string{
		"AssertPermissionGated": "every hooks-declaring adapter's test package already calls this " +
			"family, so a required row would discriminate nothing — but for claude-code the call " +
			"is for its INSTRUCTIONS permission, not its hooks install (five of six adapters wire " +
			"it in their own hookinstaller_test.go; claude-code does not). Requiring it would be " +
			"vacuously green there. Reconsider this row if the premise below stops holding.",
	}
}

// hookInstall is one hooks-declaring permission and the Go package that
// declared it.
type hookInstall struct {
	Adapter string
	Key     string
	Pkg     string
}

func TestEveryHookInstallWiresItsContractFamilies(t *testing.T) {
	installs := hookInstallingAdapters(t)
	required := requiredWirings()

	patterns := []string{contracttestingPkg}
	for _, in := range installs {
		patterns = append(patterns, in.Pkg)
	}
	pkgs := loadForWiringScan(t, patterns)

	assertRequiredWiringsExist(t, pkgs, required)

	excluded := unenforceableHere()
	assertExclusionsAreWellFormed(t, pkgs, required, excluded)

	units := testVariantUnitsByPackage(t, pkgs)
	want := make([]string, 0, len(required)+len(excluded))
	for _, r := range required {
		want = append(want, r.Fn)
	}
	want = append(want, slices.Sorted(maps.Keys(excluded))...)

	// wiredEverywhere tracks, per excluded entry point, whether EVERY adapter's
	// package already calls it — which is the stated premise of every row in
	// unenforceableHere() and the thing that makes a required row worthless
	// there. Seeded true and narrowed by the loop; the seed is only correct
	// because hookInstallingAdapters already refused an empty set.
	wiredEverywhere := map[string]bool{}
	for name := range excluded {
		wiredEverywhere[name] = true
	}
	missingSomewhere := map[string]string{}

	for _, in := range installs {
		u := units[in.Pkg]
		if len(u) == 0 {
			t.Fatalf("%s/%s declares a hook install in package %q, but the load returned no "+
				"test variant of that package — this walk would report every family missing "+
				"for a reason that has nothing to do with the adapter",
				in.Adapter, in.Key, in.Pkg)
		}
		scan := scanContractWirings(u, contracttestingPkg, want)
		// Two vacuity guards, because "found no wiring" and "read nothing" must
		// not produce the same output. Fatal rather than Errorf: with either of
		// these true every row below fails for the same wrong reason, and seven
		// misleading failures per adapter buries the one line that explains them.
		if scan.TestFiles == 0 {
			t.Fatalf("%s: walked 0 _test.go files in %q — the scan read no test source at all, "+
				"so any verdict about its wirings is about nothing", in.Adapter, in.Pkg)
		}
		if scan.Roots == 0 {
			t.Fatalf("%s: found no go-test entry function (Test…/Benchmark…/Fuzz…) in %q across "+
				"%d test file(s) — every wiring would be reported unreachable for that reason "+
				"rather than for being absent", in.Adapter, in.Pkg, scan.TestFiles)
		}
		for _, r := range required {
			if _, ok := scan.Called[r.Fn]; ok {
				continue
			}
			if pos, ok := scan.Unreached[r.Fn]; ok {
				t.Errorf("%s (%s) calls contracttesting.%s at %s, but nothing `go test` runs "+
					"reaches that call — %s.\n\tA wiring in a helper no Test function calls is a "+
					"wiring that never executes (issue #1740).",
					in.Adapter, in.Pkg, r.Fn, pos, r.Family)
				continue
			}
			t.Errorf("%s (%s) installs hooks (%s) but its test package never calls "+
				"contracttesting.%s — the %s contract is unenforced for this adapter.\n"+
				"\tWhy it is required: %s.\n"+
				"\tWire it: copy the corresponding *_test.go from an adapter that already does "+
				"(`git grep -l %s core/adapters/inbound/agents/`) and point it at this adapter's "+
				"own receiver/installer. A contract can only fail for an adapter that wired it "+
				"(issue #1740).",
				in.Adapter, in.Pkg, in.Key, r.Fn, r.Family, r.Why, r.Fn)
		}
		for name := range excluded {
			if _, ok := scan.Called[name]; !ok {
				wiredEverywhere[name] = false
				missingSomewhere[name] = in.Adapter
			}
		}
	}

	assertExclusionsStillHold(t, excluded, wiredEverywhere, missingSomewhere)
}

// assertExclusionsStillHold re-checks the premise every unenforceableHere() row
// rests on: that the entry point is ALREADY called by every hooks-declaring
// adapter's package, which is what makes requiring it discriminate nothing.
//
// The polarity is the correction, and it was earned by getting it backwards
// first. The obvious check — "fail once everybody wires it, so the row gets
// promoted" — fires immediately and permanently here, because everybody already
// does; that is the premise, not the trigger. The condition worth reporting is
// the opposite one: an adapter that does NOT call it means a required row would
// now catch something, so the exclusion has stopped being free and needs
// re-reading. An exemption whose stated reason is re-derived each run is the
// only kind that cannot rot into "you don't need to look here".
func assertExclusionsStillHold(t *testing.T, excluded map[string]string, wiredEverywhere map[string]bool, missingSomewhere map[string]string) {
	t.Helper()

	for _, name := range slices.Sorted(maps.Keys(excluded)) {
		if wiredEverywhere[name] {
			continue
		}
		t.Errorf("unenforceableHere() excludes contracttesting.%s on the premise that every "+
			"hooks-declaring adapter's package already calls it, so requiring it would discriminate "+
			"nothing. %s does not call it, so that premise no longer holds.\n"+
			"\tRe-read the row: a required row would now catch a real omission. The reason it was "+
			"excluded was: %s", name, missingSomewhere[name], excluded[name])
	}
}

// assertExclusionsAreWellFormed checks unenforceableHere()'s keys the way
// requiredWirings()' are checked: the name must exist in contracttesting, and it
// must not also be required. An exemption naming something that does not exist,
// or contradicting the requirement table, is worse than no exemption at all —
// it reads as a considered decision while meaning nothing.
func assertExclusionsAreWellFormed(t *testing.T, pkgs []*packages.Package, required []requiredWiring, excluded map[string]string) {
	t.Helper()

	scope := contracttestingScope(t, pkgs)
	isRequired := map[string]bool{}
	for _, r := range required {
		isRequired[r.Fn] = true
	}
	for _, name := range slices.Sorted(maps.Keys(excluded)) {
		if excluded[name] == "" {
			t.Fatalf("unenforceableHere()[%q] carries no reason; an exclusion nobody can justify is "+
				"one nobody can safely delete", name)
		}
		if scope.Lookup(name) == nil {
			t.Fatalf("unenforceableHere() excludes contracttesting.%s, which %s does not declare — the "+
				"entry stopped naming a real thing and now excludes nothing",
				name, contracttestingPkg)
		}
		if isRequired[name] {
			t.Fatalf("contracttesting.%s is both required and excluded; the tables contradict each "+
				"other and only one of them can be the rule", name)
		}
	}
}

// hookInstallingAdapters projects All() onto the hooks permissions, resolving
// each to the Go package that declared it.
//
// Scoped to HooksPermissionKey for the same reason the verifier and version
// tripwires are: since #1383 the Writes declaration covers every managed user
// file, and these seven families are obligations of installing into an agent's
// OWN config format and receiving what it then sends.
func hookInstallingAdapters(t *testing.T) []hookInstall {
	t.Helper()

	all := All()
	if len(all) == 0 {
		t.Fatal("All() returned no adapters — this tripwire would report a clean sweep of an " +
			"empty registry, which is the exact shape of failure it exists to prevent")
	}

	var out []hookInstall
	for _, a := range all {
		for _, p := range a.Permissions {
			if p.Writes == nil || p.Key != agent.HooksPermissionKey {
				continue
			}
			out = append(out, hookInstall{
				Adapter: a.Identity.Name,
				Key:     p.Key,
				Pkg:     declaringPackage(t, a.Identity.Name, p),
			})
		}
	}
	if len(out) == 0 {
		t.Fatalf("no adapter in the registry declares a %q permission with a managed user file, "+
			"across %d adapter(s). Either every hook install was removed, or this projection has "+
			"stopped matching — and a green run of this test would mean the same thing in both "+
			"cases", agent.HooksPermissionKey, len(all))
	}
	return out
}

// declaringPackage recovers the Go package that declared a permission, from the
// closures on the permission itself.
//
// Derived rather than looked up in a hand-written adapter-name → directory map,
// because such a map is one more thing a new adapter is covered by only if
// someone remembers to edit it — which is the failure #1740 is about, one level
// up. (agent.Identity.Name does not answer it either: "claude-code" is package
// claudecode, "mistral-vibe" is package vibe.)
//
// Apply and Uninstall must AGREE. Both are inherently adapter-specific — the
// closure that writes this agent's config format and the one that removes it —
// so a disagreement means the declaration was composed out of shared helpers
// and there is no single package whose tests this walk should be reading. That
// is a loud stop, not a skip: silently walking whichever package won would
// grade the wrong tree and report a clean result about it.
func declaringPackage(t *testing.T, adapter string, p agent.Permission) string {
	t.Helper()

	applyPkg, okApply := funcPackagePath(p.Apply)
	if !okApply {
		t.Fatalf("%s/%s: cannot resolve the package of its Apply closure (Apply is nil or not a "+
			"Go func) — TestEveryHookInstallDeclaresAVerifier requires a hooks permission to have "+
			"one, so this is a broken declaration rather than a limit of this walk",
			adapter, p.Key)
	}
	uninstallPkg, okUninstall := funcPackagePath(p.Writes.Uninstall)
	if !okUninstall {
		t.Fatalf("%s/%s: cannot resolve the package of its Writes.Uninstall closure — "+
			"ManagedUserFiles requires a non-nil Uninstall, so this is a broken declaration",
			adapter, p.Key)
	}
	if applyPkg != uninstallPkg {
		t.Fatalf("%s/%s: Apply is declared in %q but Writes.Uninstall in %q. This walk derives the "+
			"adapter's package from its own closures and cannot choose between two; declare both "+
			"in the adapter package, or teach this test the mapping in a reviewable diff",
			adapter, p.Key, applyPkg, uninstallPkg)
	}
	if !strings.HasPrefix(applyPkg, adapterTreePrefix) || strings.Contains(applyPkg[len(adapterTreePrefix):], "/") {
		t.Fatalf("%s/%s: resolved declaring package %q, which is not a direct child of %q. A "+
			"declaration composed by a shared helper points this walk at a package holding no "+
			"adapter tests, and it would then report that package's (absent) wirings as this "+
			"adapter's", adapter, p.Key, applyPkg, adapterTreePrefix)
	}
	return applyPkg
}

// funcPackagePath returns the import path of the package that declared fn.
func funcPackagePath(fn any) (string, bool) {
	if fn == nil {
		return "", false
	}
	v := reflect.ValueOf(fn)
	if v.Kind() != reflect.Func || v.IsNil() {
		return "", false
	}
	f := runtime.FuncForPC(v.Pointer())
	if f == nil {
		return "", false
	}
	return packagePathOfSymbol(f.Name())
}

// packagePathOfSymbol splits a runtime symbol name — "a/b/c.Fn",
// "a/b/c.Agent.func1", "a/b/c.(*T).M" — into its package path.
//
// The package path is everything up to the first '.' AFTER the last '/'. Doing
// it the other way round (first '.' in the whole string) is wrong for any
// hosted path: "github.com/x/y.Fn" would yield "github".
func packagePathOfSymbol(sym string) (string, bool) {
	slash := strings.LastIndex(sym, "/")
	dot := strings.Index(sym[slash+1:], ".")
	if dot < 0 {
		return "", false
	}
	return sym[:slash+1+dot], true
}

// sourceUnit is one type-checked set of files the detector walks. An adapter
// contributes two — its in-package test variant and its external test package —
// and they are handed over together so a call that crosses between them is an
// ordinary edge rather than a blind spot.
type sourceUnit struct {
	fset *token.FileSet
	file *ast.File
	info *types.Info
}

// wiringScan is the detector's verdict about one adapter's test sources.
//
// Called and Unreached are kept apart deliberately. "You never wrote it" and
// "you wrote it in a helper nothing calls" need different advice, and collapsing
// them would make the second read as the first — which is how a dead wiring gets
// re-added instead of re-attached.
type wiringScan struct {
	Called    map[string]string // entry point → position of a reachable call
	Unreached map[string]string // entry point → position of an unreachable call
	Roots     int               // go-test entry functions found
	TestFiles int               // _test.go files walked
}

// scanContractWirings reports which of `want`'s entry points in package
// contractPkg are called, from the given files, along a chain rooted at a
// function `go test` runs.
//
// A plain function over units rather than a method on anything, so
// hookcontracts_shapes_test.go can feed it a synthetic corpus and pin, case by
// case, which spellings it credits — the same shape architecture_hookbody_test.go's
// requestBodyReadsIn takes, and for the same reason.
func scanContractWirings(units []sourceUnit, contractPkg string, want []string) wiringScan {
	wanted := make(map[string]bool, len(want))
	for _, w := range want {
		wanted[w] = true
	}

	scan := wiringScan{Called: map[string]string{}, Unreached: map[string]string{}}

	type callSite struct {
		fn  string
		pos string
	}
	owner := map[*types.Func][]callSite{} // enclosing func → contract calls inside it
	edges := map[*types.Func][]*types.Func{}
	declared := map[*types.Func]bool{}
	var roots []*types.Func

	seenFiles := map[string]bool{}
	// Pass 1: index every top-level function so an edge can be recognized as
	// package-local. Two passes rather than one because a helper is routinely
	// declared below its caller, and a single pass would drop that edge.
	for _, u := range units {
		name := u.fset.Position(u.file.Pos()).Filename
		if strings.HasSuffix(name, "_test.go") && !seenFiles[name] {
			seenFiles[name] = true
			scan.TestFiles++
		}
		for _, decl := range u.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil {
				continue
			}
			obj, _ := u.info.Defs[fd.Name].(*types.Func)
			if obj == nil {
				continue
			}
			declared[obj] = true
			if isGoTestEntry(fd, u.info, name) {
				roots = append(roots, obj)
			}
		}
	}

	// Pass 2: walk each function body once, recording both kinds of call.
	for _, u := range units {
		for _, decl := range u.file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Name == nil || fd.Body == nil {
				continue
			}
			self, _ := u.info.Defs[fd.Name].(*types.Func)
			if self == nil {
				continue
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				callee := calleeFunc(call.Fun, u.info)
				if callee == nil {
					return true
				}
				if callee.Pkg() != nil && callee.Pkg().Path() == contractPkg && wanted[callee.Name()] {
					owner[self] = append(owner[self], callSite{
						fn:  callee.Name(),
						pos: u.fset.Position(call.Lparen).String(),
					})
				}
				if declared[callee] {
					edges[self] = append(edges[self], callee)
				}
				return true
			})
		}
	}
	scan.Roots = len(roots)

	reachable := map[*types.Func]bool{}
	queue := slices.Clone(roots)
	for _, r := range queue {
		reachable[r] = true
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range edges[cur] {
			if reachable[next] {
				continue
			}
			reachable[next] = true
			queue = append(queue, next)
		}
	}

	for fn, sites := range owner {
		bucket := scan.Unreached
		if reachable[fn] {
			bucket = scan.Called
		}
		for _, s := range sites {
			// Lowest position wins, so a failure names the same call every run
			// regardless of map iteration order.
			if prev, ok := bucket[s.fn]; !ok || s.pos < prev {
				bucket[s.fn] = s.pos
			}
		}
	}
	// A name found reachable somewhere is wired, whatever else is dead.
	for fn := range scan.Called {
		delete(scan.Unreached, fn)
	}
	return scan
}

// calleeFunc resolves a call expression's callee to the function it names, or
// nil for anything that is not a direct call of a declared function (a call
// through a variable, an interface method, a conversion, a builtin).
//
// It resolves through the TYPE information rather than the written name, which
// is what makes an aliased or dot import count and a local helper that merely
// shares a name not count.
func calleeFunc(fun ast.Expr, info *types.Info) *types.Func {
	var ident *ast.Ident
	switch f := ast.Unparen(fun).(type) {
	case *ast.Ident:
		ident = f
	case *ast.SelectorExpr:
		ident = f.Sel
	default:
		return nil
	}
	fn, _ := info.Uses[ident].(*types.Func)
	return fn
}

// isGoTestEntry reports whether fd is a function `go test` will run.
//
// It reproduces cmd/go's own rule — the prefix followed by a non-lowercase rune
// — plus a check on the parameter TYPE, because a helper called
// TestHelper(t *testing.T) IS run by go test (and is therefore correctly a
// root), while `Testify(x int)` is not run and must not be treated as one.
func isGoTestEntry(fd *ast.FuncDecl, info *types.Info, filename string) bool {
	if !strings.HasSuffix(filename, "_test.go") {
		return false
	}
	if fd.Recv != nil || fd.Name == nil || fd.Body == nil {
		return false
	}
	name := fd.Name.Name
	var param string
	switch {
	case name == "TestMain":
		param = "M"
	case isTestPrefixed(name, "Test"):
		param = "T"
	case isTestPrefixed(name, "Benchmark"):
		param = "B"
	case isTestPrefixed(name, "Fuzz"):
		param = "F"
	default:
		return false
	}
	params := fd.Type.Params
	if params == nil || len(params.List) != 1 || len(params.List[0].Names) != 1 {
		return false
	}
	return isTestingPointer(info.TypeOf(params.List[0].Type), param)
}

// isTestPrefixed is cmd/go/internal/load.isTest: the prefix, then either
// nothing or a rune that is not lowercase.
func isTestPrefixed(name, prefix string) bool {
	if !strings.HasPrefix(name, prefix) {
		return false
	}
	if len(name) == len(prefix) {
		return true
	}
	r, _ := utf8.DecodeRuneInString(name[len(prefix):])
	return !unicode.IsLower(r)
}

// isTestingPointer reports whether t is *testing.<name>.
func isTestingPointer(t types.Type, name string) bool {
	if t == nil {
		return false
	}
	ptr, ok := types.Unalias(t).(*types.Pointer)
	if !ok {
		return false
	}
	named, ok := types.Unalias(ptr.Elem()).(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil && obj.Pkg().Path() == "testing" && obj.Name() == name
}

// loadForWiringScan loads the named packages WITH their tests, and refuses to
// return a set it cannot vouch for.
func loadForWiringScan(t *testing.T, patterns []string) []*packages.Package {
	t.Helper()

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo,
		Tests: true,
		Dir:   ".",
	}
	pkgs, err := packages.Load(cfg, patterns...)
	if err != nil {
		t.Fatalf("packages.Load(%v): %v", patterns, err)
	}
	if n := packages.PrintErrors(pkgs); n > 0 {
		t.Fatalf("packages.Load reported %d package error(s); the build is broken, and every "+
			"verdict below would be about source that does not compile", n)
	}
	for _, pkg := range pkgs {
		if pkg.TypesInfo == nil {
			t.Fatalf("package %q loaded without type information; the detector cannot tell a "+
				"call of contracttesting.AssertX from a local helper of the same name without it",
				pkg.ID)
		}
	}
	return pkgs
}

// assertRequiredWiringsExist is the existence check on requiredWirings()' keys.
//
// Without it a renamed or deleted entry point turns this tripwire into a demand
// that every adapter call something that does not exist — six adapters × one
// row, all red, none of them the adapter's fault, and the actual cause
// (contracttesting moved) named nowhere. Exemption and requirement tables alike
// get their keys checked here for the same reason construction_test.go checks
// nilTolerant's: an entry that has stopped naming a real thing must fail rather
// than silently mean nothing.
func assertRequiredWiringsExist(t *testing.T, pkgs []*packages.Package, required []requiredWiring) {
	t.Helper()

	scope := contracttestingScope(t, pkgs)
	if len(required) == 0 {
		t.Fatal("requiredWirings() is empty; this tripwire would pass having demanded nothing")
	}
	for _, r := range required {
		obj := scope.Lookup(r.Fn)
		if obj == nil {
			t.Fatalf("requiredWirings() demands contracttesting.%s (%s), which %s does not "+
				"declare. Either the entry point was renamed — update this row and every "+
				"adapter's wiring — or it was removed, in which case delete the row and say why",
				r.Fn, r.Family, contracttestingPkg)
		}
		fn, ok := obj.(*types.Func)
		if !ok || !obj.Exported() {
			t.Fatalf("contracttesting.%s is a %T, not an exported func; requiredWirings() can "+
				"only demand something an adapter can call", r.Fn, obj)
		}
		_ = fn
	}
}

// testVariantUnitsByPackage groups the loaded packages' test variants by the
// import path of the package under test.
//
// Only the variants matter. `packages.Load(Tests: true)` returns four things
// per package: the plain build (no _test.go at all), the in-package test
// variant `p [p.test]` (which re-type-checks the ordinary files TOGETHER with
// the in-package _test.go files), the external test package `p_test [p.test]`,
// and the generated `p.test` main. The two variants are the whole truth about
// what runs, and they are grouped together so a call from the external test
// package into an in-package helper is an ordinary edge.
//
// Note the plain build is skipped rather than merged: its objects are DIFFERENT
// *types.Func values from the test variant's, so including it would add a
// second, permanently unreachable copy of every helper and nothing else.
//
// The package under test is recovered from the ID's BRACKETED part, never by
// trimming a "_test" suffix off PkgPath. That distinction is load-bearing and
// the corpus caught it: a package whose own import path legitimately ends in
// "_test" — the `skipped_test` corpus row is exactly one, and an adapter
// directory could be — has its in-package variant filed under a path that does
// not exist, so its files vanish and every family is reported missing for it.
// The ID says which package a variant belongs to; PkgPath's suffix only looks
// like it does.
func testVariantUnitsByPackage(t *testing.T, pkgs []*packages.Package) map[string][]sourceUnit {
	t.Helper()

	out := map[string][]sourceUnit{}
	for _, pkg := range pkgs {
		open := strings.Index(pkg.ID, " [")
		if open < 0 || !strings.HasSuffix(pkg.ID, "]") {
			continue // the plain build, or the generated p.test main
		}
		under, ok := strings.CutSuffix(pkg.ID[open+2:len(pkg.ID)-1], ".test")
		if !ok {
			t.Fatalf("package ID %q has a bracketed variant tag this walk cannot parse; it would "+
				"otherwise file the package's test files under a path nothing looks up, and every "+
				"family would be reported missing for it", pkg.ID)
		}
		for _, f := range pkg.Syntax {
			out[under] = append(out[under], sourceUnit{fset: pkg.Fset, file: f, info: pkg.TypesInfo})
		}
	}
	return out
}

// describeScan renders a scan for a failure message, in a stable order so two
// runs of a failing corpus row print the same line.
func describeScan(s wiringScan) string {
	return fmt.Sprintf("files=%d roots=%d called=%v unreached=%v",
		s.TestFiles, s.Roots, slices.Sorted(maps.Keys(s.Called)), slices.Sorted(maps.Keys(s.Unreached)))
}

// contracttestingScope returns the contract package's top-level scope, and
// refuses if the load did not produce it — without it every name check below
// silently becomes a no-op, and this tripwire would be asserting against
// strings nothing verifies.
func contracttestingScope(t *testing.T, pkgs []*packages.Package) *types.Scope {
	t.Helper()
	for _, pkg := range pkgs {
		if pkg.PkgPath == contracttestingPkg && pkg.ID == contracttestingPkg && pkg.Types != nil {
			return pkg.Types.Scope()
		}
	}
	t.Fatalf("package %q was not loaded — every entry-point name check would be unverifiable and "+
		"this tripwire would be asserting against names nothing checks", contracttestingPkg)
	return nil
}
