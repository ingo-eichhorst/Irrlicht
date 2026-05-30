package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// newFlagSet returns a ContinueOnError flag set that writes errors to stderr,
// so a bad flag yields exitUsage rather than a panic.
func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	return fs
}

// RunRecord is one line of the factory run-log. The write-side verbs (assess /
// record, added in a later phase) append one per cell touched; `of status
// --runs` reads them back so the skill can ask "what happened last sweep"
// without re-deriving. Kept deliberately flat (every object has an id).
type RunRecord struct {
	ID       string `json:"id"`        // unique run-step id
	StartedAt string `json:"started_at"`
	Verb     string `json:"verb"`     // assess | record | ...
	Agent    string `json:"agent,omitempty"`
	Scenario string `json:"scenario,omitempty"`
	Outcome  string `json:"outcome"`  // recorded | verify_failed | prereq_blocked | ...
	Note     string `json:"note,omitempty"`
}

// runLogPath is the append-only factory run-log. Lives under _reports/ (already
// gitignored) so run history never pollutes the committed fixtures.
func runLogPath(repoRoot string) string {
	return filepath.Join(repoRoot, "replaydata", "agents", "_reports", "runs.jsonl")
}

// readRunLog returns every parseable RunRecord, newest last. A missing file is
// not an error — it just means no runs have happened yet.
func readRunLog(repoRoot string) ([]RunRecord, error) {
	f, err := os.Open(runLogPath(repoRoot))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []RunRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r RunRecord
		if json.Unmarshal(line, &r) == nil {
			out = append(out, r)
		}
	}
	return out, sc.Err()
}

func runStatusRuns(repoRoot string, asJSON bool, stdout, stderr io.Writer) int {
	recs, err := readRunLog(repoRoot)
	if err != nil {
		fmt.Fprintf(stderr, "of status --runs: %v\n", err)
		return exitUsage
	}
	if asJSON {
		if recs == nil {
			recs = []RunRecord{}
		}
		if err := writeJSON(stdout, recs); err != nil {
			fmt.Fprintf(stderr, "of status --runs: encode: %v\n", err)
			return exitUsage
		}
		return exitOK
	}
	if len(recs) == 0 {
		fmt.Fprintln(stdout, "no factory runs recorded yet")
		return exitOK
	}
	for _, r := range recs {
		fmt.Fprintf(stdout, "%s  %-8s %-12s %-26s %s  %s\n",
			r.StartedAt, r.Verb, r.Agent, r.Scenario, r.Outcome, r.Note)
	}
	return exitOK
}
