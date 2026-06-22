package main

import (
	"context"
	"os/exec"
	"sync"
	"testing"
	"time"

	"irrlicht/core/adapters/outbound/control"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/backchannel"
	"irrlicht/core/domain/session"
)

// This is the backchannel-observe e2e (issue #732, Phase 3): it drives the REAL
// read-back stack against a REAL tmux pane — control.Reader (capture-pane) →
// backchannel.DetectUI → TerminalObserver — and proves a transcript-invisible
// signal (the trust/permission dialog) is observed off the rendered terminal.
// The fusion that turns that signal into a `waiting` state is exercised by the
// TerminalObserver internal tests; here we prove the part only a live backend
// can: real tmux output is captured and recognized. tmux is the one terminal
// environment automatable headlessly; kitty shares the same Reader seam,
// covered by unit tests + the onboarding assessment. Skips when tmux is absent.

// captureSink records the UI signals the observer forwards. Concurrency-safe:
// the observer's ticker runs on its own goroutine while the test reads.
type captureSink struct {
	mu   sync.Mutex
	kind backchannel.UIKind
	got  bool
}

func (s *captureSink) EnqueueTerminalUISignal(_ string, ui backchannel.UIKind) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.kind, s.got = ui, true
}
func (s *captureSink) last() (backchannel.UIKind, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.kind, s.got
}

// TestBackchannelObserveE2E_Local renders a trust-dialog line in a real tmux
// pane and asserts the real Reader+observer pipeline recognizes it.
func TestBackchannelObserveE2E_Local(t *testing.T) {
	tmuxOK(t)
	paneID, socket := startCatPane(t)

	repo := &e2eRepo{state: &session.SessionState{
		SessionID: "e2e",
		Adapter:   "claude-code",
		State:     session.StateWorking,
		Launcher:  &session.Launcher{TmuxPane: paneID, TmuxSocket: socket},
	}}

	// Sanity: the tmux-hosted session is readable via the real backend.
	reader := control.NewReader(repo, e2eLog{})
	if !reader.Readable("e2e") {
		t.Fatal("tmux-hosted session should be readable")
	}

	sink := &captureSink{}
	obs := services.NewTerminalObserver(repo, reader, allowConsent{}, func() bool { return true }, sink, e2eLog{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = obs.Run(ctx) }()

	// cat echoes each submitted line, so this renders the dialog in the pane —
	// exactly the screen a real agent's permission prompt would present.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel2()
	if out, err := exec.CommandContext(ctx2, "tmux", "-S", socket,
		"send-keys", "-t", paneID, "-l", "Do you want to proceed?\r").CombinedOutput(); err != nil {
		t.Fatalf("send-keys: %v: %s", err, out)
	}

	// The observer polls on a ~1s ticker; give it a few cycles to capture and
	// recognize the rendered dialog.
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if kind, got := sink.last(); got {
			if kind != backchannel.UIKindTrustDialog {
				t.Fatalf("observed UI = %q, want trust_dialog", kind)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("observer never recognized the trust dialog rendered in the pane")
}
