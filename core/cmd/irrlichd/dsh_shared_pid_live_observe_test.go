package main

import (
	"bufio"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"irrlicht/core/application/replayengine"
	"irrlicht/core/domain/lifecycle"
)

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
		if ok && row.State == state && row.PID > 0 && row.TranscriptPath != "" {
			return row
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s did not reach %s with PID and transcript within %s: rows=%v", sessionID, state, timeout, lastRows)
	return dshLiveRow{}
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
	f, err := os.Open(files[0])
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

func assertDSHLiveArc(t *testing.T, id string, events []lifecycle.Event) {
	t.Helper()
	working, readyAfterWorking, exited := false, false, 0
	for _, event := range events {
		switch event.Kind {
		case lifecycle.KindStateTransition:
			if event.NewState == "working" {
				working = true
			}
			if event.NewState == "ready" && working {
				readyAfterWorking = true
			}
		case lifecycle.KindProcessExited:
			exited++
		}
	}
	if !working || !readyAfterWorking || exited != 1 {
		t.Errorf("session %s: working=%v ready_after_working=%v process_exited=%d", id, working, readyAfterWorking, exited)
	}
}
