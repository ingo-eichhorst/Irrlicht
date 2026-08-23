// Package righome joins two facts that live in two trees and had nothing
// holding them together: which adapters the recording rig can isolate onto a
// scratch agent home (declared in
// tools/onboarding-factory/scripts/lib/agent-home.sh) and which adapters
// install hooks into the user's config (declared in the daemon's adapter
// registry, agent.Agent.Permissions).
//
// The failure it exists to catch is silent and was measured rather than
// imagined. The rig EXPORTS the variable the table names, before spawning the
// daemon; the daemon is a direct child and inherits it, while the agent CLI is
// launched by the driver through `tmux new-session` and therefore inherits the
// tmux SERVER's environment. So a table row naming a variable the daemon does
// not actually read — a rename upstream, a typo, an adapter whose override was
// never wired — leaves the daemon watching the operator's real home and the CLI
// writing to the scratch one, or the reverse. Either way the recording comes
// back with no session in it and nothing anywhere says why. Reconcile is the
// static half of that (is there a row at all); the test beside it is the
// behavioural half (does setting the row's variable actually move the adapter's
// session root AND every file its hooks install writes).
//
// It is deliberately NOT a claim that isolation is required. The rig protects
// the user's declared agent configs unconditionally, through
// lib/managed-file-snapshot.sh's snapshot/restore of `--print-managed-files`,
// and that is a hard gate on the spawn. What this package enforces is that an
// adapter's isolation story is a DECISION — a row, or a named reason it cannot
// have one — rather than an omission nobody noticed.
package righome

import (
	"bufio"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"irrlicht/core/pkg/pathutil"
)

// TableScript is the shell library that carries the declaration, relative to
// the repository root.
const TableScript = "tools/onboarding-factory/scripts/lib/agent-home.sh"

// Policy is how the rig treats an adapter's home variable when the operator
// leaves it unset. See agent-home.sh for the per-adapter reasoning, which is
// where it belongs — this type only needs to know the two values exist.
type Policy string

const (
	// PolicyDefault — the rig points the variable at a staging directory when
	// the operator leaves it unset.
	PolicyDefault Policy = "default"
	// PolicyOptIn — the rig does nothing unless the operator exports an
	// absolute path, because a scratch home would start without state the run
	// needs (a credential, a provider declaration, a persisted consent).
	PolicyOptIn Policy = "optin"
)

// Row is one adapter's declaration.
type Row struct {
	Adapter string
	EnvVar  string
	Policy  Policy
}

// Unisolatable names every hooks-declaring adapter that has NO row, with the
// structural reason. Each entry is a property of the adapter's own path
// resolution rather than work nobody has got to yet, which is the difference
// between an exemption list and a to-do list — if one of these ever became
// "not done yet", it should move out of here and into agent-home.sh.
//
// opencode's entry (#1719) is the one that carries a named remainder rather
// than being purely structural, and it says so out loud instead of reading as
// settled: the SPLIT across two XDG variables is opencode's, and no
// one-variable row can express it — but the reason the second variable would
// not help either is an irrlicht gap (StorePath ignores $XDG_DATA_HOME), and
// that half is fixable.
//
// The keys are existence-checked in both directions by
// TestRigHomeTableMatchesTheAdapterRegistry: an entry naming an adapter that
// does not declare hooks (or does not exist) fails, exactly as a hooks adapter
// in neither place does.
var Unisolatable = map[string]string{
	"antigravity": "no override exists, and the credential is not in $HOME-relative state at " +
		"all. The customization root the hooks install writes (~/.gemini/config/hooks.json, " +
		"antigravity/hookinstaller.go configDirName) and both brain stores " +
		"(antigravity/adapter.go cliBrainDir/ideBrainDir) are $HOME-relative with no " +
		"os.Getenv anywhere in core/adapters/inbound/agents/antigravity. JETSKI_APP_DATA_DIR " +
		"was measured against a scratch directory with hooks.json at <dir>/, <dir>/config/ " +
		"and <dir>/.gemini/config/ and agy still booted reporting `loaded 0 named hooks from " +
		"0 hooks.json file(s)`, so it does not relocate the customization root. Even a row " +
		"that DID move both trees would not produce a recordable session: agy's credential " +
		"is a macOS login-keychain generic password (svce=\"gemini\", acct=\"antigravity\"), " +
		"keychain resolution is derived from $HOME, and under any scratch HOME `security " +
		"default-keychain` reports no default keychain and agy answers \"Please sign in to " +
		"view available models\" — confirmed as a 2x2 against the token-free `agy models`, " +
		"with a byte-complete clone of ~/.gemini not helping. So its hook recordings can " +
		"only run against the real home, with the managed-file snapshot as the whole of the " +
		"protection, exactly as for gemini-cli.",
	"claudecode": "HALF-relocatable, which is worse than not at all: transcripts follow " +
		"CLAUDE_CONFIG_DIR (claudecode/adapter.go transcriptsDir) but the hook config does " +
		"not — claudeSettingsPath (claudecode/hookinstaller.go) joins os.UserHomeDir() with " +
		".claude/settings.json unconditionally. A row would move the session store while the " +
		"install kept landing in the operator's real settings.json, i.e. claim an isolation " +
		"it does not have.",
	"opencode": "split across TWO XDG variables, which a one-variable row cannot express, and irrlicht honours neither. opencode resolves its config dir (the plugin irrlicht installs) from $XDG_CONFIG_HOME and its DATA dir (the SQLite store the daemon watches) from $XDG_DATA_HOME (Global.Path: join(XDG_CONFIG_HOME || ~/.config, \"opencode\") and join(XDG_DATA_HOME || ~/.local/share, \"opencode\")). A row naming XDG_CONFIG_HOME would relocate the install while the store stayed real — claudecode's half-relocatable shape in mirror image — and one naming XDG_DATA_HOME would be worse still, because opencode's StorePath() joins $HOME with \".local/share/opencode/opencode.db\" unconditionally: the CLI would move and the daemon would keep watching the operator's real database. Closing this means teaching StorePath() about XDG_DATA_HOME AND giving the table a two-variable row shape; until both, the managed-file snapshot is the whole of the protection, exactly as it is for gemini-cli.",
	"gemini-cli": "no override exists at all: defaultRootDir is \".gemini/tmp\" under $HOME and " +
		"geminiHome() is $HOME/.gemini, with no os.Getenv anywhere in " +
		"core/adapters/inbound/agents/geminicli. Its hook recordings can only run against the " +
		"real home, with the managed-file snapshot as the whole of the protection.",
}

// Table runs agent-home.sh's own declaration rather than parsing the file, so
// the rig and this package cannot read the table differently. It refuses — it
// never returns a short list — on anything it cannot read with confidence: a
// shell that will not source, a malformed row, or an EMPTY table. That last one
// is the important refusal: every consistency check downstream is satisfied by
// a table with no rows in it, so "nothing declared" and "nothing wrong" would
// otherwise be the same answer.
func Table(repoRoot string) ([]Row, error) {
	script := filepath.Join(repoRoot, TableScript)
	// bash is resolved through pathutil rather than run by bare name, which is
	// this repo's own answer to SonarQube go:S4036 (that package's doc comment
	// names the rule): a local attacker who can write to a directory earlier on
	// the inherited PATH would otherwise choose the interpreter that reads the
	// declaration this whole package exists to trust. MustResolve falls back to
	// the bare name when no trusted directory has it, so a machine with an
	// unusual layout degrades to today's behaviour rather than failing to run
	// the check at all.
	// #nosec G204 -- script path is built from a caller-supplied repo root and
	// a package constant, never from external input.
	cmd := exec.Command(pathutil.MustResolve("bash"), "-c", `set -euo pipefail; source "$1"; agent_home_table`, "_", script)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("running agent_home_table from %s: %w: %s", script, err, strings.TrimSpace(string(out)))
	}

	var rows []Row
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("%s: malformed row %q: want '<adapter> <ENV_VAR> <policy>'", TableScript, line)
		}
		policy := Policy(fields[2])
		if policy != PolicyDefault && policy != PolicyOptIn {
			return nil, fmt.Errorf("%s: row %q declares unknown policy %q", TableScript, line, fields[2])
		}
		rows = append(rows, Row{Adapter: fields[0], EnvVar: fields[1], Policy: policy})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading agent_home_table output: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s: agent_home_table declared no adapters — every consistency "+
			"check over it is vacuously satisfied by an empty table", TableScript)
	}
	return rows, nil
}

// Reconcile reports every disagreement between the rig's table, the exemption
// map, and the registry's view of which adapters declare hooks. An empty result
// means they agree.
//
// declaresHooks is the registry projection (adapter slug → declares a hooks
// permission), with an entry for EVERY adapter — a false is "asked and
// answered", not "absent" — which is what lets an unknown adapter name in
// either declaration be told apart from one that simply installs no hooks.
//
// Split out as a pure function on purpose: the live wiring cannot be mutated to
// prove these checks discriminate (mutating it means editing the shell table or
// an adapter, which is a different test), so the committed mutation evidence is
// a corpus driving THIS function with deliberately wrong inputs.
func Reconcile(rows []Row, exempt map[string]string, declaresHooks map[string]bool) []string {
	if len(declaresHooks) == 0 {
		return []string{"the adapter registry projection is empty — nothing below can fail, " +
			"so this is a broken measurement rather than a clean result"}
	}

	// Three independent questions, one per helper — the table's own rows, the
	// exemption map's own entries, and the adapters neither of them covers.
	// Kept apart rather than folded into one walk because each names a
	// different fragment in its message, and the corpus beside this file
	// asserts that a case wrong in ONE way fires exactly one of them.
	rowFor, problems := checkRows(rows, exempt, declaresHooks)
	problems = append(problems, checkExemptions(exempt, declaresHooks)...)
	problems = append(problems, checkUncovered(rowFor, exempt, declaresHooks)...)

	sort.Strings(problems)
	return problems
}

// checkRows validates the rig table's own rows and returns the row index the
// uncovered-adapter check needs, so the two cannot disagree about which
// adapters actually have a usable row (a duplicate contributes only its first).
func checkRows(rows []Row, exempt map[string]string, declaresHooks map[string]bool) (map[string]Row, []string) {
	rowFor := make(map[string]Row, len(rows))
	var problems []string
	for _, r := range rows {
		if _, known := declaresHooks[r.Adapter]; !known {
			problems = append(problems, fmt.Sprintf(
				"%s names adapter %q, which is not in the daemon's adapter registry — the rig "+
					"would export %s for an adapter that does not exist",
				TableScript, r.Adapter, r.EnvVar))
			continue
		}
		if prev, dup := rowFor[r.Adapter]; dup {
			problems = append(problems, fmt.Sprintf(
				"%s declares adapter %q twice (%s and %s) — which row wins is the order of a "+
					"heredoc", TableScript, r.Adapter, prev.EnvVar, r.EnvVar))
			continue
		}
		rowFor[r.Adapter] = r
		if reason, both := exempt[r.Adapter]; both {
			problems = append(problems, fmt.Sprintf(
				"adapter %q has BOTH a row in %s (%s) and an Unisolatable entry (%q) — one of "+
					"the two is stale and a reader cannot tell which",
				r.Adapter, TableScript, r.EnvVar, truncate(reason)))
		}
	}
	return rowFor, problems
}

// checkExemptions validates the exemption map's own keys in both directions: an
// entry for an adapter the registry does not have, and one for an adapter that
// installs no hooks. Both read as coverage of an obligation that was never
// there.
func checkExemptions(exempt map[string]string, declaresHooks map[string]bool) []string {
	var problems []string
	for adapter := range exempt {
		declares, known := declaresHooks[adapter]
		switch {
		case !known:
			problems = append(problems, fmt.Sprintf(
				"Unisolatable names adapter %q, which is not in the daemon's adapter registry — "+
					"an exemption for something that does not exist reads as coverage", adapter))
		case !declares:
			problems = append(problems, fmt.Sprintf(
				"Unisolatable names adapter %q, which declares no hooks permission — it is "+
					"exempt from an obligation it never had", adapter))
		}
	}
	return problems
}

// checkUncovered is the obligation itself: a hooks-declaring adapter in neither
// declaration.
func checkUncovered(rowFor map[string]Row, exempt map[string]string, declaresHooks map[string]bool) []string {
	var problems []string
	for _, adapter := range sortedKeys(declaresHooks) {
		if !declaresHooks[adapter] {
			continue
		}
		_, hasRow := rowFor[adapter]
		_, isExempt := exempt[adapter]
		if hasRow || isExempt {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"adapter %q installs hooks into the user's config but has neither a row in %s "+
				"nor an Unisolatable entry saying why it cannot have one. Add whichever is "+
				"true — an isolation story is a decision, and an omission looks exactly like "+
				"a decision that was made silently", adapter, TableScript))
	}
	return problems
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func truncate(s string) string {
	const max = 60
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// SourceVariantCoverage reports every disagreement between agent.Source's
// declared variants (derived from every non-test file in core/domain/agent,
// not just source.go — see sourceVariantsIn) and the cases a switch over that
// type actually names. An empty result means every declared variant has a
// named case, and every named case still names a variant that exists.
//
// This is #1767's fix. Before it, sessionRootsOf's type switch understood
// only the variants whichever adapter's rig-home row had exercised so far —
// FilesUnderRoot from day one, ProcessOwnedStore added in #1722/#1765 for
// hermes — and an unhandled variant surfaced only as that switch's `default`
// branch fataling inside a DIFFERENT adapter's relocation test, at the moment
// some later adapter's row happened to use it. That is a runtime accident,
// not a check: the failure lands on whoever adds the next adapter, unrelated
// to what they were doing, telling them to extend a mechanism they did not
// know existed. A guard over a closed, enumerable sum should instead fail the
// moment the sum itself gains a variant nothing names — which is what calling
// this with the two AST-derived projections below achieves.
//
// Split out as a pure function for the same reason Reconcile is: the real
// switch cannot be mutated to prove this discriminates (mutating it means
// adding a fourth variant to core/domain/agent/source.go, a different change
// with a different blast radius), so the committed mutation evidence
// (righome_corpus_test.go) drives THIS function directly with deliberately
// wrong sets instead.
func SourceVariantCoverage(declared, handled []string) []string {
	handledSet := make(map[string]bool, len(handled))
	for _, h := range handled {
		handledSet[h] = true
	}
	declaredSet := make(map[string]bool, len(declared))
	for _, d := range declared {
		declaredSet[d] = true
	}

	var problems []string
	for _, d := range declared {
		if !handledSet[d] {
			problems = append(problems, fmt.Sprintf(
				"agent.Source variant %q has no case in sessionRootsOf's type switch — a rig-home "+
					"row for an adapter using it would fall to the default branch and fatal inside "+
					"whatever unrelated adapter's relocation test first exercised it, instead of "+
					"failing here, now, by name", d))
		}
	}
	for _, h := range handled {
		if !declaredSet[h] {
			problems = append(problems, fmt.Sprintf(
				"sessionRootsOf names a case for %q, which core/domain/agent/source.go no longer "+
					"declares — a stale case reads as coverage of a variant that does not exist", h))
		}
	}
	sort.Strings(problems)
	return problems
}

// AgentDomainDir is the package that declares agent.Source and its variants,
// relative to the repo root.
const AgentDomainDir = "core/domain/agent"

// sourceVariantsIn returns the sorted names of every type anywhere in
// core/domain/agent that declares the isSource() sealing method — the
// authoritative, derived-from-source list of agent.Source's variants, rather
// than a hand-kept one that can drift the moment a variant is added.
//
// It scans every non-test .go file in the PACKAGE, not just source.go by
// name: the three variants all happen to live in one file today, but this
// repo's own convention for a domain type whose variants multiply per-OS or
// per-concern is to split them across sibling files in the same package (see
// core/pkg/ports.ProcessObserver's process_{darwin,linux,other}.go). A scan
// scoped to one filename would silently stop covering a variant the moment
// someone followed that convention here, which is exactly the "existence
// check that quietly checks less than it claims to" this issue exists to
// avoid — so it reads the whole directory and delegates to sourceVariantsFrom,
// the same split ScanBeaconPackage/BeaconAdapters already use: a thin
// disk-reading wrapper around a pure, in-memory function a corpus can drive
// directly with synthetic packages.
func sourceVariantsIn(repoRoot string) ([]string, error) {
	dir := filepath.Join(repoRoot, AgentDomainDir)
	files, err := goSourcesIn(dir)
	if err != nil {
		return nil, err
	}
	names, err := sourceVariantsFrom(files)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", AgentDomainDir, err)
	}
	return names, nil
}

// sourceVariantsFrom is sourceVariantsIn's pure half: every type across these
// sources (a filename → source map, in the shape ScanBeaconPackage already
// takes) that declares isSource(). Non-test files only — a type declared only
// in a _test.go file is not a real agent.Source variant.
func sourceVariantsFrom(files map[string]string) ([]string, error) {
	fset := token.NewFileSet()
	var names []string
	for name, src := range productionSources(files) {
		f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parsing %s: %w", name, err)
		}
		for _, decl := range f.Decls {
			if variant, ok := isSourceReceiverName(decl); ok {
				names = append(names, variant)
			}
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("declares no isSource() method at all — the scan is broken, not " +
			"the domain type")
	}
	sort.Strings(names)
	return names, nil
}

// isSourceReceiverName reports the receiver type name of one top-level
// declaration, if it is a method named isSource — the sealing marker every
// agent.Source variant declares (core/domain/agent/source.go's own doc
// comment on the interface names it as such).
func isSourceReceiverName(decl ast.Decl) (string, bool) {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok || fn.Recv == nil || fn.Name.Name != "isSource" || len(fn.Recv.List) != 1 {
		return "", false
	}
	return bareTypeName(fn.Recv.List[0].Type)
}

// bareTypeName strips a pointer off a receiver's type expression and returns
// the underlying identifier. Every isSource() today is a value receiver, but
// a future variant switching to a pointer should not silently vanish from the
// scan.
func bareTypeName(expr ast.Expr) (string, bool) {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name, true
	case *ast.StarExpr:
		return bareTypeName(t.X)
	default:
		return "", false
	}
}

// switchCasesIn returns the sorted agent.<Name> case types a type switch
// inside the named function resolves, read from source rather than by
// importing the package that declares the function — sessionRootsOf lives in
// a _test.go file this production package cannot import without a cycle.
//
// A thin disk-reading wrapper around switchCasesFrom, the pure half a corpus
// drives directly with synthetic source — same split as sourceVariantsIn.
func switchCasesIn(repoRoot, relPath, funcName string) ([]string, error) {
	path := filepath.Join(repoRoot, relPath)
	// #nosec G304 -- path built from a caller-supplied repo root and a package constant.
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", relPath, err)
	}
	names, err := switchCasesFrom(src, relPath, funcName)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", relPath, err)
	}
	return names, nil
}

// switchCasesFrom parses one Go source file and returns the sorted case
// types a type switch inside funcName resolves. filename is used only for
// parser diagnostics.
func switchCasesFrom(src []byte, filename, funcName string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, fmt.Errorf("parsing: %w", err)
	}

	fn := topLevelFunc(f, funcName)
	if fn == nil {
		return nil, fmt.Errorf("declares no func %s — the case scan has nothing to read", funcName)
	}
	sw := firstTypeSwitch(fn)
	if sw == nil {
		return nil, fmt.Errorf("%s declares no type switch — the case scan has nothing to read", funcName)
	}

	var names []string
	for _, stmt := range sw.Body.List {
		cc, ok := stmt.(*ast.CaseClause)
		if !ok {
			continue
		}
		for _, expr := range cc.List { // a nil List is `default:` and contributes nothing
			if name, ok := selectorName(expr); ok {
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("%s's type switch names no case at all — the scan is broken, not "+
			"the switch", funcName)
	}
	sort.Strings(names)
	return names, nil
}

// topLevelFunc finds a package-level (non-method) function by name.
func topLevelFunc(f *ast.File, name string) *ast.FuncDecl {
	for _, decl := range f.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// firstTypeSwitch returns the first type switch statement in fn's body. A
// function with more than one is not a shape this package's callers produce.
func firstTypeSwitch(fn *ast.FuncDecl) *ast.TypeSwitchStmt {
	var found *ast.TypeSwitchStmt
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if found != nil {
			return false
		}
		if sw, ok := n.(*ast.TypeSwitchStmt); ok {
			found = sw
			return false
		}
		return true
	})
	return found
}

// selectorName reads the `Name` off a `pkg.Name` case expression.
func selectorName(expr ast.Expr) (string, bool) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	return sel.Sel.Name, true
}

// BeaconPackagePath is the package an adapter imports when its hooks are
// delivered by the beacon rather than by a URL baked into the installed entry.
const BeaconPackagePath = "irrlicht/core/pkg/hookbeacon"

// AgentsDir is where the adapter packages live, relative to the repo root.
const AgentsDir = "core/adapters/inbound/agents"

// DaemonAddrEnvVar is the variable a beacon resolves its target from first
// (core/pkg/daemonaddr's EnvBindAddr). It is the one thing the rig must put in
// the agent CLI's pane, and the reason is not the same as a *_HOME row's.
//
// A URL-delivered hook carries the daemon's address in the bytes the installer
// wrote, so a coexisting recording daemon is named by the config itself. A
// BEACON-delivered hook carries none: the installed entry names
// `irrlichd hook-post <adapter>` and the beacon resolves where to POST at FIRE
// time, in its own process — which is a child of the agent CLI. In coexist mode
// a pane carrying neither this variable nor IRRLICHT_HOME falls through to the
// default state tree's addr file and posts every hook to the PRODUCTION daemon.
//
// Nothing fails when that happens: the recording daemon simply records no
// hook_received event and the fixture reads as an adapter that cannot report
// state. Measured in #1735 — all three beacon adapters had zero hook-bearing
// recordings, and mistral-vibe's first two attempts came back
// driver_exit_reason=ok with a complete, hook-free capture.
const DaemonAddrEnvVar = "IRRLICHT_BIND_ADDR"

// ScanBeaconPackage reports whether one adapter package's NON-TEST sources
// reach the daemon through the beacon, and the adapter slug they declare.
//
// files maps a file name to its source. Only names not ending in _test.go are
// read: a package whose tests import hookbeacon while its production code does
// not is not a beacon adapter, and counting it would put an obligation on a
// driver that cannot satisfy it.
//
// The slug comes from the package's own `const AdapterName`, which every
// adapter already declares and which is already the on-disk catalog slug — so
// there is no second package-directory-to-slug map to drift. A package that
// uses the beacon and declares no AdapterName is an ERROR rather than a skip:
// it is the case where this function has the least idea what it is looking at,
// which is the last place to drop a check.
func ScanBeaconPackage(files map[string]string) (slug string, usesBeacon bool, err error) {
	fset := token.NewFileSet()
	production := productionSources(files)

	usesBeacon, err = importsTheBeacon(fset, production)
	if err != nil || !usesBeacon {
		return "", usesBeacon, err
	}
	if s, ok := declaredAdapterName(fset, production); ok {
		return s, true, nil
	}
	return "", true, fmt.Errorf("a package importing %s declares no `const AdapterName` — "+
		"its catalog slug cannot be derived and the rig cannot check its driver", BeaconPackagePath)
}

// productionSources drops the _test.go files, in one place rather than at each
// of the two walks below — a package whose TESTS import the beacon while its
// production code does not is not a beacon adapter, and two copies of that
// filter is one copy that can stop matching.
func productionSources(files map[string]string) map[string]string {
	out := make(map[string]string, len(files))
	for name, src := range files {
		if !strings.HasSuffix(name, "_test.go") {
			out[name] = src
		}
	}
	return out
}

// importsTheBeacon reports whether any of these sources imports the beacon
// package. It parses imports only, and an unparseable file is an ERROR rather
// than a miss: a file it could not read is where it has the least idea what it
// is looking at, which is the last place to drop a check.
//
// It walks EVERY source before answering, and does not stop at the first
// beacon import it finds. That is deliberate and it is not an optimisation
// being declined: map iteration order is unspecified, so an early return would
// make "a package that imports the beacon AND has a file this cannot parse"
// answer `(true, nil)` or `(false, err)` depending on which file came first —
// a validator that silently drops a check, nondeterministically. Pinned by the
// corpus row of the same name.
func importsTheBeacon(fset *token.FileSet, sources map[string]string) (bool, error) {
	found := false
	for name, src := range sources {
		f, err := parser.ParseFile(fset, name, src, parser.ImportsOnly|parser.SkipObjectResolution)
		if err != nil {
			return false, fmt.Errorf("parsing %s: %w", name, err)
		}
		if fileImports(f, BeaconPackagePath) {
			found = true
		}
	}
	return found, nil
}

// fileImports reports whether f imports path, under any alias.
func fileImports(f *ast.File, path string) bool {
	for _, imp := range f.Imports {
		if got, err := strconv.Unquote(imp.Path.Value); err == nil && got == path {
			return true
		}
	}
	return false
}

// declaredAdapterName returns the first `const AdapterName` these sources
// declare. Map iteration order is unspecified, which is harmless because a Go
// package cannot declare the constant twice and still compile.
func declaredAdapterName(fset *token.FileSet, sources map[string]string) (string, bool) {
	for name, src := range sources {
		if s, ok := adapterNameConst(fset, name, src); ok {
			return s, true
		}
	}
	return "", false
}

// adapterNameConst finds `const AdapterName = "<slug>"` at package scope.
func adapterNameConst(fset *token.FileSet, name, src string) (string, bool) {
	f, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		return "", false
	}
	for _, decl := range f.Decls {
		if s, ok := adapterNameInDecl(decl); ok {
			return s, true
		}
	}
	return "", false
}

// adapterNameInDecl searches one top-level declaration. Split out so each of
// the three functions in this chain walks exactly ONE level — file to decl,
// decl to spec, spec to name. The nesting is real (a const block holds specs
// and a spec holds names), so the only way to remove it is to let each level
// name what it is walking.
func adapterNameInDecl(decl ast.Decl) (string, bool) {
	gd, ok := decl.(*ast.GenDecl)
	if !ok || gd.Tok != token.CONST {
		return "", false
	}
	for _, spec := range gd.Specs {
		if s, ok := adapterNameInSpec(spec); ok {
			return s, true
		}
	}
	return "", false
}

// adapterNameInSpec pulls the AdapterName string out of one const spec, which
// may declare several names at once (`const ( Other = "x"; AdapterName = "y" )`
// arrives as separate specs, but `const a, AdapterName = "x", "y"` does not).
func adapterNameInSpec(spec ast.Spec) (string, bool) {
	vs, ok := spec.(*ast.ValueSpec)
	if !ok {
		return "", false
	}
	for i, ident := range vs.Names {
		if ident.Name != "AdapterName" || i >= len(vs.Values) {
			continue
		}
		lit, ok := vs.Values[i].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			continue
		}
		if s, err := strconv.Unquote(lit.Value); err == nil {
			return s, true
		}
	}
	return "", false
}

// BeaconAdapters returns the sorted slugs of every beacon-delivered adapter in
// the tree. It REFUSES on an empty result rather than returning a short list,
// for the reason Table does: every check downstream is vacuously satisfied by
// finding no beacon adapters, so "none use the beacon" and "the scan broke"
// would otherwise be the same answer.
func BeaconAdapters(repoRoot string) ([]string, error) {
	dir := filepath.Join(repoRoot, AgentsDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", AgentsDir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		files, ferr := goSourcesIn(filepath.Join(dir, e.Name()))
		if ferr != nil {
			return nil, ferr
		}
		slug, uses, serr := ScanBeaconPackage(files)
		if serr != nil {
			return nil, fmt.Errorf("%s/%s: %w", AgentsDir, e.Name(), serr)
		}
		if uses {
			out = append(out, slug)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no package under %s imports %s — every check over the result "+
			"is vacuously satisfied, so this is a broken scan rather than a clean one",
			AgentsDir, BeaconPackagePath)
	}
	sort.Strings(out)
	return out, nil
}

// goSourcesIn reads one directory's .go files into the map ScanBeaconPackage
// takes. Non-recursive: an adapter's own package is one directory.
func goSourcesIn(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", dir, err)
	}
	files := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(dir, e.Name())) // #nosec G304 -- path built from a caller-supplied repo root and a directory listing
		if rerr != nil {
			return nil, fmt.Errorf("reading %s: %w", filepath.Join(dir, e.Name()), rerr)
		}
		files[e.Name()] = string(b)
	}
	return files, nil
}
