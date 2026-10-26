package main

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"irrlicht/core/adapters/inbound/agents/opencode"
)

// v2EventTypes are the event types opencode.Parser's 2.x branch emits
// (parser_v2.go parseV2Message: user → user_message, assistant →
// assistant_message, idle → turn_done).
var v2EventTypes = map[string]bool{"user_message": true, "assistant_message": true, "turn_done": true}

// TestOpenCodeV2ExportFeedsTheV2Parser exports a scratch 2.x store with the
// opencode-v2 driver's own export (replaydata/agents/opencode-v2/
// service-api.sh oc2_export_session) and parses every line with the replay's
// opencode parser (#2109). The store is built from the 2.1.4 DDL and rows the
// opencode adapter's own tests use (core/.../opencode/testdata), so the export
// is graded against the shape a real service writes:
//
//   - one line per session_message row, in seq order;
//   - every line carries `type`, `_cwd` (the session's directory) and `_ts`
//     (the row's time_updated) — the keys the daemon's reader adds
//     (metrics_v2.go buildV2MessageRaw);
//   - ParseLine takes the 2.x branch on every line;
//   - and the same line WITHOUT its `type` does not — it falls to the 1.x part
//     switch, which is what an export that dropped `$.type` would hand the
//     replay (the mutation fixture opencode-v2-rig-mutations_test.sh runs that
//     drop against this test).
func TestOpenCodeV2ExportFeedsTheV2Parser(t *testing.T) {
	for _, tool := range []string{"sqlite3", "bash"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Fatalf("%s is required and not on PATH — this test cannot run: %v", tool, err)
		}
	}
	root := mustAbs(t, filepath.Join("..", "..", "..", ".."))
	testdata := filepath.Join(root, "core", "adapters", "inbound", "agents", "opencode", "testdata")
	lib := filepath.Join(root, "replaydata", "agents", "opencode-v2", "service-api.sh")
	db := filepath.Join(t.TempDir(), "opencode.db")
	for _, f := range []string{"v2-schema.sql", "v2-store.rows.sql"} {
		sql, err := os.ReadFile(filepath.Join(testdata, f))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("sqlite3", db)
		cmd.Stdin = strings.NewReader(string(sql))
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("loading %s into the scratch store: %v: %s", f, err, out)
		}
	}

	sessions := sqliteLines(t, db, "SELECT id FROM session_v2 ORDER BY id")
	if len(sessions) == 0 {
		t.Fatal("the fixture loaded no session — this test graded nothing")
	}
	total := 0
	for _, sid := range sessions {
		t.Run(sid, func(t *testing.T) {
			want := sqliteLines(t, db, "SELECT type||'|'||time_updated FROM session_message WHERE session_id='"+sid+"' ORDER BY seq")
			dir := sqliteLines(t, db, "SELECT directory FROM session_v2 WHERE id='"+sid+"'")[0]
			out := filepath.Join(t.TempDir(), "transcript.jsonl")
			// #nosec G204 -- bash runs the repo's own export with fixture paths.
			cmd := exec.Command("bash", "-c", `set -euo pipefail; source "$1"; oc2_export_session "$2" "$3" "$4"`,
				"_", lib, db, sid, out)
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("oc2_export_session: %v: %s", err, b)
			}
			lines := readLines(t, out)
			if len(lines) != len(want) {
				t.Fatalf("exported %d line(s) for %d session_message row(s)", len(lines), len(want))
			}
			for i, line := range lines {
				var raw map[string]interface{}
				if err := json.Unmarshal([]byte(line), &raw); err != nil {
					t.Fatalf("line %d is not JSON: %v: %s", i+1, err, line)
				}
				typ, ts, _ := strings.Cut(want[i], "|")
				if raw["type"] != typ {
					t.Errorf("line %d: type = %v, want the row's %q", i+1, raw["type"], typ)
				}
				if raw["_cwd"] != dir {
					t.Errorf("line %d: _cwd = %v, want the session's directory %q", i+1, raw["_cwd"], dir)
				}
				if wantTS, _ := strconv.ParseFloat(ts, 64); raw["_ts"] != wantTS {
					t.Errorf("line %d: _ts = %v, want the row's time_updated %v", i+1, raw["_ts"], wantTS)
				}
				ev := (&opencode.Parser{}).ParseLine(raw)
				if ev == nil || !v2EventTypes[ev.EventType] {
					t.Errorf("line %d (%s): ParseLine did not take the 2.x branch (event %+v)", i+1, typ, ev)
				}
				delete(raw, "type")
				if ev := (&opencode.Parser{}).ParseLine(raw); ev != nil && v2EventTypes[ev.EventType] {
					t.Errorf("line %d without its type still parsed as 2.x (%s) — the check above "+
						"cannot tell an export that carries the type from one that drops it", i+1, ev.EventType)
				}
			}
			total += len(lines)
		})
	}
	if total == 0 {
		t.Fatal("no line was exported from any session — this test graded nothing")
	}
}

func sqliteLines(t *testing.T, db, query string) []string {
	t.Helper()
	out, err := exec.Command("sqlite3", "-readonly", db, query).CombinedOutput()
	if err != nil {
		t.Fatalf("sqlite3 %q: %v: %s", query, err, out)
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		if sc.Text() != "" {
			lines = append(lines, sc.Text())
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return lines
}
