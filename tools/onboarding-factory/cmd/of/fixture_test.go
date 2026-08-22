package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// richRepo writes a catalog exercising every display state and every
// hook-coverage status, so `of status --summary` and `of coverage --hooks`
// are both pinned against ONE fixture whose expected counts are stated here
// rather than recomputed by the assertions.
//
// The adapter slugs are real (claudecode, codex, aider, antigravity) because
// `of coverage --hooks` joins the catalog to the daemon's adapter registry:
// claudecode, codex and antigravity declare a "hooks" permission and aider
// does not. Inventing a slug would silently drop the row from that join.
//
// Cells per agent, and the display state each one derives to:
//
//	claudecode  1-1 observed (recorded)   2-1 pending-record   3-1 blocked-driver
//	            4-1 blocked-daemon        5-1 unobservable     6-1 n/a (supports=no)
//	            7-1 unknown (supports=unknown)                        → 7 cells
//	codex       1-1 observed (recorded)   2-1 pending-record          → 2 cells
//	aider       1-1 observed (recorded)                               → 1 cell
//	antigravity 1-1 observed (recorded)                               → 1 cell
//
// Recordings, and which carry a hook_received event:
//
//	claudecode 1-1: r1 (hook_received), r2 (none)  → 2 recordings, 1 with hooks
//	codex      1-1: r1 (none)                      → 1 recording,  0 with hooks
//	aider       1-1: r1 (none)                     → 1 recording,  0 with hooks
//	antigravity 1-1: r1 (none)                     → 1 recording,  0 with hooks
func richRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	write(t, filepath.Join(root, "replaydata", "agents", "scenarios.json"), `{
  "meta": {"min_versions": {"aider": "1.0.0", "claudecode": "2.0.0", "codex": "1.0.0", "antigravity": "1.0.0"}},
  "scenarios": [
    {"id": "1.1", "name": "one",   "description": "d", "process": "p", "acceptance_criteria": "a"},
    {"id": "2.1", "name": "two",   "description": "d", "process": "p", "acceptance_criteria": "a"},
    {"id": "3.1", "name": "three", "description": "d", "process": "p", "acceptance_criteria": "a"},
    {"id": "4.1", "name": "four",  "description": "d", "process": "p", "acceptance_criteria": "a"},
    {"id": "5.1", "name": "five",  "description": "d", "process": "p", "acceptance_criteria": "a"},
    {"id": "6.1", "name": "six",   "description": "d", "process": "p", "acceptance_criteria": "a"},
    {"id": "7.1", "name": "seven", "description": "d", "process": "p", "acceptance_criteria": "a"}
  ]
}`)

	// claudecode: one cell per display state.
	cell(t, root, "claudecode", "1-1_one", "yes", "full", "ready")
	cell(t, root, "claudecode", "2-1_two", "yes", "full", "ready")
	cell(t, root, "claudecode", "3-1_three", "yes", "full", "gap:driver-cannot-interrupt")
	cell(t, root, "claudecode", "4-1_four", "yes", "bug", "ready")
	cell(t, root, "claudecode", "5-1_five", "yes", "incapable", "ready")
	cell(t, root, "claudecode", "6-1_six", "no", "full", "ready")
	cell(t, root, "claudecode", "7-1_seven", "unknown", "full", "ready")
	recording(t, root, "claudecode", "1-1_one", "r1", true)
	recording(t, root, "claudecode", "1-1_one", "r2", false)

	cell(t, root, "codex", "1-1_one", "yes", "full", "ready")
	cell(t, root, "codex", "2-1_two", "yes", "full", "ready")
	recording(t, root, "codex", "1-1_one", "r1", false)

	// aider is the fixture's "declares no hooks" adapter — the counterweight
	// TestCoverageHooksDistinguishesGapFromNoHooks measures codex's GAP
	// against. The role has moved three times, always for the same reason:
	// opencode held it until #1719, hermes until #1722, antigravity until
	// #1723, and each move happened because the adapter playing it gained a
	// hooks permission.
	//
	// aider is where it stops, because aider is now the ONLY registry adapter
	// declaring no hooks. That has a cost this fixture pays deliberately: the
	// INCIDENTAL status (a hook-bearing recording under an adapter that
	// declares nothing) needs a SECOND non-declaring adapter, and there is no
	// longer one to spare — the two roles are mutually exclusive by
	// construction. So aider's recording is hook-free here and incidental is
	// pinned one layer down, where it does not need a real registry row:
	// hookcov's own TestStatusOf table and TestGapsListsOnlyGaps. Losing
	// the CLI-level incidental row is the lesser loss; gap-vs-none is the
	// distinction `of coverage --hooks` exists to draw.
	cell(t, root, "aider", "1-1_one", "yes", "full", "ready")
	recording(t, root, "aider", "1-1_one", "r1", false)

	cell(t, root, "antigravity", "1-1_one", "yes", "full", "ready")
	recording(t, root, "antigravity", "1-1_one", "r1", false)

	return root
}

// cell writes one (agent, scenario) cell with the three assessment axes that
// drive matrix.DeriveDisplayState.
func cell(t *testing.T, root, agent, folder, supports, daemon, driver string) {
	t.Helper()
	dir := filepath.Join(root, "replaydata", "agents", agent, "scenarios", folder)
	write(t, filepath.Join(dir, "metadata.json"), `{
  "scenario_id": "`+scenarioNameOf(folder)+`",
  "details": {"assessment": {"agent_supports": "`+supports+`", "daemon_capability": "`+daemon+`", "driver_capability": "`+driver+`"}}
}`)
	write(t, filepath.Join(dir, "expected.jsonl"), `{"schema_version":1}`+"\n")
}

// scenarioNameOf maps the on-disk folder "<dashed-id>_<name>" to the scenario
// name the shard loader keys cells by.
func scenarioNameOf(folder string) string {
	if _, name, ok := strings.Cut(folder, "_"); ok {
		return name
	}
	return folder
}

// recording writes one complete recording under a cell. withHook adds a
// hook_received event, which is what `of coverage --hooks` counts — attributed
// to session "s", which the transcript_new line below tags adapter:agent, so
// hookcov's session_id cross-reference (#1768) resolves it as agent's own
// rather than dropping it as unattributable.
func recording(t *testing.T, root, agent, folder, name string, withHook bool) {
	t.Helper()
	rec := filepath.Join(root, "replaydata", "agents", agent, "scenarios", folder, "recordings", name)
	events := `{"seq":1,"ts":"2026-05-01T00:00:00Z","kind":"transcript_new","session_id":"s","adapter":"` + agent + `"}` + "\n"
	if withHook {
		events += `{"seq":2,"ts":"2026-05-01T00:00:01Z","kind":"hook_received","session_id":"s","hook_name":"PostToolUse"}` + "\n"
	}
	write(t, filepath.Join(rec, "events.jsonl"), events)
	write(t, filepath.Join(rec, "manifest.json"), `{}`+"\n")
	write(t, filepath.Join(rec, "transcript.jsonl"), `{}`+"\n")
	write(t, filepath.Join(rec, "transcript.jsonl.replay.json.golden"), `{}`+"\n")
}
