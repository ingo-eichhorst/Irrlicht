package services

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// gatedLoadRepo is an in-memory SessionRepository whose next Load of one
// session, once armed, signals `loaded` and then blocks until `proceed`
// closes — holding a copy taken before the block, exactly as a real pass
// holds a copy it read before a concurrent writer ran.
type gatedLoadRepo struct {
	mu      sync.Mutex
	states  map[string][]byte
	gateID  string
	loaded  chan struct{}
	proceed chan struct{}
}

func newGatedLoadRepo() *gatedLoadRepo {
	return &gatedLoadRepo{states: map[string][]byte{}, loaded: make(chan struct{}), proceed: make(chan struct{})}
}

func (r *gatedLoadRepo) arm(id string) {
	r.mu.Lock()
	r.gateID = id
	r.mu.Unlock()
}

func (r *gatedLoadRepo) Load(id string) (*session.SessionState, error) {
	r.mu.Lock()
	data, ok := r.states[id]
	gated := ok && r.gateID == id
	if gated {
		r.gateID = ""
	}
	r.mu.Unlock()
	if !ok {
		return nil, errors.New("not found")
	}
	var s session.SessionState
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if gated {
		close(r.loaded)
		<-r.proceed
	}
	return &s, nil
}

func (r *gatedLoadRepo) Save(s *session.SessionState) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.states[s.SessionID] = data
	r.mu.Unlock()
	return nil
}

func (r *gatedLoadRepo) Delete(id string) error {
	r.mu.Lock()
	delete(r.states, id)
	r.mu.Unlock()
	return nil
}

func (r *gatedLoadRepo) ListAll() ([]*session.SessionState, error) { return nil, nil }

type lostUpdateGit struct{ bornGit }

func (lostUpdateGit) GetCWDFromTranscript(string) string { return "" }

func waitClosed(t *testing.T, ch <-chan struct{}, d time.Duration, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(d):
		t.Fatalf("%s did not happen within %s", what, d)
	}
}

// TestProcessActivity_NeverResurrectsAClearedProviderSnapshot is the #2161
// QA finding: after the Z.ai permission was revoked, a session's zai reading
// survived the sweep tick that cleared it in 2 of 3 live trials and went
// only on the next tick.
//
// The mechanism this reproduces: processActivity loaded the session BEFORE
// taking the session-state lock that SetProviderRateLimit writes under. A
// transcript/refresh pass that loaded its copy just before the sweep's clear
// therefore saved that copy — reading included — just after it, and the
// clear was lost until the next sweep tick re-applied it.
func TestProcessActivity_NeverResurrectsAClearedProviderSnapshot(t *testing.T) {
	repo := newGatedLoadRepo()
	_ = repo.Save(&session.SessionState{
		SessionID: "z1",
		Adapter:   "claude-code",
		State:     session.StateReady,
		PID:       os.Getpid(),
		Metrics: &session.SessionMetrics{RateLimit: &session.RateLimitSnapshot{
			Provider:           session.ProviderZai,
			AttributionQuality: session.AttributionQualityConfirmed,
			Windows:            []session.RateLimitWindow{{UsedPercent: 1, WindowMinutes: 300, ResetsAt: 1795284966}},
		}},
	})
	d := NewSessionDetector(nil, SessionDetectorDeps{Repo: repo, Log: bornLog{}, Git: lostUpdateGit{}, Metrics: emptyMetrics{}})

	repo.arm("z1")
	passDone := make(chan struct{})
	go func() {
		d.processActivity(agent.Identity{}, agent.Event{SessionID: "z1"})
		close(passDone)
	}()
	waitClosed(t, repo.loaded, 5*time.Second, "the pass's load")

	// The revoke's clear lands while the pass holds its copy.
	clearDone := make(chan struct{})
	go func() {
		d.SetProviderRateLimit("z1", session.ProviderZai, nil)
		close(clearDone)
	}()
	// Give the clear every chance to finish first; it can only finish here
	// if the pass is not holding the session-state lock.
	select {
	case <-clearDone:
	case <-time.After(500 * time.Millisecond):
	}
	close(repo.proceed)
	waitClosed(t, passDone, 5*time.Second, "the pass")
	waitClosed(t, clearDone, 5*time.Second, "the clear")

	got, err := repo.Load("z1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Metrics != nil && got.Metrics.RateLimit != nil && got.Metrics.RateLimit.Provider == session.ProviderZai {
		t.Fatal("the zai reading is back after its clear: a pass that loaded the session before " +
			"SetProviderRateLimit(nil) saved its older copy over the clear")
	}
}

var _ outbound.SessionRepository = (*gatedLoadRepo)(nil)
