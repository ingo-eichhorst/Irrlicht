// Package graceperiod implements a per-session idle timer that transitions
// sessions to "waiting" when no transcript activity occurs and no tool calls
// are open.
package graceperiod

import (
	"sync"
	"time"

	"irrlicht/core/pkg/tailer"
)

// WaitingHandler is called when the grace period fires and the session has
// no open tool calls — meaning it should transition to "waiting".
type WaitingHandler func(sessionID string)

// Timer manages per-session grace period timers.
// It implements outbound.GracePeriodTimer.
type Timer struct {
	delay   time.Duration
	handler WaitingHandler

	mu     sync.Mutex
	timers map[string]*sessionTimer // sessionID → active timer
}

// sessionTimer tracks the pending timer and the transcript path needed to
// compute metrics when the timer fires.
type sessionTimer struct {
	timer          *time.Timer
	transcriptPath string
}

// New creates a Timer with the given grace period delay and callback.
func New(delay time.Duration, handler WaitingHandler) *Timer {
	return &Timer{
		delay:   delay,
		handler: handler,
		timers:  make(map[string]*sessionTimer),
	}
}

// Reset restarts the grace period timer for a session. Called on each
// transcript activity event. Any previously pending timer for the same
// session is cancelled before starting a new one.
func (t *Timer) Reset(sessionID string, transcriptPath string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Cancel existing timer for this session.
	if st, ok := t.timers[sessionID]; ok {
		st.timer.Stop()
		delete(t.timers, sessionID)
	}

	timer := time.AfterFunc(t.delay, func() {
		t.onFire(sessionID, transcriptPath)
	})
	t.timers[sessionID] = &sessionTimer{
		timer:          timer,
		transcriptPath: transcriptPath,
	}
}

// Stop cancels the timer for a session.
func (t *Timer) Stop(sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if st, ok := t.timers[sessionID]; ok {
		st.timer.Stop()
		delete(t.timers, sessionID)
	}
}

// StopAll cancels all active timers.
func (t *Timer) StopAll() {
	t.mu.Lock()
	defer t.mu.Unlock()

	for id, st := range t.timers {
		st.timer.Stop()
		delete(t.timers, id)
	}
}

// onFire is called when the grace period timer expires. It tails the
// transcript to check hasOpenToolCall and, if false, invokes the handler.
func (t *Timer) onFire(sessionID string, transcriptPath string) {
	// Remove ourselves from the active timers map.
	t.mu.Lock()
	delete(t.timers, sessionID)
	t.mu.Unlock()

	// Tail the transcript to check open tool call state.
	tt := tailer.NewTranscriptTailer(transcriptPath)
	metrics, err := tt.TailAndProcess()
	if err != nil {
		// Cannot determine tool call state — err on the side of not
		// transitioning (stay working).
		return
	}

	if metrics != nil && !metrics.HasOpenToolCall {
		t.handler(sessionID)
	}
}

// ActiveCount returns the number of sessions with active timers (for testing).
func (t *Timer) ActiveCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.timers)
}
