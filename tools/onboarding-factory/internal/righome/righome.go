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
	"os/exec"
	"path/filepath"
	"sort"
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
// structural reason. Both entries are properties of the adapter's own path
// resolution rather than work nobody has got to yet, which is the difference
// between an exemption list and a to-do list — if one of these ever became
// "not done yet", it should move out of here and into agent-home.sh.
//
// The keys are existence-checked in both directions by
// TestRigHomeTableMatchesTheAdapterRegistry: an entry naming an adapter that
// does not declare hooks (or does not exist) fails, exactly as a hooks adapter
// in neither place does.
var Unisolatable = map[string]string{
	"claudecode": "HALF-relocatable, which is worse than not at all: transcripts follow " +
		"CLAUDE_CONFIG_DIR (claudecode/adapter.go transcriptsDir) but the hook config does " +
		"not — claudeSettingsPath (claudecode/hookinstaller.go) joins os.UserHomeDir() with " +
		".claude/settings.json unconditionally. A row would move the session store while the " +
		"install kept landing in the operator's real settings.json, i.e. claim an isolation " +
		"it does not have.",
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
