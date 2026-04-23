package services

import (
	"context"
	"sync"
	"time"

	"irrlicht/core/domain/session"
)

const (
	historyBucketCount = 150

	// statePriority maps state names to an integer so we can apply
	// waiting > working > ready aggregation within a bucket.
	statePriorityReady   = 0
	statePriorityWorking = 1
	statePriorityWaiting = 2
)

var validGranularities = []int{1, 10, 60}

func statePriority(s string) int {
	switch s {
	case session.StateWaiting:
		return statePriorityWaiting
	case session.StateWorking:
		return statePriorityWorking
	default:
		return statePriorityReady
	}
}

// ringBuffer is a fixed-size circular buffer of state strings.
type ringBuffer struct {
	buckets  [historyBucketCount]int8 // encoded priority; -1 = empty
	head     int                      // next-write index
	size     int                      // number of valid entries
	tickMod  int                      // advance every tickMod global 1-s ticks
	tickAcc  int                      // accumulator towards tickMod
	lastState string                  // carry-forward: last state before current open bucket
}

func newRingBuffer(granularitySec int) *ringBuffer {
	rb := &ringBuffer{tickMod: granularitySec}
	for i := range rb.buckets {
		rb.buckets[i] = -1
	}
	return rb
}

// current returns the index of the currently open (not yet finalised) bucket.
func (rb *ringBuffer) current() int {
	if rb.size == 0 {
		return -1
	}
	return (rb.head - 1 + historyBucketCount) % historyBucketCount
}

// upgrade writes the highest-priority state into the current open bucket.
func (rb *ringBuffer) upgrade(newState string) {
	p := int8(statePriority(newState))
	if rb.size == 0 {
		// Initialise first bucket.
		rb.buckets[rb.head] = p
		rb.head = (rb.head + 1) % historyBucketCount
		rb.size = 1
	} else {
		cur := rb.current()
		if p > rb.buckets[cur] {
			rb.buckets[cur] = p
		}
	}
	rb.lastState = newState
}

// tick advances the buffer by one global 1-second tick.
func (rb *ringBuffer) tick() {
	rb.tickAcc++
	if rb.tickAcc < rb.tickMod {
		return
	}
	rb.tickAcc = 0

	// Carry forward the last known state into the new bucket.
	p := int8(statePriority(rb.lastState))
	rb.buckets[rb.head] = p
	rb.head = (rb.head + 1) % historyBucketCount
	if rb.size < historyBucketCount {
		rb.size++
	}
}

// snapshot returns all valid entries oldest→newest as state strings.
func (rb *ringBuffer) snapshot() []string {
	if rb.size == 0 {
		return nil
	}
	out := make([]string, rb.size)
	start := (rb.head - rb.size + historyBucketCount) % historyBucketCount
	for i := 0; i < rb.size; i++ {
		out[i] = priorityToState(rb.buckets[(start+i)%historyBucketCount])
	}
	return out
}

func priorityToState(p int8) string {
	switch p {
	case statePriorityWaiting:
		return session.StateWaiting
	case statePriorityWorking:
		return session.StateWorking
	default:
		return session.StateReady
	}
}

// sessionBuffers holds one ring buffer per supported granularity.
type sessionBuffers struct {
	mu  sync.Mutex
	bufs [3]*ringBuffer // index 0=1s, 1=10s, 2=60s
}

func newSessionBuffers() *sessionBuffers {
	return &sessionBuffers{
		bufs: [3]*ringBuffer{
			newRingBuffer(1),
			newRingBuffer(10),
			newRingBuffer(60),
		},
	}
}

func granularityIndex(sec int) int {
	switch sec {
	case 10:
		return 1
	case 60:
		return 2
	default:
		return 0
	}
}

// HistoryTracker maintains per-session rolling state buffers in memory for as
// long as the daemon runs. Three granularities (1 s / 10 s / 60 s) are kept in
// parallel; within each bucket, priority aggregation (waiting > working > ready)
// determines the displayed state.
type HistoryTracker struct {
	mu       sync.Mutex
	sessions map[string]*sessionBuffers
}

// NewHistoryTracker creates a HistoryTracker ready for use. Call Run(ctx) to
// start the internal 1-second ticker.
func NewHistoryTracker() *HistoryTracker {
	return &HistoryTracker{
		sessions: make(map[string]*sessionBuffers),
	}
}

// OnTransition records a state transition, upgrading all three buffers' current
// open buckets if the new state has higher priority.
func (h *HistoryTracker) OnTransition(sessionID, newState string, _ time.Time) {
	h.mu.Lock()
	sb, ok := h.sessions[sessionID]
	if !ok {
		sb = newSessionBuffers()
		h.sessions[sessionID] = sb
	}
	h.mu.Unlock()

	sb.mu.Lock()
	defer sb.mu.Unlock()
	for _, rb := range sb.bufs {
		rb.upgrade(newState)
	}
}

// Snapshot returns the ring-buffer contents for a session at the given
// granularity (1, 10, or 60 seconds), oldest→newest. Returns nil, false when
// the session is unknown.
func (h *HistoryTracker) Snapshot(sessionID string, granularitySec int) ([]string, bool) {
	h.mu.Lock()
	sb, ok := h.sessions[sessionID]
	h.mu.Unlock()
	if !ok {
		return nil, false
	}

	sb.mu.Lock()
	defer sb.mu.Unlock()
	idx := granularityIndex(granularitySec)
	return sb.bufs[idx].snapshot(), true
}

// Remove drops all buffers for a session when it is deleted.
func (h *HistoryTracker) Remove(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions, sessionID)
}

// Run starts the internal 1-second ticker that advances all ring buffers.
// Blocks until ctx is cancelled.
func (h *HistoryTracker) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.tick()
		}
	}
}

func (h *HistoryTracker) tick() {
	h.mu.Lock()
	sbs := make([]*sessionBuffers, 0, len(h.sessions))
	for _, sb := range h.sessions {
		sbs = append(sbs, sb)
	}
	h.mu.Unlock()

	for _, sb := range sbs {
		sb.mu.Lock()
		for _, rb := range sb.bufs {
			rb.tick()
		}
		sb.mu.Unlock()
	}
}

// ValidGranularity reports whether sec is an accepted granularity value.
func ValidGranularity(sec int) bool {
	for _, g := range validGranularities {
		if g == sec {
			return true
		}
	}
	return false
}
