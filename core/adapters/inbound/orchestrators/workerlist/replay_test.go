package workerlist

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"irrlicht/core/domain/orchestrator"
)

// replay_test.go drives the document-to-State mapping (decodeDocument then
// buildState) over the scenarios in replaydata/orchestrators/workerlist/scenarios/,
// as gastown's TestGastownReplay drives its poller (#2148). Each scenario has
// scenario.json (a `now` per tick and a hand-written `verify` block),
// input/ticks/tick-NNN/status.json, and golden/state-NNN.json.
//
// Two checks per tick, because a golden written by the code under test only
// proves the code still does what it did: the golden comparison locks the
// whole State, and the verify block — written by hand from the schema, not
// from output — states the facts each scenario exists for.
//
// Regenerate the goldens after an intentional change with
//   go test ./core/adapters/inbound/orchestrators/workerlist/ -run TestWorkerlistReplay -update-goldens -count=1

var updateGoldens = flag.Bool("update-goldens", false, "regenerate worker-list scenario goldens instead of comparing")

// scenariosRel is used when WORKERLIST_FIXTURES_DIR is unset.
const scenariosRel = "../../../../../replaydata/orchestrators/workerlist/scenarios"

type scenarioConfig struct {
	Description string         `json:"description"`
	Ticks       []scenarioTick `json:"ticks"`
	Verify      scenarioVerify `json:"verify"`
}

type scenarioTick struct {
	Now time.Time `json:"now"`
}

// scenarioVerify holds the hand-written expectations. A single-tick key
// applies to the last tick; a _per_tick key holds one entry per tick.
type scenarioVerify struct {
	Name                *string        `json:"name"`
	Teams               []string       `json:"teams"`
	TeamStatus          []string       `json:"team_status"`
	TeamStatusPerTick   [][]string     `json:"team_status_per_tick"`
	WorkerStates        []string       `json:"worker_states"`
	WorkerIDs           []string       `json:"worker_ids"`
	StaleWorkers        []string       `json:"stale_workers"`
	StaleWorkersPerTick [][]string     `json:"stale_workers_per_tick"`
	LastExit            map[string]int `json:"last_exit"`
}

func TestWorkerlistReplay(t *testing.T) {
	dir := os.Getenv("WORKERLIST_FIXTURES_DIR")
	if dir == "" {
		dir = scenariosRel
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read scenarios dir %s: %v", dir, err)
	}
	ran := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		ran++
		t.Run(e.Name(), func(t *testing.T) { runScenario(t, filepath.Join(dir, e.Name())) })
	}
	// A replay that found nothing to replay must not read as a pass.
	if ran == 0 {
		t.Fatalf("no scenarios under %s", dir)
	}
}

func runScenario(t *testing.T, dir string) {
	t.Helper()
	cfg := readScenario(t, filepath.Join(dir, "scenario.json"))
	states := make([]*orchestrator.State, 0, len(cfg.Ticks))
	for i, tick := range cfg.Ticks {
		st := replayTick(t, dir, i+1, tick.Now)
		states = append(states, st)
		compareGolden(t, st, dir, i+1)
	}
	checkVerify(t, cfg.Verify, states)
}

// replayTick decodes tick n's status file and maps it as of now.
func replayTick(t *testing.T, dir string, n int, now time.Time) *orchestrator.State {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "input", "ticks", fmt.Sprintf("tick-%03d", n), "status.json"))
	if err != nil {
		t.Fatalf("tick %d: %v", n, err)
	}
	doc, err := decodeDocument(data)
	if err != nil {
		t.Fatalf("tick %d: fixture does not decode: %v", n, err)
	}
	return buildState(doc, now)
}

// compareGolden compares st with the scenario's golden for tick n, or writes
// that golden under -update-goldens.
func compareGolden(t *testing.T, st *orchestrator.State, dir string, tick int) {
	t.Helper()
	path := filepath.Join(dir, "golden", fmt.Sprintf("state-%03d.json", tick))
	got, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *updateGoldens {
		writeGolden(t, path, got)
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with -update-goldens to create)", path, err)
	}
	if string(want) != string(got) {
		t.Errorf("tick %d state mismatch\n--- expected (%s) ---\n%s\n--- actual ---\n%s", tick, path, want, got)
	}
}

// checkVerify asserts the scenario's hand-written facts against the states.
func checkVerify(t *testing.T, v scenarioVerify, states []*orchestrator.State) {
	t.Helper()
	last := states[len(states)-1]
	if v.Name != nil && last.Name != *v.Name {
		t.Errorf("verify name: got %q, want %q", last.Name, *v.Name)
	}
	expect(t, "teams", v.Teams, teamField(last, func(cb orchestrator.Codebase) string { return cb.Name }))
	expect(t, "team_status", v.TeamStatus, teamStatuses(last))
	expect(t, "worker_states", v.WorkerStates, workerField(last, func(w orchestrator.Worker) string { return w.State }))
	expect(t, "worker_ids", v.WorkerIDs, workerField(last, func(w orchestrator.Worker) string { return w.ID }))
	expect(t, "stale_workers", v.StaleWorkers, staleNames(last))
	expectPerTick(t, "team_status_per_tick", v.TeamStatusPerTick, states, teamStatuses)
	expectPerTick(t, "stale_workers_per_tick", v.StaleWorkersPerTick, states, staleNames)
	if v.LastExit != nil {
		got := map[string]int{}
		for _, w := range allWorkers(last) {
			if w.LastExit != nil {
				got[w.Name] = *w.LastExit
			}
		}
		if !reflect.DeepEqual(got, v.LastExit) {
			t.Errorf("verify last_exit: got %v, want %v", got, v.LastExit)
		}
	}
}

// expect compares want with got when want is set (nil means "not verified";
// an empty list is a real expectation of none).
func expect(t *testing.T, key string, want, got []string) {
	t.Helper()
	if want != nil && !reflect.DeepEqual(got, want) {
		t.Errorf("verify %s: got %q, want %q", key, got, want)
	}
}

func expectPerTick(t *testing.T, key string, want [][]string, states []*orchestrator.State, field func(*orchestrator.State) []string) {
	t.Helper()
	if want == nil {
		return
	}
	if len(want) != len(states) {
		t.Fatalf("verify %s: %d entries for %d ticks", key, len(want), len(states))
	}
	for i, st := range states {
		expect(t, fmt.Sprintf("%s[tick %d]", key, i+1), want[i], field(st))
	}
}

func teamField(st *orchestrator.State, f func(orchestrator.Codebase) string) []string {
	out := []string{}
	for _, cb := range st.Codebases {
		out = append(out, f(cb))
	}
	return out
}

func teamStatuses(st *orchestrator.State) []string {
	return teamField(st, func(cb orchestrator.Codebase) string { return cb.Status })
}

func allWorkers(st *orchestrator.State) []orchestrator.Worker {
	var out []orchestrator.Worker
	for _, cb := range st.Codebases {
		for _, wt := range cb.Worktrees {
			out = append(out, wt.Workers...)
		}
	}
	return out
}

func workerField(st *orchestrator.State, f func(orchestrator.Worker) string) []string {
	out := []string{}
	for _, w := range allWorkers(st) {
		out = append(out, f(w))
	}
	return out
}

func staleNames(st *orchestrator.State) []string {
	out := []string{}
	for _, w := range allWorkers(st) {
		if w.Stale {
			out = append(out, w.Name)
		}
	}
	return out
}

// readScenario decodes scenario.json strictly: an unknown key — a misspelt
// verify key above all — fails rather than being ignored, and a scenario
// must list ticks and verify something, so a verify block cannot pass while
// checking nothing.
func readScenario(t *testing.T, path string) scenarioConfig {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var cfg scenarioConfig
	if err := dec.Decode(&cfg); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(cfg.Ticks) == 0 {
		t.Fatalf("%s lists no ticks", path)
	}
	if reflect.ValueOf(cfg.Verify).IsZero() {
		t.Fatalf("%s has an empty verify block", path)
	}
	return cfg
}

func writeGolden(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
