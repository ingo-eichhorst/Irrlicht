// Command of is the onboarding-factory CLI — the single entry point the
// onboarding skill drives for everything under replaydata/. It NEVER lets the
// skill touch replaydata files directly: read-side commands derive their answer
// from the canonical matrix model (internal/matrix) + the catalog shards
// (internal/shard); write-side commands (added in a later phase) are the sole
// writers and validate before they touch disk.
//
// Read-side commands (this phase):
//
//	of status   [--agent a] [--scenario s] [--profile p] [--runs] [--summary] [--json]  coverage / run status
//	of validate [--json]                                                  schema + referential integrity
//	of coverage [--hooks] [--json]                                        derived rollup, or hook coverage
//	of hookcheck --agent a --events e                                     one staged recording's hook coverage
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
	"path/filepath"

	"irrlicht/tools/onboarding-factory/internal/matrix"
	"irrlicht/tools/onboarding-factory/internal/shard"
)

const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

const usage = `usage:
  of status   [--agent a] [--scenario s] [--profile cli-local|desktop-local] [--runs] [--summary] [--json] [--repo-root .]
  of validate [--json] [--repo-root .]
  of coverage [--hooks] [--json] [--repo-root .]
  of scenario add|update --name n [--id i] [--description d] [--process-file f] [--acceptance-file f]
  of scenario show --name n [--json]
  of agent add    --id i --name n --provider p [--min-version v] [--prereq p]...
  of agent update --id i [--name n] [--provider p] [--min-version v] [--prereq p]... [--add-prereq p]...
  of cell write --agent a --scenario s --file metadata.json [--folder f]
  of cell spec  --agent a --scenario s --file expected.jsonl [--folder f]
  of verify --agent a --scenario s [--folder f] [--json]
  of record run --agent a --scenario s [--attach] [--dry-run]
  of record prereq-check --agent a
  of record verify --agent a --scenario s
  of hookcheck --agent a --events events.jsonl`

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
	case "hookcheck":
		return runHookCheck(args[1:], stdout, stderr)
	default:
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
}

// emitSummary renders the folded per-agent counts, as JSON or as the text
// table.
func emitSummary(m *matrix.Matrix, view statusView, asJSON bool, stdout, stderr io.Writer) int {
	sv := buildSummaryView(m, view)
	if asJSON {
		if err := writeJSON(stdout, sv); err != nil {
			fmt.Fprintf(stderr, "of status: encode: %v\n", err)
			return exitUsage
		}
		return exitOK
	}
	printSummaryText(stdout, sv)
	return exitOK
}

// absRoot resolves --repo-root to an absolute path. Every filesystem reader
// under internal/validate and internal/replay refuses a path containing ".."
// (a CodeQL taint barrier) by returning an EMPTY result rather than an error,
// so a relative root like "--repo-root .." silently reports a catalog in which
// nothing is recorded — exit 0, no warning. `of status --summary` renders that
// as a full table of zeros, which reads as a finished measurement rather than
// a misread path. Resolving once at the flag boundary is what makes the guard
// a no-op for legitimate callers instead of a trap.
func absRoot(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// cellView is the per-cell projection `of status` emits: the matrix display
// state + the 3 pillars (agent / daemon / driver) the issue wants surfaced.
type cellView struct {
	DisplayState      string `json:"display_state"`
	Recorded          bool   `json:"recorded"`
	ExecutionProfile  string `json:"execution_profile"`
	RecordingName     string `json:"recording_name,omitempty"`
	Entrypoint        string `json:"entrypoint,omitempty"`
	DaemonVersion     string `json:"daemon_version,omitempty"`
	AgentCLIVersion   string `json:"agent_cli_version,omitempty"`
	DesktopAppVersion string `json:"desktop_app_version,omitempty"`
	Route             string `json:"route"`
	Disposition       string `json:"disposition"`
	AgentSupports     string `json:"agent_supports,omitempty"`
	DaemonCapability  string `json:"daemon_capability,omitempty"`
	DriverCapability  string `json:"driver_capability,omitempty"`
	// Derived marks a cell synthesized from the capability model rather than
	// read from a directory (#1369). Emitted here because a reader filtering
	// a work-list needs to tell a modelled cell from a written one, and the
	// only other signal is the indirect frozen+applicable_false shape.
	Derived bool `json:"derived,omitempty"`
}

type scenarioView struct {
	ID    string              `json:"id"`
	Name  string              `json:"name"`
	Cells map[string]cellView `json:"cells"`
}

type statusView struct {
	ExecutionProfile string         `json:"execution_profile"`
	Agents           []string       `json:"agents"`
	Scenarios        []scenarioView `json:"scenarios"`
}

func cellViewOf(cs matrix.CellState) cellView {
	v := cellView{
		DisplayState:      cs.DisplayState,
		Recorded:          cs.Recorded,
		ExecutionProfile:  string(cs.ExecutionProfile),
		RecordingName:     cs.RecordingName,
		Entrypoint:        cs.Entrypoint,
		DaemonVersion:     cs.DaemonVersion,
		AgentCLIVersion:   cs.AgentCLIVersion,
		DesktopAppVersion: cs.DesktopAppVersion,
		Route:             string(cs.Route),
		Disposition:       string(cs.Disposition),
		Derived:           cs.Derived,
	}
	if cs.Assessment != nil {
		v.AgentSupports = cs.Assessment.AgentSupports
		v.DaemonCapability = cs.Assessment.DaemonCapability
		v.DriverCapability = cs.Assessment.DriverCapability
	}
	return v
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	request, err := parseStatusRequest(args)
	if err != nil {
		fmt.Fprintf(stderr, "of status: %v\n", err)
		return exitUsage
	}
	if request.Runs {
		return runStatusRuns(request.RepoRoot, request.JSON, stdout, stderr)
	}
	return runMatrixStatus(request, stdout, stderr)
}

type statusRequest struct {
	Agent, Scenario, RepoRoot string
	Profile                   matrix.ExecutionProfile
	Runs, Summary, JSON       bool
}

func parseStatusRequest(args []string) (statusRequest, error) {
	fs := newFlagSet("of status")
	var (
		agent    = fs.String("agent", "", "filter to one agent column")
		scenario = fs.String("scenario", "", "filter to one scenario (by name or id)")
		runs     = fs.Bool("runs", false, "show the factory run-log instead of coverage")
		summary  = fs.Bool("summary", false, "per-agent cell counts instead of the full cell dump")
		profile  = fs.String("profile", string(matrix.ProfileCLILocal), "execution profile (cli-local or desktop-local)")
		asJSON   = fs.Bool("json", false, "emit JSON")
		repoRoot = fs.String("repo-root", ".", "repository root")
	)
	if err := fs.Parse(args); err != nil {
		return statusRequest{}, err
	}
	*repoRoot = absRoot(*repoRoot)

	executionProfile, err := statusExecutionProfile(*profile, *runs, flagPassed(fs, "profile"))
	if err != nil {
		return statusRequest{}, err
	}
	return statusRequest{
		Agent: *agent, Scenario: *scenario, RepoRoot: *repoRoot,
		Profile: executionProfile, Runs: *runs, Summary: *summary, JSON: *asJSON,
	}, nil
}

func runMatrixStatus(request statusRequest, stdout, stderr io.Writer) int {
	m, view, err := statusViewForRequest(request)
	if err != nil {
		fmt.Fprintf(stderr, "of status: %v\n", err)
		return exitUsage
	}
	return emitStatus(m, view, request, statusOutput{stdout: stdout, stderr: stderr})
}

type statusOutput struct{ stdout, stderr io.Writer }

func statusViewForRequest(request statusRequest) (*matrix.Matrix, statusView, error) {
	m, err := matrix.LoadRepoForProfile(request.RepoRoot, request.Profile)
	if err != nil {
		return nil, statusView{}, err
	}
	agents, err := statusAgents(m, request.Agent)
	if err != nil {
		return nil, statusView{}, err
	}
	view := buildStatusView(m, request.RepoRoot, agents, request.Scenario)

	// Validate --scenario the way --agent is validated. An unmatched filter
	// used to yield a visibly empty listing; under --summary it yields a full
	// table of zeros that reads as a completed measurement, so a typo has to
	// fail loudly instead. Checked on the built view rather than by a second
	// catalog walk, so the guard and the filter are literally the same
	// predicate — buildStatusView emits a row for every matching shard, so an
	// empty result means nothing matched.
	if request.Scenario != "" && len(view.Scenarios) == 0 {
		return nil, statusView{}, fmt.Errorf("%q is not a scenario (by name or id)", request.Scenario)
	}
	return m, view, nil
}

func statusAgents(m *matrix.Matrix, agent string) ([]string, error) {
	if agent == "" {
		return m.Agents(), nil
	}
	if !m.HasAgent(agent) {
		return nil, fmt.Errorf("%q is not an onboarded agent", agent)
	}
	return []string{agent}, nil
}

func emitStatus(m *matrix.Matrix, view statusView, request statusRequest, output statusOutput) int {
	// --summary folds the same view into per-agent counts. Folding the view
	// (rather than re-reading the matrix) is what keeps the two renderings of
	// `of status` arithmetically consistent.
	if request.Summary {
		return emitSummary(m, view, request.JSON, output.stdout, output.stderr)
	}

	if request.JSON {
		if err := writeJSON(output.stdout, view); err != nil {
			fmt.Fprintf(output.stderr, "of status: encode: %v\n", err)
			return exitUsage
		}
		return exitOK
	}
	printStatusText(output.stdout, view)
	return exitOK
}

func statusExecutionProfile(value string, runs, explicit bool) (matrix.ExecutionProfile, error) {
	profile, err := matrix.ParseExecutionProfile(value)
	if err != nil {
		return "", err
	}
	if runs && explicit {
		return "", fmt.Errorf("--profile cannot be used with --runs because run-log records have no execution-profile identity")
	}
	return profile, nil
}

// buildStatusView projects the matrix + catalog shards (optionally filtered
// to one scenario) into the per-cell view `of status` renders or encodes.
func buildStatusView(m *matrix.Matrix, repoRoot string, agents []string, scenarioFilter string) statusView {
	view := statusView{ExecutionProfile: string(m.ExecutionProfile()), Agents: agents}
	for _, sh := range shard.LoadAll(repoRoot) {
		if scenarioFilter != "" && sh.Name != scenarioFilter && sh.ID != scenarioFilter {
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
	return view
}

func printStatusText(stdout io.Writer, view statusView) {
	fmt.Fprintf(stdout, "scenarios × agents — %d × %d (profile %s; display state per cell)\n\n",
		len(view.Scenarios), len(view.Agents), view.ExecutionProfile)
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
