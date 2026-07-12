package services

import (
	"context"
	"errors"
	"time"

	"irrlicht/core/domain/backchannel"
	"irrlicht/core/ports/outbound"
)

// terminalObserveInterval is how often the observer captures each readable,
// consented session's screen. capture-pane renders a snapshot, so reading is
// inherently polled; 1s balances responsiveness against the cost of shelling
// out per controllable session (issue #732).
const terminalObserveInterval = 1 * time.Second

// uiEnqueuer receives edge-triggered terminal read-back signals and applies
// them on the session-detector's single writer goroutine. SessionDetector
// implements it.
type uiEnqueuer interface {
	EnqueueTerminalUISignal(sessionID string, ui backchannel.UIKind)
}

// TerminalObserver is the read counterpart to InputService: it captures the
// rendered terminal screen of the sessions Irrlicht can already control and
// folds transcript-invisible signals (today: the trust/permission dialog) into
// the session lifecycle as a complementary observation source. It does not
// replace the transcript/process observers — it only contributes signals they
// structurally cannot see.
//
// It reuses the backchannel gate chain exactly — master-toggle → per-adapter
// "control" consent → readable backend — so a disabled backchannel or an
// ungranted adapter is never read. Multiplexer/kitty-only: plain
// iTerm2/Terminal.app sessions are not readable and are silently skipped.
type TerminalObserver struct {
	repo    outbound.SessionRepository
	reader  outbound.TerminalReader
	consent consentGranter
	betaOn  func() bool
	sink    uiEnqueuer
	logger  outbound.Logger

	// lastUI tracks the last UI kind seen per session so only edges
	// (appear/clear) reach the event loop — one lifecycle record per dialog
	// appearance, not one per poll. Owned by the ticker goroutine; no lock.
	lastUI map[string]backchannel.UIKind
}

// NewTerminalObserver constructs a TerminalObserver. betaOn reports whether the
// backchannel master-toggle is on (default false), shared with InputService.
func NewTerminalObserver(repo outbound.SessionRepository, reader outbound.TerminalReader, consent consentGranter, betaOn func() bool, sink uiEnqueuer, logger outbound.Logger) *TerminalObserver {
	return &TerminalObserver{
		repo:    repo,
		reader:  reader,
		consent: consent,
		betaOn:  betaOn,
		sink:    sink,
		logger:  logger,
		lastUI:  make(map[string]backchannel.UIKind),
	}
}

// Run polls readable, consented sessions until ctx is cancelled.
func (o *TerminalObserver) Run(ctx context.Context) error {
	ticker := time.NewTicker(terminalObserveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			o.tick()
		}
	}
}

// tick captures every gated session once and forwards UI-state edges.
func (o *TerminalObserver) tick() {
	if !o.betaOn() {
		// Backchannel off: forget edge state so a re-enable detects cleanly
		// instead of suppressing a dialog that was already on screen.
		if len(o.lastUI) > 0 {
			o.lastUI = make(map[string]backchannel.UIKind)
		}
		return
	}
	states, err := o.repo.ListAll()
	if err != nil {
		o.logger.LogError("terminal-observer", "", err.Error())
		return
	}
	seen := make(map[string]bool, len(states))
	for _, st := range states {
		seen[st.SessionID] = true
		o.observe(st.SessionID, st.Adapter)
	}
	// Drop edge state for sessions that have gone away.
	for id := range o.lastUI {
		if !seen[id] {
			delete(o.lastUI, id)
		}
	}
}

// observe captures one session and forwards a signal only when its UI state
// changed since the last poll. The gate order mirrors InputService.resolve:
// consent, then readable backend. A single CaptureScreen does both the
// readability check (ErrNotReadable, returned without shelling out) and the
// capture, so the polling path loads the session from the repo only once.
func (o *TerminalObserver) observe(sessionID, adapter string) {
	if !o.consent.Granted(adapter, ControlPermissionKey) {
		return
	}
	screen, err := o.reader.CaptureScreen(sessionID)
	if errors.Is(err, outbound.ErrNotReadable) {
		return // not a multiplexer/kitty session — nothing to read
	}
	if err != nil {
		o.logger.LogError("terminal-observer", sessionID, err.Error())
		return
	}
	ui := backchannel.DetectUI(string(screen))
	if ui == o.lastUI[sessionID] {
		return // no edge
	}
	o.lastUI[sessionID] = ui
	o.sink.EnqueueTerminalUISignal(sessionID, ui)
}
