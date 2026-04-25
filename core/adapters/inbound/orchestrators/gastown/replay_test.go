package gastown

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"irrlicht/core/domain/orchestrator"
	"irrlicht/core/domain/session"
)

// replay_test.go drives the gastown poller against canned `gt` responses and
// rigs.json sidecars to lock down orchestrator state across the five
// scenarios in testdata/orchestrator/gastown/. See issue #201.
//
// Run `go test ./core/adapters/inbound/orchestrators/gastown/...
//   -run TestGastownReplay -update-goldens` to regenerate the goldens after
// an intentional behavior change.

var updateGoldens = flag.Bool("update-goldens", false, "regenerate orchestrator scenario goldens instead of comparing")

// scenariosRel is used when GASTOWN_FIXTURES_DIR is unset. The skill's
// drive-gastown.sh sets that env var to a staging directory so it can
// regenerate goldens without touching committed testdata/.
const scenariosRel = "../../../../../testdata/orchestrator/gastown"

type scenarioConfig struct {
	Description string `json:"description"`
	PollTicks   int    `json:"poll_ticks"`
}

type sessionFixture struct {
	SessionID string `json:"session_id"`
	State     string `json:"state"`
	CWD       string `json:"cwd"`
}

type fakeSessionLister struct {
	sessions []*session.SessionState
}

func (f *fakeSessionLister) ListAll() ([]*session.SessionState, error) {
	return f.sessions, nil
}

func TestGastownReplay(t *testing.T) {
	scenariosDir := os.Getenv("GASTOWN_FIXTURES_DIR")
	if scenariosDir == "" {
		scenariosDir = scenariosRel
	}
	scenariosDir, err := filepath.Abs(scenariosDir)
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	entries, err := os.ReadDir(scenariosDir)
	if err != nil {
		t.Fatalf("read scenarios dir %s: %v", scenariosDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			runScenario(t, filepath.Join(scenariosDir, name))
		})
	}
}

func runScenario(t *testing.T, scenarioDir string) {
	t.Helper()

	var cfg scenarioConfig
	mustReadJSON(t, filepath.Join(scenarioDir, "scenario.json"), &cfg)
	if cfg.PollTicks <= 0 {
		cfg.PollTicks = 1
	}

	gtRoot := t.TempDir()

	// Seed daemon/state.json and rigs.json into the scratch GT_ROOT so the
	// collector detects it and caches rigs for the fallback path.
	if err := os.MkdirAll(filepath.Join(gtRoot, "daemon"), 0o755); err != nil {
		t.Fatal(err)
	}
	copyFile(t, filepath.Join(scenarioDir, "input/rigs.json"),
		filepath.Join(gtRoot, "rigs.json"))
	copyFile(t, filepath.Join(scenarioDir, "input/daemon/state.json"),
		filepath.Join(gtRoot, "daemon/state.json"))

	// Per-tick counter file. The fake gt reads this to dispatch responses.
	tickWorkdir := t.TempDir()
	tickFile := filepath.Join(tickWorkdir, "tick")
	writeTick(t, tickFile, 1)

	ticksDir := filepath.Join(scenarioDir, "input/ticks")
	fakeGT := writeFakeGT(t, ticksDir, tickFile)

	// Sessions for CWD-based role matching.
	var fixtures []sessionFixture
	mustReadJSON(t, filepath.Join(scenarioDir, "input/sessions.json"), &fixtures)
	sessions := make([]*session.SessionState, 0, len(fixtures))
	for _, f := range fixtures {
		cwd := strings.ReplaceAll(f.CWD, "<GT_ROOT>", gtRoot)
		sessions = append(sessions, &session.SessionState{
			SessionID: f.SessionID,
			State:     f.State,
			CWD:       cwd,
		})
	}

	// Wire collector + poller directly. We don't run the Adapter loop
	// because BuildOrchestratorState is the entire surface we care about.
	t.Setenv("GT_ROOT", gtRoot)
	c := New()
	if !c.Detected() {
		t.Fatalf("collector did not detect scratch GT_ROOT %s", gtRoot)
	}
	p := newPoller(c, fakeGT, time.Second, &fakeSessionLister{sessions: sessions})

	goldenDir := filepath.Join(scenarioDir, "golden")
	if *updateGoldens {
		if err := os.MkdirAll(goldenDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for tick := 1; tick <= cfg.PollTicks; tick++ {
		writeTick(t, tickFile, tick)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		state := p.BuildOrchestratorState(ctx)
		cancel()

		actualJSON := mustMarshal(t, normalizeState(state, gtRoot))
		goldenPath := filepath.Join(goldenDir, fmt.Sprintf("state-%03d.json", tick))

		if *updateGoldens {
			if err := os.WriteFile(goldenPath, actualJSON, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("wrote %s", goldenPath)
			continue
		}

		expected, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v (run with -update-goldens to create)", goldenPath, err)
		}
		if string(expected) != string(actualJSON) {
			t.Errorf("tick %d state mismatch\n--- expected (%s) ---\n%s\n--- actual ---\n%s",
				tick, goldenPath, string(expected), string(actualJSON))
		}
	}
}

// normalizeState produces a deterministic snapshot suitable for golden
// comparison: zero out UpdatedAt, replace tmp paths with <GT_ROOT>, and
// sort slices that the poller already orders (defensive).
func normalizeState(s *orchestrator.State, gtRoot string) orchestrator.State {
	if s == nil {
		return orchestrator.State{}
	}
	cp := *s
	cp.UpdatedAt = time.Time{}
	cp.RoleIcons = nil
	cp.Root = stripRoot(cp.Root, gtRoot)

	codebases := make([]orchestrator.Codebase, len(cp.Codebases))
	for i, cb := range cp.Codebases {
		wts := make([]orchestrator.Worktree, len(cb.Worktrees))
		for j, wt := range cb.Worktrees {
			wt.Path = stripRoot(wt.Path, gtRoot)
			wts[j] = wt
		}
		cb.Worktrees = wts
		codebases[i] = cb
	}
	cp.Codebases = codebases
	sort.Slice(cp.Codebases, func(i, j int) bool {
		return cp.Codebases[i].Name < cp.Codebases[j].Name
	})
	return cp
}

func stripRoot(path, gtRoot string) string {
	if path == "" {
		return ""
	}
	if path == gtRoot {
		return "<GT_ROOT>"
	}
	if strings.HasPrefix(path, gtRoot+string(filepath.Separator)) {
		return "<GT_ROOT>" + path[len(gtRoot):]
	}
	return path
}

func mustReadJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return append(data, '\n')
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

func writeTick(t *testing.T, path string, tick int) {
	t.Helper()
	if err := os.WriteFile(path, fmt.Appendf(nil, "%d\n", tick), 0o644); err != nil {
		t.Fatalf("write tick: %v", err)
	}
}

// writeFakeGT generates a bash shim that pretends to be the gt CLI:
// reads the tick number from tickFile, looks up the matching fixture
// under ticksDir/tick-NNN/, and cats it. A `fail` marker file in the
// tick directory simulates gt being completely unavailable.
func writeFakeGT(t *testing.T, ticksDir, tickFile string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "fake-gt")
	script := fmt.Sprintf(`#!/usr/bin/env bash
set -uo pipefail
TICK=$(cat %q 2>/dev/null || echo "1")
DIR=%q/tick-$(printf '%%03d' "$TICK")
if [ -f "$DIR/fail" ]; then exit 1; fi
case "$1 $2" in
  "rig list")     RESP="$DIR/rig-list.json" ;;
  "polecat list") RESP="$DIR/polecat-list.json" ;;
  "dog list")     RESP="$DIR/dog-list.json" ;;
  "boot status")  RESP="$DIR/boot-status.json" ;;
  *) echo "fake-gt: unknown command: $*" >&2; exit 2 ;;
esac
if [ ! -f "$RESP" ]; then exit 1; fi
cat "$RESP"
`, tickFile, ticksDir)
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake gt: %v", err)
	}
	return bin
}
