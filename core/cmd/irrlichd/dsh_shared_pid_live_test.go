package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/application/replayengine"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/permission"
)

// Run with IRRLICHT_DSH_SHARED_PID_LIVE=1. The opt-in requires the installed
// dsh binary; the ordinary core suite does not claim to exercise that binary.
func TestDSHWebSharedPIDLive(t *testing.T) {
	if os.Getenv("IRRLICHT_DSH_SHARED_PID_LIVE") != "1" {
		t.Skip("set IRRLICHT_DSH_SHARED_PID_LIVE=1 for the installed DSH web fixture")
	}
	dshBin, err := exec.LookPath("dsh")
	if err != nil {
		t.Fatalf("live DSH fixture requires dsh: %v", err)
	}
	model, modelCalls := newDSHLiveModel(t)
	stateDir, dshHome, projectDir := t.TempDir(), t.TempDir(), t.TempDir()
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		_ = filepath.WalkDir(filepath.Join(dshHome, "sessions"), func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				t.Logf("DSH home walk: %v", err)
				return nil
			}
			if !entry.IsDir() {
				info, _ := entry.Info()
				t.Logf("DSH file: %s (%d bytes)", strings.TrimPrefix(path, dshHome), info.Size())
			}
			return nil
		})
		files, _ := filepath.Glob(filepath.Join(stateDir, "recordings", "*.jsonl"))
		for _, file := range files {
			data, _ := os.ReadFile(file)
			t.Logf("daemon recording: %d bytes", len(data))
		}
	})
	grants := permission.Set{"dsh": {"transcripts": permission.StateGranted}}
	if err := filesystem.NewPermissionStore(stateDir).Save(grants); err != nil {
		t.Fatal(err)
	}
	daemon := bootSmokeDaemonIn(t, buildIrrlichd(t), t.TempDir(), stateDir,
		"DSH_HOME="+dshHome, "IRRLICHT_RECORD=1")
	assertDSHLivePermission(t, daemon.addr)
	web := startDSHLiveWeb(t, dshBin, dshHome, projectDir, model.URL)
	client := authenticateDSHLiveWeb(t, web.authURL)

	first := dshLiveRPC(t, client, web.baseURL, "session/create", map[string]any{"cwd": projectDir})
	firstID := dshLiveSessionID(t, first)
	dshLiveRPC(t, client, web.baseURL, "session/prompt", dshLivePrompt(firstID, "FIRST-SHARED-PID"))
	firstRow := waitDSHLiveRow(t, daemon.addr, firstID, "ready", 35*time.Second)
	assertDSHLiveRow(t, firstRow, web.pid)
	waitDSHLiveTurnEnd(t, firstRow.TranscriptPath, 35*time.Second)

	second := dshLiveRPC(t, client, web.baseURL, "session/create", map[string]any{"cwd": projectDir})
	secondID := dshLiveSessionID(t, second)
	if secondID == firstID {
		t.Fatal("DSH web reused the first session ID")
	}
	dshLiveRPC(t, client, web.baseURL, "session/prompt", dshLivePrompt(secondID, "SECOND-SHARED-PID"))
	secondRow := waitDSHLiveRow(t, daemon.addr, secondID, "ready", 35*time.Second)
	assertDSHLiveRow(t, secondRow, web.pid)
	waitDSHLiveTurnEnd(t, secondRow.TranscriptPath, 35*time.Second)
	firstRow = waitDSHLiveRow(t, daemon.addr, firstID, "ready", 10*time.Second)
	assertDSHLiveRow(t, firstRow, web.pid)
	if firstRow.TranscriptPath == secondRow.TranscriptPath {
		t.Fatalf("distinct DSH sessions resolved to one transcript: first=%+v second=%+v", firstRow, secondRow)
	}
	if modelCalls.Load() < 2 {
		t.Fatalf("mock model saw %d requests, want at least one per session", modelCalls.Load())
	}

	web.stop(t)
	waitDSHLiveAbsent(t, daemon.addr, firstID, 15*time.Second)
	waitDSHLiveAbsent(t, daemon.addr, secondID, 15*time.Second)
	daemon.shutdown(t)
	assertDSHLiveArcs(t, stateDir, firstID, secondID)
}

func assertDSHLivePermission(t *testing.T, addr string) {
	t.Helper()
	snap := fetchPermissionsSnapshot(t, &http.Client{Timeout: 3 * time.Second},
		"http://"+addr+"/api/v1/permissions")
	for _, entry := range snap.Agents {
		if entry.Name != "dsh" {
			continue
		}
		for _, p := range entry.Permissions {
			if p.Key == "transcripts" && p.State == "granted" {
				return
			}
		}
	}
	t.Fatalf("DeepSeek Harness transcript permission was not granted: %+v", snap.Agents)
}

func newDSHLiveModel(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	calls := new(atomic.Int64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"object":"list","data":[{"id":"fixture-model","object":"model"}]}`)
		case "/v1/chat/completions":
			calls.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"fixture-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"\"},\"finish_reason\":null}]}\n\n")
			if flush, ok := w.(http.Flusher); ok {
				flush.Flush()
			}
			_, _ = io.WriteString(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"fixture-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":null}]}\n\n")
			_, _ = io.WriteString(w, "data: {\"id\":\"fixture\",\"object\":\"chat.completion.chunk\",\"created\":0,\"model\":\"fixture-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":1,\"total_tokens\":13}}\n\n")
			_, _ = io.WriteString(w, "data: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, calls
}

type dshLiveWeb struct {
	cmd     *exec.Cmd
	exited  chan struct{}
	pid     int
	authURL string
	baseURL string
}

func startDSHLiveWeb(t *testing.T, bin, dshHome, cwd, modelURL string) *dshLiveWeb {
	t.Helper()
	patch := filepath.Join(t.TempDir(), "web.patch.yml")
	config := fmt.Sprintf("- id: llm-pi-ai\n  config:\n    providers:\n      lmstudio:\n        displayName: LM Studio\n        apiKeyEnv: LMSTUDIO_API_KEY\n        api: openai-completions\n        baseURL: %s/v1\n        models:\n          - id: fixture-model\n            name: fixture-model\n            contextWindow: 8192\n- id: agent-default-model\n  config:\n    provider: lmstudio\n    model: fixture-model\n", modelURL)
	if err := os.WriteFile(patch, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(t.TempDir(), "dsh-web.log")
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		data, err := os.ReadFile(logPath)
		if err != nil {
			t.Logf("read DSH web log: %v", err)
			return
		}
		if len(data) > 6000 {
			data = data[len(data)-6000:]
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "dsh web: ") {
				t.Log("DSH web announced an authenticated URL (token redacted)")
				continue
			}
			if line != "" {
				t.Logf("DSH web: %s", line)
			}
		}
	})
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "--profile", "web", "--patch", patch, "--no-open", "--host", "127.0.0.1", "--port", "0")
	cmd.Dir = cwd
	cmd.Env = append(os.Environ(), "DSH_HOME="+dshHome, "LMSTUDIO_API_KEY=fixture", "DSH_PERMISSION_MODE=danger-full-access")
	cmd.Stdout, cmd.Stderr = logFile, logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatal(err)
	}
	web := &dshLiveWeb{cmd: cmd, exited: make(chan struct{}), pid: cmd.Process.Pid}
	go func() { _ = cmd.Wait(); close(web.exited); _ = logFile.Close() }()
	t.Cleanup(func() {
		select {
		case <-web.exited:
		default:
			_ = cmd.Process.Kill()
			<-web.exited
		}
	})
	web.authURL = waitDSHLiveURL(t, logPath, web.exited)
	parsed, err := url.Parse(web.authURL)
	if err != nil {
		t.Fatal(err)
	}
	web.baseURL = parsed.Scheme + "://" + parsed.Host
	return web
}

func waitDSHLiveURL(t *testing.T, logPath string, exited <-chan struct{}) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(logPath)
		if err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if url, ok := strings.CutPrefix(line, "dsh web: "); ok {
					if fields := strings.Fields(url); len(fields) > 0 {
						return fields[0]
					}
				}
			}
		}
		select {
		case <-exited:
			t.Fatal("DSH web exited before announcing its authenticated URL")
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("DSH web did not announce its authenticated URL within 20s")
	return ""
}

func authenticateDSHLiveWeb(t *testing.T, authURL string) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar, Timeout: 5 * time.Second}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(authURL)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return client
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("DSH web auth URL did not serve a 200 response within 10s")
	return nil
}

type dshLiveRPCResult struct {
	Type   string `json:"type"`
	RPCID  string `json:"rpcId"`
	Result struct {
		OK    bool `json:"ok"`
		Value struct {
			SessionID string `json:"sessionId"`
		} `json:"value"`
		Error json.RawMessage `json:"error"`
	} `json:"result"`
}

func dshLiveRPC(t *testing.T, client *http.Client, baseURL, method string, request map[string]any) dshLiveRPCResult {
	t.Helper()
	rpcID := fmt.Sprintf("fixture-%d", time.Now().UnixNano())
	payload, err := json.Marshal(map[string]any{
		"type": "client-request", "rpcId": rpcID, "method": method,
		"payload": map[string]any{"args": map[string]any{"request": request}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/api/"+method, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", baseURL)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s request: %v", method, err)
	}
	defer resp.Body.Close()
	var result dshLiveRPCResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("%s response: %v", method, err)
	}
	if resp.StatusCode != http.StatusOK || result.RPCID != rpcID || !result.Result.OK {
		t.Fatalf("%s failed: status=%d response=%+v", method, resp.StatusCode, result)
	}
	return result
}

func dshLiveSessionID(t *testing.T, result dshLiveRPCResult) string {
	t.Helper()
	if !strings.HasPrefix(result.Result.Value.SessionID, "session-") {
		t.Fatalf("invalid DSH session ID: %q", result.Result.Value.SessionID)
	}
	return result.Result.Value.SessionID
}

func dshLivePrompt(sessionID, marker string) map[string]any {
	return map[string]any{
		"requestId": fmt.Sprintf("prompt-%d", time.Now().UnixNano()),
		"sessionId": sessionID, "mode": "queue",
		"content": []any{map[string]string{"type": "text", "text": "Reply with ok. " + marker}},
	}
}

type dshLiveRow struct {
	SessionID      string `json:"session_id"`
	State          string `json:"state"`
	PID            int    `json:"pid"`
	TranscriptPath string `json:"transcript_path"`
}

func dshLiveRows(t *testing.T, addr string) map[string]dshLiveRow {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + addr + "/api/v1/sessions")
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
		if id, ok := value["session_id"].(string); ok {
			state, _ := value["state"].(string)
			path, _ := value["transcript_path"].(string)
			pid, _ := value["pid"].(float64)
			if state != "" && path != "" {
				rows[id] = dshLiveRow{SessionID: id, State: state, PID: int(pid), TranscriptPath: path}
			}
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

func waitDSHLiveRow(t *testing.T, addr, sessionID, state string, timeout time.Duration) dshLiveRow {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastRows map[string]dshLiveRow
	for time.Now().Before(deadline) {
		lastRows = dshLiveRows(t, addr)
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
		complete := false
		lastErr = replayengine.ScanTranscriptLines(transcriptPath, func(line []byte) error {
			var record struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal(line, &record); err != nil {
				return err
			}
			if record.Type == "turn/end" {
				complete = true
			}
			return nil
		})
		if lastErr == nil && complete {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("transcript %s had no completed turn within %s (last scan: %v)", transcriptPath, timeout, lastErr)
}

func waitDSHLiveAbsent(t *testing.T, addr, sessionID string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, ok := dshLiveRows(t, addr)[sessionID]; !ok {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("session %s remained after web process exit for %s", sessionID, timeout)
}

func (web *dshLiveWeb) stop(t *testing.T) {
	t.Helper()
	if err := web.cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	select {
	case <-web.exited:
	case <-time.After(8 * time.Second):
		t.Fatal("DSH web did not stop within 8s")
	}
}

func assertDSHLiveArcs(t *testing.T, stateDir string, sessionIDs ...string) {
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
	for _, id := range sessionIDs {
		working, readyAfterWorking, exited := false, false, 0
		for _, event := range events[id] {
			if event.Kind == lifecycle.KindStateTransition && event.NewState == "working" {
				working = true
			}
			if event.Kind == lifecycle.KindStateTransition && event.NewState == "ready" && working {
				readyAfterWorking = true
			}
			if event.Kind == lifecycle.KindProcessExited {
				exited++
			}
		}
		if !working || !readyAfterWorking || exited != 1 {
			t.Errorf("session %s: working=%v ready_after_working=%v process_exited=%d", id, working, readyAfterWorking, exited)
		}
	}
}
