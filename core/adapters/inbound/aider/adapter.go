// Package aider implements the inbound adapter for Aider AI pair programmer sessions.
//
// Aider has no hook system like Claude Code. Instead, it exposes two usable signals:
//  1. Analytics log (AIDER_ANALYTICS_LOG) — JSONL file, append-only, written during the session.
//  2. Notifications command (AIDER_NOTIFICATIONS_COMMAND) — shell command executed when aider
//     transitions to waiting-for-user-input state.
//
// The recommended integration uses a wrapper script (irrlicht-aider) that:
//   - Sends a synthetic SessionStart to irrlicht-hook before launching aider
//   - Starts irrlicht-aider-tail in the background to tail the analytics log
//   - Sets AIDER_NOTIFICATIONS_COMMAND to irrlicht-aider-notify for reliable waiting state
//   - Sends a synthetic SessionEnd to irrlicht-hook after aider exits
//
// State machine mapping (per specs/aider-adapter.md §4):
//
//	cli session       → Notification (waiting — aider is showing its prompt)
//	message_send_starting → UserPromptSubmit (working — LLM call starting)
//	ai-comments execute   → UserPromptSubmit (working — watch-files triggered a prompt)
//	message_send      → schedule 200ms fallback Notification (LLM call finished)
//	exit              → SessionEnd (session ending)
//	everything else   → ignored
//
// The 200ms timer after message_send is a safety net for when AIDER_NOTIFICATIONS_COMMAND
// is not configured. When the notification command fires first, it delivers a Notification
// event via irrlicht-aider-notify before the timer expires, and the timer is cancelled.
package aider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"irrlicht/core/domain/event"
	"irrlicht/core/ports/inbound"
)

// AnalyticsEvent represents a single line in the Aider analytics JSONL log.
type AnalyticsEvent struct {
	Event      string                 `json:"event"`
	Properties map[string]interface{} `json:"properties"`
	UserID     string                 `json:"user_id"`
	Time       int64                  `json:"time"`
}

// WaitTimerDelay is how long after a message_send event before a fallback
// Notification is emitted. Exported for test overrides.
var WaitTimerDelay = 200 * time.Millisecond

// Adapter processes Aider analytics events and converts them to HookEvents.
// It maintains session context (sessionID, cwd, model) that persists across
// multiple analytics events during a single aider session.
type Adapter struct {
	handler   inbound.EventHandler
	sessionID string
	cwd       string

	mu        sync.Mutex
	model     string       // set from cli_session event properties
	waitTimer *time.Timer  // fallback timer: fires Notification after message_send
}

// New returns an Adapter configured with the given handler, session ID, and working directory.
func New(handler inbound.EventHandler, sessionID, cwd string) *Adapter {
	return &Adapter{
		handler:   handler,
		sessionID: sessionID,
		cwd:       cwd,
	}
}

// ProcessEvent converts a single Aider analytics event to a HookEvent and calls the handler.
// Returns nil if the event should be silently ignored.
func (a *Adapter) ProcessEvent(evt *AnalyticsEvent) error {
	he, err := a.translate(evt)
	if err != nil {
		return err
	}
	if he == nil {
		return nil // silently ignored
	}
	return a.handler.HandleEvent(he)
}

// translate maps an Aider analytics event to a HookEvent.
// Returns (nil, nil) for events that should be silently ignored.
func (a *Adapter) translate(evt *AnalyticsEvent) (*event.HookEvent, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	switch evt.Event {
	case "launched":
		// The wrapper already sent SessionStart; skip.
		return nil, nil

	case "cli session":
		// Extract model name from properties.
		a.mu.Lock()
		if m, ok := evt.Properties["main_model"].(string); ok {
			a.model = m
		}
		model := a.model
		a.mu.Unlock()

		// Aider is now showing its prompt — transition to waiting.
		he := &event.HookEvent{
			HookEventName: "Notification",
			SessionID:     a.sessionID,
			Timestamp:     now,
			CWD:           a.cwd,
			Adapter:       "aider",
		}
		if model != "" {
			he.Model = model
		}
		return he, nil

	case "message_send_starting", "ai-comments execute":
		// Cancel any pending fallback-waiting timer.
		a.cancelWaitTimer()

		return &event.HookEvent{
			HookEventName: "UserPromptSubmit",
			SessionID:     a.sessionID,
			Timestamp:     now,
			CWD:           a.cwd,
			Adapter:       "aider",
		}, nil

	case "message_send":
		// LLM call finished. Schedule a fallback Notification after WaitTimerDelay.
		// If AIDER_NOTIFICATIONS_COMMAND fires first, it will deliver the Notification
		// event via irrlicht-aider-notify (which cancels this timer via a separate process).
		// Within this process, a subsequent message_send_starting will cancel this timer.
		a.scheduleWaitTimer()
		return nil, nil

	case "exit":
		a.cancelWaitTimer()
		return &event.HookEvent{
			HookEventName: "SessionEnd",
			SessionID:     a.sessionID,
			Timestamp:     now,
			CWD:           a.cwd,
			Adapter:       "aider",
		}, nil

	default:
		// command_*, repo, no-repo, auto_commits, model warning, etc.
		return nil, nil
	}
}

// scheduleWaitTimer starts a timer that emits a Notification event after WaitTimerDelay.
// Any existing timer is cancelled first.
func (a *Adapter) scheduleWaitTimer() {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.waitTimer != nil {
		a.waitTimer.Stop()
	}
	sessionID := a.sessionID
	cwd := a.cwd
	a.waitTimer = time.AfterFunc(WaitTimerDelay, func() {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = a.handler.HandleEvent(&event.HookEvent{
			HookEventName: "Notification",
			SessionID:     sessionID,
			Timestamp:     now,
			CWD:           cwd,
			Adapter:       "aider",
		})
	})
}

// cancelWaitTimer stops any pending fallback wait timer.
func (a *Adapter) cancelWaitTimer() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.waitTimer != nil {
		a.waitTimer.Stop()
		a.waitTimer = nil
	}
}

// TailFile continuously reads analytics events from the JSONL log at logPath,
// calling ProcessEvent for each new line. It exits when an "exit" event is seen,
// when stopCh is closed, or when an unrecoverable read error occurs.
//
// The file is polled every 100ms for new content, matching the typical latency
// tolerance for session state display in a menu bar app.
func (a *Adapter) TailFile(logPath string, stopCh <-chan struct{}) error {
	f, err := os.Open(logPath)
	if err != nil {
		return fmt.Errorf("open analytics log %q: %w", logPath, err)
	}
	defer f.Close()

	var pos int64
	var remainder []byte
	readBuf := make([]byte, 32*1024) // 32KB read buffer

	for {
		select {
		case <-stopCh:
			return nil
		default:
		}

		n, readErr := f.ReadAt(readBuf, pos)
		if n > 0 {
			pos += int64(n)
			chunk := append(remainder, readBuf[:n]...)
			remainder = nil

			done, leftover, err := a.processChunk(chunk)
			remainder = leftover
			if err != nil {
				return err
			}
			if done {
				return nil // "exit" event received
			}
			// Continue immediately if we read a full buffer (more data may be waiting).
			if n == len(readBuf) {
				continue
			}
		}

		if readErr != nil && readErr != io.EOF {
			return fmt.Errorf("reading analytics log: %w", readErr)
		}

		// At EOF — wait for more data.
		select {
		case <-stopCh:
			return nil
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// processChunk splits data into newline-delimited lines and processes each one.
// Returns (done=true) when an "exit" event is seen, plus any incomplete trailing
// bytes that should be prepended to the next chunk.
func (a *Adapter) processChunk(data []byte) (done bool, remainder []byte, err error) {
	for {
		idx := bytes.IndexByte(data, '\n')
		if idx == -1 {
			return false, data, nil
		}
		line := data[:idx]
		data = data[idx+1:]

		if len(line) == 0 {
			continue
		}

		var evt AnalyticsEvent
		if jsonErr := json.Unmarshal(line, &evt); jsonErr != nil {
			continue // skip malformed lines
		}

		if procErr := a.ProcessEvent(&evt); procErr != nil {
			// Log-and-continue: a single bad event shouldn't kill the tailer.
			_ = procErr
		}

		if evt.Event == "exit" {
			return true, nil, nil
		}
	}
}
