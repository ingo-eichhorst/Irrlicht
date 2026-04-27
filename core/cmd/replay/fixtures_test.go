package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestFixtureReplayByteIdentity pins the JSON output of every
// replaydata/agents/**/*.jsonl fixture to a committed golden. Refresh with:
//
//	UPDATE_REPLAY_GOLDENS=1 go test ./core/cmd/replay/...
func TestFixtureReplayByteIdentity(t *testing.T) {
	// Walk the agents tree from its repo-root anchor. We cannot reuse
	// fixturePath here because that helper inserts a "scenarios/" segment
	// for "<adapter>/..." inputs; here we want the bare adapters root.
	fixturesDir, err := filepath.Abs(filepath.Join("..", "..", "..", "replaydata", "agents"))
	if err != nil {
		t.Fatalf("abs fixtures dir: %v", err)
	}
	fixtures := discoverReplayFixtures(t, fixturesDir)
	if len(fixtures) == 0 {
		t.Fatalf("no .jsonl fixtures discovered under %s", fixturesDir)
	}

	update := os.Getenv("UPDATE_REPLAY_GOLDENS") == "1"

	for _, fx := range fixtures {
		t.Run(filepath.Base(fx), func(t *testing.T) {
			t.Parallel()
			got := runFixtureReplay(t, fx)
			goldenPath := fx + ".replay.json.golden"

			if update {
				if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s: %v (run with UPDATE_REPLAY_GOLDENS=1 to create)", goldenPath, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("replay output differs from golden %s\n"+
					"run UPDATE_REPLAY_GOLDENS=1 go test ./core/cmd/replay/... to refresh\n"+
					"first diff: %s", goldenPath, firstJSONDiff(got, want))
			}
		})
	}
}

func discoverReplayFixtures(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		// Skip lifecycle-event sidecars: legacy <scenario>.events.jsonl naming
		// AND the post-WS02 per-scenario-folder naming where the basename is
		// exactly "events.jsonl".
		base := filepath.Base(path)
		if base == "events.jsonl" || strings.HasSuffix(path, ".events.jsonl") {
			return nil
		}
		out = append(out, path)
		return nil
	})
	if err != nil {
		t.Fatalf("walk fixtures: %v", err)
	}
	return out
}

// runFixtureReplay dispatches through the same runReplay() path as main(),
// zeroes per-run fields (GeneratedAt, SourceTranscript) on the returned
// report so goldens are stable across worktrees and clones, and returns
// the indented JSON.
func runFixtureReplay(t *testing.T, transcriptPath string) []byte {
	t.Helper()
	adapter, err := detectAdapter(transcriptPath)
	if err != nil {
		t.Fatalf("detectAdapter %s: %v", transcriptPath, err)
	}
	_, sidecarPath, useSidecar := resolveInputPaths(transcriptPath)
	cfg := reportSettings{
		Adapter:            adapter,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	}
	report, err := runReplay(transcriptPath, sidecarPath, useSidecar, cfg)
	if err != nil {
		t.Fatalf("runReplay %s: %v", transcriptPath, err)
	}
	report.GeneratedAt = time.Time{}
	report.SourceTranscript = ""

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		t.Fatalf("encode %s: %v", transcriptPath, err)
	}
	return buf.Bytes()
}

func firstJSONDiff(a, b []byte) string {
	n := min(len(a), len(b))
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return "first differing offset " + strconv.Itoa(i) + ": " +
				snippet(a, i) + " vs " + snippet(b, i)
		}
	}
	if len(a) != len(b) {
		return "lengths differ: got " + strconv.Itoa(len(a)) + " vs want " + strconv.Itoa(len(b))
	}
	return "equal"
}

func snippet(s []byte, i int) string {
	start := max(i-16, 0)
	end := min(i+16, len(s))
	return "'" + string(s[start:end]) + "'"
}
