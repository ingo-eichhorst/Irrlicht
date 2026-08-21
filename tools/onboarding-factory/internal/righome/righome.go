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
	// #nosec G204 -- script path is built from a caller-supplied repo root and
	// a package constant, never from external input.
	cmd := exec.Command("bash", "-c", `set -euo pipefail; source "$1"; agent_home_table`, "_", script)
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
	var problems []string

	if len(declaresHooks) == 0 {
		return []string{"the adapter registry projection is empty — nothing below can fail, " +
			"so this is a broken measurement rather than a clean result"}
	}

	rowFor := make(map[string]Row, len(rows))
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

	for adapter, reason := range exempt {
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
		_ = reason
	}

	for _, adapter := range sortedKeys(declaresHooks) {
		if !declaresHooks[adapter] {
			continue
		}
		_, hasRow := rowFor[adapter]
		_, isExempt := exempt[adapter]
		if !hasRow && !isExempt {
			problems = append(problems, fmt.Sprintf(
				"adapter %q installs hooks into the user's config but has neither a row in %s "+
					"nor an Unisolatable entry saying why it cannot have one. Add whichever is "+
					"true — an isolation story is a decision, and an omission looks exactly like "+
					"a decision that was made silently", adapter, TableScript))
		}
	}

	sort.Strings(problems)
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
