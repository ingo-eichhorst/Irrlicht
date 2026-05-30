package viewer

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// TestPlayback_concurrentControlsNoRace is the regression guard for issue
// #461 finding #5: Pause/Resume/SetSpeed write pb.Paused/pb.Speed and
// handleStatus/Snapshot read them — previously outside any lock. Run under
// `go test -race`; this fails loudly if the mutex contract regresses.
func TestPlayback_concurrentControlsNoRace(t *testing.T) {
	root := fixtureRoot(t)
	s := &Server{RepoRoot: root}
	h := s.Handler()

	body, _ := json.Marshal(map[string]any{
		"agent": "claudecode", "subtree": "scenarios", "scenario": "test",
		"mode": "viewer-internal", "speed": 1.0, // slow so the machine stays alive during the hammer
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/api/replay/start", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("start: status=%d body=%s", rr.Code, rr.Body)
	}

	// Endpoints that read/write the shared mutable playback state.
	routes := []struct {
		method, path string
	}{
		{"POST", "/api/replay/pause"},
		{"POST", "/api/replay/resume"},
		{"POST", "/api/replay/speed?speed=5.0"},
		{"GET", "/api/replay/status"},
		{"GET", "/api/v1/sessions"},
	}

	var wg sync.WaitGroup
	for i := 0; i < len(routes); i++ {
		for g := 0; g < 4; g++ {
			rt := routes[i]
			wg.Add(1)
			go func() {
				defer wg.Done()
				for n := 0; n < 25; n++ {
					w := httptest.NewRecorder()
					h.ServeHTTP(w, httptest.NewRequest(rt.method, rt.path, nil))
				}
			}()
		}
	}
	wg.Wait()

	// Stop concurrently with a final status read to exercise stopCurrent's
	// teardown against a reader.
	var stopWg sync.WaitGroup
	stopWg.Add(2)
	go func() {
		defer stopWg.Done()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/api/replay/stop", nil))
	}()
	go func() {
		defer stopWg.Done()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/api/replay/status", nil))
	}()
	stopWg.Wait()
}
