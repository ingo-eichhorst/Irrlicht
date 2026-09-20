package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"

	"irrlicht/core/application/replayengine"
	"irrlicht/core/domain/lifecycle"
)

func TestDSHBackgroundReleaseFollowsTerminalNotice(t *testing.T) {
	const sessionID = "session-dbb71762-2da1-4a14-a40c-b57ffb8895d9"
	path := filepath.Join("..", "..", "..", "replaydata", "agents", "deepseek-harness", "scenarios",
		"3-3_background-process", "recordings", "2026-09-20-21-35-33_irrlichd-0.6.4+ec736f3.dirty")
	terminal := dshTerminalNoticeTime(t, filepath.Join(path, "transcript.jsonl.zstd"), "bash-1")
	events := readDSHLiveEventsFile(t, filepath.Join(path, "events.jsonl"))[sessionID]
	if len(events) == 0 {
		t.Fatalf("events for %s not found", sessionID)
	}
	working := time.Time{}
	readyAfterNotice := time.Time{}
	for _, event := range events {
		if event.NewState == "working" {
			working = event.Timestamp
		}
		if !working.IsZero() && event.NewState == "ready" && event.PrevState == "working" {
			if event.Timestamp.Before(terminal) {
				t.Fatalf("background session became ready at %s before terminal notice at %s", event.Timestamp, terminal)
			}
			readyAfterNotice = event.Timestamp
		}
	}
	if working.IsZero() {
		t.Fatal("working transition not found")
	}
	if readyAfterNotice.IsZero() {
		t.Fatal("ready transition after terminal notice not found")
	}
}

func dshTerminalNoticeTime(t *testing.T, path, bashID string) time.Time {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder, err := zstd.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer decoder.Close()
	scanner := bufio.NewScanner(decoder)
	for scanner.Scan() {
		var record struct {
			Type string `json:"type"`
			Time int64  `json:"time"`
			Data struct {
				Source struct {
					Kind   string `json:"kind"`
					Plugin string `json:"plugin"`
					Form   string `json:"form"`
				} `json:"source"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"data"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		if record.Type == "user/message" && record.Data.Source.Kind == "plugin" && record.Data.Source.Plugin == "tool-jobs" && record.Data.Source.Form == "notice" && len(record.Data.Content) == 1 && strings.Contains(record.Data.Content[0].Text, "background job "+bashID+" ") && strings.Contains(record.Data.Content[0].Text, "finished [status: completed") {
			return time.UnixMilli(record.Time)
		}
	}
	if err := scanner.Err(); err != nil && err != io.EOF {
		t.Fatal(err)
	}
	t.Fatal("terminal tool-jobs notice not found")
	return time.Time{}
}

type dshLiveRow struct {
	SessionID      string `json:"session_id"`
	State          string `json:"state"`
	PID            int    `json:"pid"`
	TranscriptPath string `json:"transcript_path"`
}

type dshLiveObserver struct{ addr string }

func (observer dshLiveObserver) rows(t *testing.T) map[string]dshLiveRow {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + observer.addr + "/api/v1/sessions")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sessions API returned %d", resp.StatusCode)
	}
	var tree any
	if err := json.NewDecoder(resp.Body).Decode(&tree); err != nil {
		t.Fatal(err)
	}
	rows := make(map[string]dshLiveRow)
	collectDSHLiveRows(tree, rows)
	return rows
}

func collectDSHLiveRows(node any, rows map[string]dshLiveRow) {
	switch value := node.(type) {
	case map[string]any:
		if row, ok := dshLiveRowFromMap(value); ok {
			rows[row.SessionID] = row
		}
		for _, child := range value {
			collectDSHLiveRows(child, rows)
		}
	case []any:
		for _, child := range value {
			collectDSHLiveRows(child, rows)
		}
	}
}

func dshLiveRowFromMap(value map[string]any) (dshLiveRow, bool) {
	id, _ := value["session_id"].(string)
	state, _ := value["state"].(string)
	path, _ := value["transcript_path"].(string)
	pid, _ := value["pid"].(float64)
	row := dshLiveRow{SessionID: id, State: state, PID: int(pid), TranscriptPath: path}
	return row, id != "" && state != "" && path != ""
}

func (observer dshLiveObserver) waitRow(t *testing.T, sessionID, state string, timeout time.Duration) dshLiveRow {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastRows map[string]dshLiveRow
	for time.Now().Before(deadline) {
		lastRows = observer.rows(t)
		row, ok := lastRows[sessionID]
		if ok && dshLiveRowReached(row, state) {
			return row
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s did not reach %s with PID and transcript within %s: rows=%v", sessionID, state, timeout, lastRows)
	return dshLiveRow{}
}

func dshLiveRowReached(row dshLiveRow, state string) bool {
	if row.State != state {
		return false
	}
	if row.PID <= 0 {
		return false
	}
	return row.TranscriptPath != ""
}

func assertDSHLiveRow(t *testing.T, row dshLiveRow, pid int) {
	t.Helper()
	if row.PID != pid {
		t.Fatalf("session %s PID=%d, web PID=%d", row.SessionID, row.PID, pid)
	}
	if _, err := os.Stat(row.TranscriptPath); err != nil {
		t.Fatalf("session %s transcript: %v", row.SessionID, err)
	}
}

func waitDSHLiveTurnEnd(t *testing.T, transcriptPath string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		complete, err := dshLiveTurnEnded(transcriptPath)
		lastErr = err
		if lastErr == nil && complete {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("transcript %s had no completed turn within %s (last scan: %v)", transcriptPath, timeout, lastErr)
}

func dshLiveTurnEnded(transcriptPath string) (bool, error) {
	complete := false
	err := replayengine.ScanTranscriptLines(transcriptPath, func(line []byte) error {
		var record struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &record); err != nil {
			return err
		}
		complete = complete || record.Type == "turn/end"
		return nil
	})
	return complete, err
}

func (observer dshLiveObserver) waitAbsent(t *testing.T, sessionID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, ok := observer.rows(t)[sessionID]; !ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s remained after web process exit for %s", sessionID, timeout)
}

func assertDSHLiveArcs(t *testing.T, stateDir string, sessionIDs ...string) {
	t.Helper()
	events := readDSHLiveEvents(t, stateDir)
	for _, id := range sessionIDs {
		assertDSHLiveArc(t, id, events[id])
	}
}

func readDSHLiveEvents(t *testing.T, stateDir string) map[string][]lifecycle.Event {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(stateDir, "recordings", "*.jsonl"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one daemon event recording: files=%v err=%v", files, err)
	}
	return readDSHLiveEventsFile(t, files[0])
}

func readDSHLiveEventsFile(t *testing.T, path string) map[string][]lifecycle.Event {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	events := make(map[string][]lifecycle.Event)
	scan := bufio.NewScanner(f)
	for scan.Scan() {
		var event lifecycle.Event
		if err := json.Unmarshal(scan.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events[event.SessionID] = append(events[event.SessionID], event)
	}
	if err := scan.Err(); err != nil {
		t.Fatal(err)
	}
	return events
}

func TestDSHLiveArcRejectsRecordedCrossSessionIdentity(t *testing.T) {
	const sessionID = "session-9e1e0760-a587-4886-a855-abd971c22838"
	path := filepath.Join("..", "..", "..", "replaydata", "agents", "deepseek-harness", "scenarios",
		"4-1_multiple-sessions-same-cwd", "recordings", "2026-09-18-15-43-18_irrlichd-0.6.4+da0e1be", "events.jsonl")
	arc := summarizeDSHLiveArc(readDSHLiveEventsFile(t, path)[sessionID])
	if arc.complete(sessionID) {
		t.Fatalf("recorded identity failure passed: %+v", arc)
	}
	if arc.working != 2 || arc.readyAfterWorking != 2 {
		t.Fatalf("recorded second state arc = working:%d ready-after-working:%d, want 2/2", arc.working, arc.readyAfterWorking)
	}
	if len(arc.removedPaths) != 2 || filepath.Base(filepath.Dir(arc.removedPaths[1])) == sessionID {
		t.Fatalf("recorded cross-session teardown path = %v", arc.removedPaths)
	}
}

func TestDSHLiveArcRejectsRecordedForkHandoff(t *testing.T) {
	const sessionID = "session-6875581b-de0a-4978-8182-3854f1ce6002"
	path := filepath.Join("..", "..", "..", "replaydata", "agents", "deepseek-harness", "scenarios",
		"1-6_checkpoint-rewind", "recordings", "2026-09-19-00-56-13_irrlichd-0.6.4+5b3ddbd", "events.jsonl")
	arc := summarizeDSHLiveArc(readDSHLiveEventsFile(t, path)[sessionID])
	if arc.complete(sessionID) {
		t.Fatalf("recorded fork handoff failure passed: %+v", arc)
	}
	if arc.working != 0 || arc.readyAfterWorking != 0 {
		t.Fatalf("recorded child working window = working:%d ready-after-working:%d, want 0/0", arc.working, arc.readyAfterWorking)
	}
	if len(arc.removedPaths) != 3 || filepath.Base(filepath.Dir(arc.removedPaths[1])) == sessionID {
		t.Fatalf("recorded fork teardown path = %v", arc.removedPaths)
	}
}

func assertDSHLiveArc(t *testing.T, id string, events []lifecycle.Event) {
	t.Helper()
	arc := summarizeDSHLiveArc(events)
	if arc.complete(id) {
		return
	}
	t.Errorf("session %s: working_transitions=%d ready_after_working=%d process_exited=%d removed_paths=%v", id, arc.working, arc.readyAfterWorking, arc.exited, arc.removedPaths)
}

type dshLiveArc struct {
	working, readyAfterWorking, exited int
	removedPaths                       []string
}

func (arc dshLiveArc) complete(id string) bool {
	if arc.working != 1 || arc.readyAfterWorking != 1 || arc.exited != 1 || len(arc.removedPaths) == 0 {
		return false
	}
	for _, path := range arc.removedPaths {
		if path == "" || filepath.Base(filepath.Dir(path)) != id {
			return false
		}
	}
	return true
}

func summarizeDSHLiveArc(events []lifecycle.Event) dshLiveArc {
	arc := dshLiveArc{}
	for _, event := range events {
		switch event.Kind {
		case lifecycle.KindStateTransition:
			if event.NewState == "working" {
				arc.working++
			}
			if event.NewState == "ready" && arc.working > 0 {
				arc.readyAfterWorking++
			}
		case lifecycle.KindProcessExited:
			arc.exited++
		case lifecycle.KindTranscriptRemoved:
			arc.removedPaths = append(arc.removedPaths, event.TranscriptPath)
		}
	}
	return arc
}
