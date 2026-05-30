// Command of is the onboarding-factory CLI — the single entry point the
// onboarding skill drives for everything under replaydata/. It NEVER lets the
// skill touch replaydata files directly: read-side commands derive their answer
// from the canonical matrix model (internal/matrix) + the catalog shards
// (internal/shard); write-side commands (added in a later phase) are the sole
// writers and validate before they touch disk.
//
// Read-side commands (this phase):
//
//	of status   [--agent a] [--scenario s] [--runs] [--json]   coverage / run status
//	of validate [--json]                                        schema + referential integrity
//	of coverage [--json]                                        derived rollup (in-memory)
//
// Exit codes (matching the sibling cmd tools):
//
//	0  — success / validation clean
//	1  — validation failed (schema / referential violations)
//	2  — usage or configuration error
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"irrlicht/tools/onboarding-factory/internal/matrix"
	"irrlicht/tools/onboarding-factory/internal/shard"
)

const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

const usage = `usage:
  of status   [--agent a] [--scenario s] [--runs] [--json] [--repo-root .]
  of validate [--json] [--repo-root .]
  of coverage [--json] [--repo-root .]
  of scenario add|update --name n [--id i] [--description d] [--process-file f] [--acceptance-file f]
  of agent add --id i --name n --provider p [--min-version v] [--prereq p]...
  of cell write --agent a --scenario s --file metadata.json [--folder f]
  of verify --agent a --scenario s [--folder f] [--json]
  of record run --agent a --scenario s [--attach] [--dry-run]
  of record prereq-check --agent a
  of record verify --agent a --scenario s`

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "validate":
		return runValidate(args[1:], stdout, stderr)
	case "coverage":
		return runCoverage(args[1:], stdout, stderr)
	case "scenario":
		return runScenario(args[1:], stdout, stderr)
	case "agent":
		return runAgent(args[1:], stdout, stderr)
	case "cell":
		return runCell(args[1:], stdout, stderr)
	case "verify":
		return runVerify(args[1:], stdout, stderr)
	case "record":
		return runRecord(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// cellView is the per-cell projection `of status` emits: the matrix display
// state + the 3 pillars (agent / daemon / driver) the issue wants surfaced.
type cellView struct {
	DisplayState     string `json:"display_state"`
	Recorded         bool   `json:"recorded"`
	Route            string `json:"route"`
	Disposition      string `json:"disposition"`
	AgentSupports    string `json:"agent_supports,omitempty"`
	DaemonCapability string `json:"daemon_capability,omitempty"`
	DriverCapability string `json:"driver_capability,omitempty"`
}

type scenarioView struct {
	ID    string              `json:"id"`
	Name  string              `json:"name"`
	Cells map[string]cellView `json:"cells"`
}

type statusView struct {
	Agents    []string       `json:"agents"`
	Scenarios []scenarioView `json:"scenarios"`
}

func cellViewOf(cs matrix.CellState) cellView {
	v := cellView{
		DisplayState: cs.DisplayState,
		Recorded:     cs.Recorded,
		Route:        string(cs.Route),
		Disposition:  string(cs.Disposition),
	}
	if cs.Assessment != nil {
		v.AgentSupports = cs.Assessment.AgentSupports
		v.DaemonCapability = cs.Assessment.DaemonCapability
		v.DriverCapability = cs.Assessment.DriverCapability
	}
	return v
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("of status")
	var (
		agent    = fs.String("agent", "", "filter to one agent column")
		scenario = fs.String("scenario", "", "filter to one scenario (by name or id)")
		runs     = fs.Bool("runs", false, "show the factory run-log instead of coverage")
		asJSON   = fs.Bool("json", false, "emit JSON")
		repoRoot = fs.String("repo-root", ".", "repository root")
	)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if *runs {
		return runStatusRuns(*repoRoot, *asJSON, stdout, stderr)
	}

	m, err := matrix.LoadRepo(*repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "of status: %v\n", err)
		return exitUsage
	}
	agents := m.Agents()
	if *agent != "" {
		if !m.HasAgent(*agent) {
			fmt.Fprintf(stderr, "of status: %q is not an onboarded agent\n", *agent)
			return exitUsage
		}
		agents = []string{*agent}
	}

	view := statusView{Agents: agents}
	for _, sh := range shard.LoadAll(*repoRoot) {
		if *scenario != "" && sh.Name != *scenario && sh.ID != *scenario {
			continue
		}
		sv := scenarioView{ID: sh.ID, Name: sh.Name, Cells: map[string]cellView{}}
		for _, a := range agents {
			if cs, ok := m.Cell(a, sh.Name); ok {
				sv.Cells[a] = cellViewOf(cs)
			}
		}
		view.Scenarios = append(view.Scenarios, sv)
	}

	if *asJSON {
		if err := writeJSON(stdout, view); err != nil {
			fmt.Fprintf(stderr, "of status: encode: %v\n", err)
			return exitUsage
		}
		return exitOK
	}
	printStatusText(stdout, view)
	return exitOK
}

func printStatusText(stdout io.Writer, view statusView) {
	fmt.Fprintf(stdout, "scenarios × agents — %d × %d (display state per cell)\n\n", len(view.Scenarios), len(view.Agents))
	for _, sv := range view.Scenarios {
		fmt.Fprintf(stdout, "%-6s %-34s", sv.ID, sv.Name)
		for _, a := range view.Agents {
			c, ok := sv.Cells[a]
			st := "—"
			if ok {
				st = c.DisplayState
			}
			fmt.Fprintf(stdout, "  %s=%s", a, st)
		}
		fmt.Fprintln(stdout)
	}
}
