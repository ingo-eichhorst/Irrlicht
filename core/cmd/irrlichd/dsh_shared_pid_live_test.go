package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/filesystem"
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
	t.Cleanup(func() { logDSHLiveArtifacts(t, dshHome, stateDir) })
	grants := permission.Set{"dsh": {"transcripts": permission.StateGranted}}
	if err := filesystem.NewPermissionStore(stateDir).Save(grants); err != nil {
		t.Fatal(err)
	}
	daemon := bootSmokeDaemonIn(t, buildIrrlichd(t), t.TempDir(), stateDir,
		"DSH_HOME="+dshHome, "IRRLICHT_RECORD=1")
	assertDSHLivePermission(t, daemon.addr)
	observer := dshLiveObserver{addr: daemon.addr}
	web := startDSHLiveWeb(t, dshLiveWebConfig{bin: dshBin, home: dshHome, cwd: projectDir, modelURL: model.URL})
	web.client = authenticateDSHLiveWeb(t, web.authURL)
	fixture := dshLiveFixture{web: web, observer: observer, cwd: projectDir}

	firstID, firstRow := fixture.createSession(t, "FIRST-SHARED-PID")
	secondID, secondRow := fixture.createSession(t, "SECOND-SHARED-PID")
	if secondID == firstID {
		t.Fatal("DSH web reused the first session ID")
	}
	firstRow = observer.waitRow(t, firstID, "ready", 10*time.Second)
	assertDSHLiveRow(t, firstRow, web.pid)
	if firstRow.TranscriptPath == secondRow.TranscriptPath {
		t.Fatalf("distinct DSH sessions resolved to one transcript: first=%+v second=%+v", firstRow, secondRow)
	}
	if modelCalls.Load() < 2 {
		t.Fatalf("mock model saw %d requests, want at least one per session", modelCalls.Load())
	}

	web.stop(t)
	observer.waitAbsent(t, firstID, 15*time.Second)
	observer.waitAbsent(t, secondID, 15*time.Second)
	daemon.shutdown(t)
	assertDSHLiveArcs(t, stateDir, firstID, secondID)
}

func logDSHLiveArtifacts(t *testing.T, dshHome, stateDir string) {
	t.Helper()
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
}

type dshLiveFixture struct {
	web      *dshLiveWeb
	observer dshLiveObserver
	cwd      string
}

func (fixture dshLiveFixture) createSession(t *testing.T, marker string) (string, dshLiveRow) {
	t.Helper()
	result := fixture.web.rpc(t, "session/create", map[string]any{"cwd": fixture.cwd})
	id := dshLiveSessionID(t, result)
	fixture.web.rpc(t, "session/prompt", dshLivePrompt(id, marker))
	row := fixture.observer.waitRow(t, id, "ready", 35*time.Second)
	assertDSHLiveRow(t, row, fixture.web.pid)
	waitDSHLiveTurnEnd(t, row.TranscriptPath, 35*time.Second)
	return id, row
}

func assertDSHLivePermission(t *testing.T, addr string) {
	t.Helper()
	snap := fetchPermissionsSnapshot(t, &http.Client{Timeout: 3 * time.Second},
		"http://"+addr+"/api/v1/permissions")
	for _, entry := range snap.Agents {
		if entry.Name == "dsh" && dshTranscriptGranted(entry) {
			return
		}
	}
	t.Fatalf("DeepSeek Harness transcript permission was not granted: %+v", snap.Agents)
}

func dshTranscriptGranted(entry permissionsSnapshotAgent) bool {
	for _, p := range entry.Permissions {
		if p.Key == "transcripts" && p.State == "granted" {
			return true
		}
	}
	return false
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
