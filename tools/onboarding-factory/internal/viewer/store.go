package viewer

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// underRoot reports whether path, once cleaned, resolves to somewhere inside
// st.agentsDir() — the backstop readFile/exists/listArchiveDirs funnel every
// lookup through: whatever hand-built path a caller passes (however its
// pieces were assembled upstream), it can never resolve to a file outside
// the replaydata/agents tree this store exists to serve.
func (st RecordingStore) underRoot(path string) bool {
	rel, err := filepath.Rel(st.agentsDir(), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// readFile reads a path joined onto RepoRoot's replaydata tree, or any
// absolute path passed through filepath.Join. Returns the bytes and ok=false
// when the file is absent, unreadable, or escapes the tree this store serves.
func (st RecordingStore) readFile(path string) ([]byte, bool) {
	if !st.underRoot(path) {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return b, true
}

// exists reports whether path is present on disk and within the tree this
// store serves.
func (st RecordingStore) exists(path string) bool {
	if !st.underRoot(path) {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// SafeArchiveName is a recording-archive directory name that has already
// been checked against path traversal (no ".." segment, no path
// separator). The zero value is invalid — construct via NewSafeArchiveName.
// archiveFilePath is the only thing that accepts this type, so a future
// call site building a path under an archive dir can't skip the check by
// passing a raw string instead.
type SafeArchiveName string

// NewSafeArchiveName validates name and returns it as a SafeArchiveName. An
// archive name is joined one path segment below <scenarioDir>/recordings/,
// so a ".." segment or an embedded separator would let it escape that
// directory.
func NewSafeArchiveName(name string) (SafeArchiveName, error) {
	if name == "" || strings.Contains(name, "..") || strings.ContainsRune(name, filepath.Separator) {
		return "", fmt.Errorf("invalid archive name %q", name)
	}
	return SafeArchiveName(name), nil
}

// archiveFilePath resolves relPath under scenarioDir/recordings/<name>.
// relPath may be "" to get the archive directory itself.
func (st RecordingStore) archiveFilePath(scenarioDir string, name SafeArchiveName, relPath string) string {
	return filepath.Join(scenarioDir, "recordings", string(name), relPath)
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
	recordingsDir := filepath.Join(scenarioDir, "recordings")
	if !st.underRoot(recordingsDir) {
		return nil
	}
	entries, err := os.ReadDir(recordingsDir)
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
