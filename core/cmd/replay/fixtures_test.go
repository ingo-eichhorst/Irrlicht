package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestFixtureReplayByteIdentity runs the replay binary over every
// testdata/replay/**/*.jsonl fixture and asserts the output matches a
// committed golden. The golden is stored next to the fixture as
// <fixture>.replay.json.golden with generated_at normalised to the zero
// time so the file is stable across runs.
//
// Any intentional classifier or tailer change must refresh the goldens:
//
//	UPDATE_REPLAY_GOLDENS=1 go test ./core/cmd/replay/...
//
// Without that env var a golden-output diff fails the test, making this
// the persistent version of the manual parity check performed during the
// #194 refactor.
func TestFixtureReplayByteIdentity(t *testing.T) {
	fixturesDir := fixturePath(t, ".")
	fixtures := discoverReplayFixtures(t, fixturesDir)
	if len(fixtures) == 0 {
		t.Fatalf("no .jsonl fixtures discovered under %s", fixturesDir)
	}

	update := os.Getenv("UPDATE_REPLAY_GOLDENS") == "1"

	for _, fx := range fixtures {
		t.Run(filepath.Base(fx), func(t *testing.T) {
			got := runFixtureReplay(t, fx)
			got = normaliseReportJSON(t, got)
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

// discoverReplayFixtures returns every real transcript .jsonl under
// testdata/replay/, excluding the paired lifecycle sidecars.
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
		if !strings.HasSuffix(path, ".jsonl") || strings.HasSuffix(path, ".events.jsonl") {
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

// runFixtureReplay dispatches to the sidecar or transcript-only replay path
// based on whether a sibling .events.jsonl exists, mirroring how main()
// resolves inputs. Returns the indented JSON report.
func runFixtureReplay(t *testing.T, transcriptPath string) []byte {
	t.Helper()
	adapter, err := detectAdapter(transcriptPath)
	if err != nil {
		t.Fatalf("detectAdapter %s: %v", transcriptPath, err)
	}
	cfg := reportSettings{
		Adapter:            adapter,
		DebounceWindow:     2 * time.Second,
		FlickerMaxDuration: 10 * time.Second,
	}

	sidecar := strings.TrimSuffix(transcriptPath, ".jsonl") + ".events.jsonl"
	var report *replayReport
	if _, err := os.Stat(sidecar); err == nil {
		report, err = replayWithSidecar(transcriptPath, sidecar, cfg)
		if err != nil {
			t.Fatalf("replayWithSidecar %s: %v", transcriptPath, err)
		}
		check, err := runExtendedCheck(sidecar, report.Transitions)
		if err != nil {
			t.Fatalf("runExtendedCheck %s: %v", sidecar, err)
		}
		report.ExtendedCheck = check
	} else {
		report, err = replay(transcriptPath, cfg)
		if err != nil {
			t.Fatalf("replay %s: %v", transcriptPath, err)
		}
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		t.Fatalf("encode %s: %v", transcriptPath, err)
	}
	return buf.Bytes()
}

// normaliseReportJSON zeroes the generated_at timestamp so the golden is
// stable across runs. Round-trips through a generic map to avoid coupling
// the test to replayReport's JSON shape.
func normaliseReportJSON(t *testing.T, data []byte) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal for normalisation: %v", err)
	}
	if _, ok := m["generated_at"]; ok {
		m["generated_at"] = time.Time{}.Format(time.RFC3339Nano)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		t.Fatalf("re-encode normalised: %v", err)
	}
	return buf.Bytes()
}

// firstJSONDiff returns a short string pinpointing the first differing
// byte so the failure message is actionable without dumping both files.
func firstJSONDiff(a, b []byte) string {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return "first differing offset " + itoa(i) + ": " +
				snippet(a, i) + " vs " + snippet(b, i)
		}
	}
	if len(a) != len(b) {
		return "lengths differ: got " + itoa(len(a)) + " vs want " + itoa(len(b))
	}
	return "equal"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// snippet returns up to 32 bytes around offset i, single-quoted for
// readable test output.
func snippet(s []byte, i int) string {
	start := i - 16
	if start < 0 {
		start = 0
	}
	end := i + 16
	if end > len(s) {
		end = len(s)
	}
	return "'" + string(s[start:end]) + "'"
}
