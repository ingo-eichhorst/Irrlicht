package desktopresults

// The Desktop verdict census answers "how did the Claude Desktop campaign
// actually come out", from the one artifact that records it.
//
// `of status --profile desktop-local` cannot answer it. That view derives each
// cell's display state from matrix.DeriveDisplayState, which reads the cell's
// ASSESSMENT axes plus whether a recording exists — and the assessment axes
// were measured for the CLI. A cell the Desktop driver cannot drive has an
// explicit, terminal not-runnable verdict here and no Desktop recording, so it
// derives to "pending-record": indistinguishable from a cell nobody has
// reached yet. 26 of the 50 claudecode cells are in exactly that position.
//
// Fixing the display state itself is a separate change with an import-direction
// problem behind it (matrix cannot import this package; this package imports
// matrix). This census is additive instead: it changes no existing number, and
// it gives a documented figure a command that produces it.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"irrlicht/tools/onboarding-factory/internal/matrix"
)

// VerdictCensus is one agent's Desktop Local verdicts, counted from the
// execution-results.json files themselves.
type VerdictCensus struct {
	Agent string `json:"agent"`
	// ByOutcome counts every desktop-local verdict, keyed by outcome. Outcomes
	// with no cell are present with a zero, so a reader can tell "none" from
	// "this build of the tool did not know about that outcome".
	ByOutcome map[string]int `json:"by_outcome"`
	// Cells is how many canonical cells the agent has.
	Cells int `json:"cells"`
	// WithVerdict is how many of them carry a desktop-local verdict.
	WithVerdict int `json:"with_verdict"`
	// WithoutVerdict names the cells carrying none, sorted. A count alone
	// would let "every cell is decided" and "the walk found nothing" print
	// the same way.
	WithoutVerdict []string `json:"without_verdict"`
}

// Outcomes returns every outcome the contract defines, in report order.
func Outcomes() []Outcome {
	return []Outcome{
		OutcomeObservedPassing,
		OutcomeObservedFailure,
		OutcomeNotRunnable,
		OutcomeUnobservable,
		OutcomeNotApplicable,
	}
}

// CountVerdicts walks one agent's cells and counts their Desktop Local
// verdicts.
//
// Every failure is returned, never skipped. A cell whose result artifact
// cannot be parsed is the one case where a silent zero would read exactly like
// a clean campaign, so it stops the census instead.
func CountVerdicts(repoRoot, agent string) (VerdictCensus, error) {
	scenariosRoot := filepath.Join(repoRoot, "replaydata", "agents", agent, "scenarios")
	entries, err := os.ReadDir(scenariosRoot)
	if err != nil {
		return VerdictCensus{}, fmt.Errorf("read %s: %w", scenariosRoot, err)
	}

	census := VerdictCensus{Agent: agent, ByOutcome: map[string]int{}}
	for _, outcome := range Outcomes() {
		census.ByOutcome[string(outcome)] = 0
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		cellDir := filepath.Join(scenariosRoot, entry.Name())
		if !isCell(cellDir) {
			continue
		}
		census.Cells++
		outcome, found, err := desktopOutcomeOf(cellDir)
		if err != nil {
			return VerdictCensus{}, err
		}
		if !found {
			census.WithoutVerdict = append(census.WithoutVerdict, entry.Name())
			continue
		}
		census.WithVerdict++
		census.ByOutcome[string(outcome)]++
	}
	sort.Strings(census.WithoutVerdict)
	return census, nil
}

// isCell reports whether a directory is a canonical cell, by its metadata.json.
func isCell(cellDir string) bool {
	info, err := os.Stat(filepath.Join(cellDir, metadataFile))
	return err == nil && !info.IsDir()
}

// desktopOutcomeOf returns a cell's desktop-local outcome. found=false means
// the cell carries no result artifact, or one with no desktop-local entry —
// both are "undecided", which the census reports by name.
func desktopOutcomeOf(cellDir string) (Outcome, bool, error) {
	path := filepath.Join(cellDir, FileName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return "", false, nil
	} else if err != nil {
		return "", false, fmt.Errorf("stat %s: %w", path, err)
	}
	doc, err := Load(path)
	if err != nil {
		return "", false, fmt.Errorf("load %s: %w", path, err)
	}
	for _, result := range doc.Results {
		if matrix.ExecutionProfile(result.ExecutionProfile) == matrix.ProfileDesktopLocal {
			return result.Outcome, true, nil
		}
	}
	return "", false, nil
}
