package replay

import (
	"context"
	"sync"
	"time"

	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// StateMachine walks a slice of lifecycle events at a configurable speed,
// translating each event into a SessionState mutation and emitting a
// PushMessage onto the broadcaster. The dashboard's WebSocket hub
// subscribes to the broadcaster and forwards messages to connected
// clients, so the user sees sessions appearing / transitioning /
// disappearing in real time as the recording replays.
//
// The mapping from lifecycle events to SessionState is the minimum
// surface the dashboard reads — Version, SessionID, State, Adapter,
// CWD, TranscriptPath, PID, ParentSessionID, FirstSeen, UpdatedAt,
// EventCount. Full fidelity (metrics, tasks, subagents) is out of scope
// for this iteration; we add fields incrementally as we surface more
// in the viewer.
type StateMachine struct {
	events      []lifecycle.Event
	broadcaster outbound.PushBroadcaster

	mu       sync.Mutex
	cursor   int                              // index of next event to process
	sessions map[string]*session.SessionState // SessionID → current state

	// playStart is the wall-clock time Run() began. The dashboard reads
	// FirstSeen/UpdatedAt as Unix-seconds and computes "age" against
	// `now`; using the recording's original timestamps would make every
	// session look weeks-old. We rewrite ts on emit so the dashboard
	// renders ages as if the recording were happening live.
	playStart      time.Time
	recordingStart time.Time

	// playhead is the live virtual-time offset in ms — the scrubber's
	// value. Decoupled from `cursor` (which is event-based) so the
	// scrubber advances smoothly between events instead of freezing.
	// Updated by tickPlayhead(); guarded by mu.
	playheadMs       float64
	lastTickWall     time.Time
	playheadRunning  bool

	speedMu sync.RWMutex
	speed   float64 // 1.0, 2.0, 5.0, …; >0 always

	cmd chan command

	doneCh chan struct{}
}

type command struct {
	kind     string // "pause" / "resume" / "stop" / "seek"
	seekToMs int64
	reply    chan struct{}
}

// New constructs a StateMachine. speed must be >0 (1 = real time).
func New(events []lifecycle.Event, broadcaster outbound.PushBroadcaster, speed float64) *StateMachine {
	if speed <= 0 {
		speed = 1.0
	}
	return &StateMachine{
		events:      events,
		broadcaster: broadcaster,
		sessions:    map[string]*session.SessionState{},
		speed:       speed,
		cmd:         make(chan command, 8),
		doneCh:      make(chan struct{}),
	}
}

// Speed reports the current playback speed.
func (m *StateMachine) Speed() float64 {
	m.speedMu.RLock()
	defer m.speedMu.RUnlock()
	return m.speed
}

// tickPlayheadLocked accumulates wall-clock-elapsed-since-last-tick into
// the live playhead, scaled by the CURRENT speed. Called before any read
// of m.playheadMs and on every state change (pause/resume/setspeed/seek)
// so the playhead reflects the right value for the active speed/state.
// Caller MUST hold m.mu.
func (m *StateMachine) tickPlayheadLocked() {
	now := time.Now()
	if m.playheadRunning && !m.lastTickWall.IsZero() {
		elapsed := now.Sub(m.lastTickWall).Seconds()
		m.playheadMs += elapsed * 1000.0 * m.Speed()
		if total := m.totalDurationMsLocked(); total > 0 && m.playheadMs > float64(total) {
			m.playheadMs = float64(total)
		}
	}
	m.lastTickWall = now
}

// totalDurationMsLocked returns the recording's total span in ms.
// Caller MUST hold m.mu.
func (m *StateMachine) totalDurationMsLocked() int64 {
	if len(m.events) < 2 {
		return 0
	}
	return m.events[len(m.events)-1].Timestamp.Sub(m.events[0].Timestamp).Milliseconds()
}

// LivePlayheadMs returns the scrubber's continuous playhead offset in
// milliseconds (virtual time, anchored to the recording's first event).
// Continuous between events — increments every call by wall-clock delta
// since the last tick, scaled by current speed. The pause / resume /
// SetSpeed methods all call tickPlayheadLocked() so the playhead doesn't
// jump when speed changes or pauses end.
func (m *StateMachine) LivePlayheadMs() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tickPlayheadLocked()
	return int64(m.playheadMs)
}

// SetSpeed changes playback speed live. Subsequent inter-event waits
// are recomputed against the new value.
func (m *StateMachine) SetSpeed(s float64) {
	if s <= 0 {
		return
	}
	// Lock playhead state first to flush the accumulated offset at the
	// OLD speed before changing speed — otherwise the next tick would
	// retroactively scale the elapsed time at the new speed.
	m.mu.Lock()
	m.tickPlayheadLocked()
	m.mu.Unlock()
	m.speedMu.Lock()
	m.speed = s
	m.speedMu.Unlock()
}

// Done returns a channel closed when the state machine exits (either
// because all events have been applied or because Stop was called).
func (m *StateMachine) Done() <-chan struct{} { return m.doneCh }

// Pause halts event emission until Resume is called.
func (m *StateMachine) Pause() { m.sendCmd(command{kind: "pause"}) }

// Resume continues emission after Pause.
func (m *StateMachine) Resume() { m.sendCmd(command{kind: "resume"}) }

// Stop terminates the state machine; Run returns shortly after.
func (m *StateMachine) Stop() { m.sendCmd(command{kind: "stop"}) }

// SeekToOffset jumps the cursor to the first event whose offset (in ms,
// relative to events[0].Timestamp) is at or after offsetMs. Sessions
// that existed at that point are reconstructed by re-applying events up
// to the seek target with zero delay.
func (m *StateMachine) SeekToOffset(offsetMs int64) {
	m.sendCmd(command{kind: "seek", seekToMs: offsetMs})
}

func (m *StateMachine) sendCmd(c command) {
	select {
	case m.cmd <- c:
	default:
		// Drop on full queue; the state machine is shutting down.
	}
}

// PrimeFirstEvent applies events[0] synchronously and advances the
// cursor to 1. Used by StartViewerInternal to close the race window
// where /start returns before Run()'s goroutine has scheduled and the
// dashboard's initial /api/v1/sessions fetch sees an empty snapshot.
//
// After this call, m.sessions has the first-event-derived state and
// Snapshot() returns a non-empty slice. Subsequent Run() begins at
// cursor=1.
func (m *StateMachine) PrimeFirstEvent() {
	if len(m.events) == 0 {
		return
	}
	m.mu.Lock()
	if m.cursor > 0 {
		m.mu.Unlock()
		return
	}
	ev := m.events[0]
	m.cursor = 1
	m.mu.Unlock()
	m.apply(ev)
}

// Run drives the playback. Returns when ctx is cancelled, Stop is
// called, or all events are consumed. Safe to call once per StateMachine.
//
// Cursor is advanced only AFTER an event is applied — otherwise a pause
// arriving during the inter-event sleep would drop the pending event.
func (m *StateMachine) Run(ctx context.Context) {
	defer close(m.doneCh)
	if len(m.events) == 0 {
		return
	}
	anchor := m.events[0].Timestamp
	m.mu.Lock()
	m.playStart = time.Now().UTC()
	m.recordingStart = anchor
	m.lastTickWall = time.Now()
	m.playheadRunning = true
	m.playheadMs = 0
	m.mu.Unlock()
	paused := false

	handle := func(c command) (stop bool) {
		switch c.kind {
		case "pause":
			paused = true
			m.mu.Lock()
			m.tickPlayheadLocked()
			m.playheadRunning = false
			m.mu.Unlock()
		case "resume":
			paused = false
			m.mu.Lock()
			m.lastTickWall = time.Now()
			m.playheadRunning = true
			m.mu.Unlock()
		case "stop":
			m.mu.Lock()
			m.tickPlayheadLocked()
			m.playheadRunning = false
			m.mu.Unlock()
			return true
		case "seek":
			m.seekTo(c.seekToMs, anchor)
			m.mu.Lock()
			m.playheadMs = float64(c.seekToMs)
			m.lastTickWall = time.Now()
			m.mu.Unlock()
		}
		return false
	}

	for {
		if ctx.Err() != nil {
			return
		}
		// While paused, only commands advance us.
		if paused {
			select {
			case <-ctx.Done():
				return
			case c := <-m.cmd:
				if handle(c) {
					return
				}
			}
			continue
		}

		// Read the next event without advancing the cursor — that way a
		// pause/seek mid-wait doesn't lose it.
		m.mu.Lock()
		cur := m.cursor
		m.mu.Unlock()
		if cur >= len(m.events) {
			return
		}
		ev := m.events[cur]

		// Compute the wait until this event's timestamp.
		var scaled time.Duration
		if cur > 0 {
			prev := m.events[cur-1].Timestamp
			scaled = time.Duration(float64(ev.Timestamp.Sub(prev)) / m.Speed())
		}

		if scaled > 0 {
			select {
			case <-ctx.Done():
				return
			case c := <-m.cmd:
				if handle(c) {
					return
				}
				continue // don't apply ev; re-evaluate (possibly paused / seeked / new cursor)
			case <-time.After(scaled):
			}
		} else {
			// Still poll commands non-blocking so back-to-back zero-delta
			// events stay responsive to pause/stop.
			select {
			case c := <-m.cmd:
				if handle(c) {
					return
				}
				continue
			default:
			}
		}

		m.apply(ev)
		m.mu.Lock()
		// Only advance if we're still on the same cursor (a seek may
		// have moved us); applying out-of-order events is fine but the
		// cursor must reflect that the just-applied event is consumed.
		if m.cursor == cur {
			m.cursor++
		}
		m.mu.Unlock()
	}
}

// seekTo fast-forwards (or rewinds) the cursor to the first event whose
// offset from anchor is >= offsetMs, re-applying events without delay
// so the synthetic SessionState reflects the seek target.
func (m *StateMachine) seekTo(offsetMs int64, anchor time.Time) {
	target := anchor.Add(time.Duration(offsetMs) * time.Millisecond)
	m.mu.Lock()
	defer m.mu.Unlock()
	// Find the first event at or after target.
	newCursor := 0
	for i, e := range m.events {
		if !e.Timestamp.Before(target) {
			newCursor = i
			break
		}
		newCursor = i + 1
	}
	// If seeking forward, replay intermediate events into the synthetic
	// state map (no delays, no broadcasts beyond the final session_*
	// per session) so the dashboard sees the right state at the seek
	// target. For simplicity in this iteration, broadcast every replayed
	// event. The dashboard collapses rapid duplicates.
	if newCursor > m.cursor {
		for i := m.cursor; i < newCursor; i++ {
			m.applyLocked(m.events[i])
		}
	} else if newCursor < m.cursor {
		// Seeking backward: rebuild from scratch up to newCursor.
		m.sessions = map[string]*session.SessionState{}
		for i := 0; i < newCursor; i++ {
			m.applyLocked(m.events[i])
		}
	}
	m.cursor = newCursor
}

func (m *StateMachine) apply(ev lifecycle.Event) {
	m.mu.Lock()
	m.applyLocked(ev)
	m.mu.Unlock()
}

// eventTimestampLive rewrites a recording timestamp to "playback wall
// clock" — i.e. now + (event_ts - recording_start). Returns Unix seconds.
// When called before Run() has set playStart, falls back to time.Now()
// so seed unit tests with zero-value playStart still produce sensible
// FirstSeen / UpdatedAt values.
func (m *StateMachine) eventTimestampLive(evTs time.Time) int64 {
	if m.playStart.IsZero() {
		return time.Now().Unix()
	}
	offset := evTs.Sub(m.recordingStart)
	return m.playStart.Add(offset).Unix()
}

// applyLocked mutates the synthetic session map and emits a PushMessage.
// Caller holds m.mu.
func (m *StateMachine) applyLocked(ev lifecycle.Event) {
	sid := ev.SessionID
	if sid == "" {
		return
	}
	now := m.eventTimestampLive(ev.Timestamp)

	switch ev.Kind {
	case lifecycle.KindTranscriptNew, lifecycle.KindPreSessionCreated:
		state, existed := m.sessions[sid]
		if !existed {
			state = &session.SessionState{
				Version: 1, SessionID: sid, State: "ready",
				Adapter: ev.Adapter, TranscriptPath: ev.TranscriptPath,
				CWD: ev.CWD, FirstSeen: now, UpdatedAt: now,
				Confidence: "high", EventCount: 1, LastEvent: string(ev.Kind),
			}
			m.sessions[sid] = state
			m.broadcast(outbound.PushTypeCreated, state)
			return
		}
		state.UpdatedAt = now
		state.EventCount++
		state.LastEvent = string(ev.Kind)
		if state.TranscriptPath == "" {
			state.TranscriptPath = ev.TranscriptPath
		}
		if state.CWD == "" {
			state.CWD = ev.CWD
		}
		m.broadcast(outbound.PushTypeUpdated, state)

	case lifecycle.KindPIDDiscovered:
		state := m.upsertExisting(sid, now, ev.Adapter)
		state.PID = ev.PID
		state.LastEvent = string(ev.Kind)
		m.broadcast(outbound.PushTypeUpdated, state)

	case lifecycle.KindStateTransition:
		state := m.upsertExisting(sid, now, ev.Adapter)
		if ev.NewState != "" {
			state.State = ev.NewState
		}
		state.LastEvent = string(ev.Kind)
		m.broadcast(outbound.PushTypeUpdated, state)

	case lifecycle.KindTranscriptActivity:
		state := m.upsertExisting(sid, now, ev.Adapter)
		state.LastEvent = string(ev.Kind)
		m.broadcast(outbound.PushTypeUpdated, state)

	case lifecycle.KindProcessExited, lifecycle.KindTranscriptRemoved,
		lifecycle.KindPreSessionRemoved:
		if state, ok := m.sessions[sid]; ok {
			delete(m.sessions, sid)
			m.broadcast(outbound.PushTypeDeleted, state)
		}

	case lifecycle.KindParentLinked:
		state := m.upsertExisting(sid, now, ev.Adapter)
		state.ParentSessionID = ev.ParentSessionID
		state.LastEvent = string(ev.Kind)
		m.broadcast(outbound.PushTypeUpdated, state)

	default:
		// debounce_coalesced, file_event, hook_received: bookkeeping
		// only — bump event count if the session exists.
		if state, ok := m.sessions[sid]; ok {
			state.UpdatedAt = now
			state.EventCount++
			state.LastEvent = string(ev.Kind)
		}
	}
}

// upsertExisting returns the current session state for sid, creating a
// stub if necessary. Used by events that reference a session before its
// transcript_new (defensive: real recordings always create-first, but
// out-of-order replays shouldn't crash).
func (m *StateMachine) upsertExisting(sid string, now int64, adapter string) *session.SessionState {
	if state, ok := m.sessions[sid]; ok {
		state.UpdatedAt = now
		state.EventCount++
		return state
	}
	state := &session.SessionState{
		Version: 1, SessionID: sid, State: "ready", Adapter: adapter,
		FirstSeen: now, UpdatedAt: now, Confidence: "low", EventCount: 1,
	}
	m.sessions[sid] = state
	m.broadcast(outbound.PushTypeCreated, state)
	return state
}

func (m *StateMachine) broadcast(typ string, s *session.SessionState) {
	// Deep-copy before broadcasting so post-emit mutations don't race
	// with WebSocket marshaling.
	cp := *s
	m.broadcaster.Broadcast(outbound.PushMessage{Type: typ, Session: &cp})
}

// Snapshot returns a copy of the current session map. Used by the
// REST GET /api/v1/sessions endpoint.
func (m *StateMachine) Snapshot() []*session.SessionState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*session.SessionState, 0, len(m.sessions))
	for _, s := range m.sessions {
		cp := *s
		out = append(out, &cp)
	}
	return out
}

// CursorOffsetMs returns how far through the recording the playhead is.
// Used by the frontend to position the scrubber.
func (m *StateMachine) CursorOffsetMs() int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cursor == 0 || len(m.events) == 0 {
		return 0
	}
	idx := m.cursor - 1
	if idx >= len(m.events) {
		idx = len(m.events) - 1
	}
	return m.events[idx].Timestamp.Sub(m.events[0].Timestamp).Milliseconds()
}

// TotalDurationMs reports the recording's wall-clock duration.
func (m *StateMachine) TotalDurationMs() int64 {
	if len(m.events) < 2 {
		return 0
	}
	return m.events[len(m.events)-1].Timestamp.Sub(m.events[0].Timestamp).Milliseconds()
}

// EventMarker is one point on the scrubber's event-tick lane. The viewer
// uses these to render colored ticks on the progress bar AND to drive
// "jump to previous/next event" controls.
type EventMarker struct {
	OffsetMs  int64  `json:"offset_ms"`
	Kind      string `json:"kind"`
	SessionID string `json:"session_id,omitempty"`
	NewState  string `json:"new_state,omitempty"`
}

// EventMarkers returns one marker per event, ordered by offset. Offsets
// are anchored to the recording's first event (events[0]).
func (m *StateMachine) EventMarkers() []EventMarker {
	if len(m.events) == 0 {
		return nil
	}
	anchor := m.events[0].Timestamp
	out := make([]EventMarker, len(m.events))
	for i, e := range m.events {
		out[i] = EventMarker{
			OffsetMs:  e.Timestamp.Sub(anchor).Milliseconds(),
			Kind:      string(e.Kind),
			SessionID: e.SessionID,
			NewState:  e.NewState,
		}
	}
	return out
}
