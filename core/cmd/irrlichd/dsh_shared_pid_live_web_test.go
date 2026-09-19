package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type dshLiveWeb struct {
	cmd     *exec.Cmd
	exited  chan struct{}
	pid     int
	authURL string
	baseURL string
	client  *http.Client
}

type dshLiveWebConfig struct {
	bin, home, cwd, modelURL string
}

func startDSHLiveWeb(t *testing.T, cfg dshLiveWebConfig) *dshLiveWeb {
	t.Helper()
	patch := writeDSHLiveWebPatch(t, cfg.modelURL)
	logPath, logFile := openDSHLiveLog(t)
	cmd := exec.Command(cfg.bin, "--profile", "web", "--patch", patch, "--no-open", "--host", "127.0.0.1", "--port", "0")
	cmd.Dir = cfg.cwd
	cmd.Env = append(os.Environ(), "DSH_HOME="+cfg.home, "LMSTUDIO_API_KEY=fixture", "DSH_PERMISSION_MODE=danger-full-access")
	cmd.Stdout, cmd.Stderr = logFile, logFile
	web := runDSHLiveWeb(t, cmd, logFile)
	web.authURL = waitDSHLiveURL(t, logPath, web.exited)
	parsed, err := url.Parse(web.authURL)
	if err != nil {
		t.Fatal(err)
	}
	web.baseURL = parsed.Scheme + "://" + parsed.Host
	return web
}

func writeDSHLiveWebPatch(t *testing.T, modelURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "web.patch.yml")
	config := fmt.Sprintf("- id: llm-pi-ai\n  config:\n    providers:\n      lmstudio:\n        displayName: LM Studio\n        apiKeyEnv: LMSTUDIO_API_KEY\n        api: openai-completions\n        baseURL: %s/v1\n        models:\n          - id: fixture-model\n            name: fixture-model\n            contextWindow: 8192\n- id: agent-default-model\n  config:\n    provider: lmstudio\n    model: fixture-model\n", modelURL)
	if err := os.WriteFile(path, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func openDSHLiveLog(t *testing.T) (string, *os.File) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dsh-web.log")
	t.Cleanup(func() { reportDSHLiveLog(t, path) })
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, file
}

func reportDSHLiveLog(t *testing.T, path string) {
	t.Helper()
	if !t.Failed() {
		return
	}
	data, err := os.ReadFile(path)
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
}

func runDSHLiveWeb(t *testing.T, cmd *exec.Cmd, logFile *os.File) *dshLiveWeb {
	t.Helper()
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
	return web
}
func waitDSHLiveURL(t *testing.T, logPath string, exited <-chan struct{}) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var lastReadErr error
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(logPath)
		lastReadErr = err
		if authURL := dshLiveURLFromLog(data); err == nil && authURL != "" {
			return authURL
		}
		select {
		case <-exited:
			t.Fatal("DSH web exited before announcing its authenticated URL")
		default:
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastReadErr != nil {
		t.Fatalf("cannot read DSH web log within 20s: %v", lastReadErr)
	}
	t.Fatal("DSH web did not announce its authenticated URL within 20s")
	return ""
}

func dshLiveURLFromLog(data []byte) string {
	for _, line := range strings.Split(string(data), "\n") {
		url, ok := strings.CutPrefix(line, "dsh web: ")
		if !ok {
			continue
		}
		if fields := strings.Fields(url); len(fields) > 0 {
			return fields[0]
		}
	}
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

func (web *dshLiveWeb) rpc(t *testing.T, method string, request map[string]any) dshLiveRPCResult {
	t.Helper()
	rpcID := fmt.Sprintf("fixture-%d", time.Now().UnixNano())
	payload, err := json.Marshal(map[string]any{
		"type": "client-request", "rpcId": rpcID, "method": method,
		"payload": map[string]any{"args": map[string]any{"request": request}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, web.baseURL+"/api/"+method, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", web.baseURL)
	resp, err := web.client.Do(req)
	if err != nil {
		t.Fatalf("%s request: %v", method, err)
	}
	defer resp.Body.Close()
	var result dshLiveRPCResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("%s response: %v", method, err)
	}
	result.assert(t, method, rpcID, resp.StatusCode)
	return result
}

func (result dshLiveRPCResult) assert(t *testing.T, method, rpcID string, status int) {
	t.Helper()
	if status != http.StatusOK {
		t.Fatalf("%s returned status %d: %+v", method, status, result)
	}
	if result.RPCID != rpcID {
		t.Fatalf("%s returned RPC ID %q, want %q", method, result.RPCID, rpcID)
	}
	if !result.Result.OK {
		t.Fatalf("%s returned an error: %+v", method, result)
	}
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
