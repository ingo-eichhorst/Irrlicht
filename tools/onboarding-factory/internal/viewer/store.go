package viewer

import (
	"os"
	"path/filepath"
	"sort"
)

// RecordingStore is the filesystem repository for the replaydata tree. It
// is the seam between the HTTP handlers and disk: handlers ask it for
// scenario files, scenario/archive listings, and expected.jsonl presence
// rather than reaching into os.ReadFile / os.ReadDir with hand-built
// paths. RepoRoot is the directory containing replaydata/.
//
// Config files outside replaydata/ (scenarios.json, the coverage rollup,
// the spec markdown) are deliberately NOT the store's concern — they're
// resolved by the *Server's path resolvers, since in a git worktree they
// may live in the main checkout rather than under RepoRoot.
type RecordingStore struct {
	RepoRoot string
}

func (st RecordingStore) agentsDir() string {
	return filepath.Join(st.RepoRoot, "replaydata", "agents")
}

// scenarioDir is the canonical recording directory for one cell.
func (st RecordingStore) scenarioDir(agent, subtree, id string) string {
	return filepath.Join(st.agentsDir(), agent, subtree, id)
}

// readFile reads a path joined onto RepoRoot's replaydata tree, or any
// absolute path passed through filepath.Join. Returns the bytes and ok=false
// when the file is absent or unreadable.
func (st RecordingStore) readFile(path string) ([]byte, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return b, true
}

// exists reports whether path is present on disk.
func (st RecordingStore) exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// listScenarios walks replaydata/agents/<agent>/{scenarios,regressions}/<id>
// and returns every recording cell, sorted by (agent, subtree, id).
func (st RecordingStore) listScenarios() []ScenarioListEntry {
	entries, _ := os.ReadDir(st.agentsDir())
	var out []ScenarioListEntry
	for _, agentEntry := range entries {
		if !agentEntry.IsDir() {
			continue
		}
		agent := agentEntry.Name()
		for _, subtree := range []string{"scenarios", "regressions"} {
			scns, _ := os.ReadDir(filepath.Join(st.agentsDir(), agent, subtree))
			for _, sd := range scns {
				if !sd.IsDir() {
					continue
				}
				out = append(out, ScenarioListEntry{Agent: agent, Subtree: subtree, ID: sd.Name()})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Agent != out[j].Agent {
			return out[i].Agent < out[j].Agent
		}
		if out[i].Subtree != out[j].Subtree {
			return out[i].Subtree < out[j].Subtree
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// listArchiveDirs returns the archive subdirectory names under
// <scenarioDir>/recordings/, or nil when the dir is absent.
func (st RecordingStore) listArchiveDirs(scenarioDir string) []string {
	entries, err := os.ReadDir(filepath.Join(scenarioDir, "recordings"))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out
}

// agentHasExpectedJSONL reports whether
// replaydata/agents/<agent>/scenarios/<scenarioName>/expected.jsonl exists
// for one specific agent.
func (st RecordingStore) agentHasExpectedJSONL(agent, scenarioName string) bool {
	return st.exists(filepath.Join(st.agentsDir(), agent, "scenarios", scenarioName, "expected.jsonl"))
}

// hasExpectedJSONL reports whether ANY agent's scenario folder for the
// given scenario name contains an expected.jsonl. Used to disambiguate
// coverage_id collisions in favour of the scenario whose folder actually
// backs the matrix cell.
func (st RecordingStore) hasExpectedJSONL(scenarioName string) bool {
	entries, err := os.ReadDir(st.agentsDir())
	if err != nil {
		return false
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if st.exists(filepath.Join(st.agentsDir(), e.Name(), "scenarios", scenarioName, "expected.jsonl")) {
			return true
		}
	}
	return false
}
