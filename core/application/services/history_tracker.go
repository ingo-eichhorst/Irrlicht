package services

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"irrlicht/core/domain/session"
)

const (
	// HistoryBucketCount is the number of buckets retained per granularity per
	// session. At 1s granularity this is 60 s; at 60s it is 1 h.
	HistoryBucketCount = 60

	statePriorityReady   = 0
	statePriorityWorking = 1
	statePriorityWaiting = 2
)

var validGranularities = []int{1, 10, 60}

func validGranularity(sec int) bool {
	for _, g := range validGranularities {
		if g == sec {
			return true
		}
	}
	return false
}

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
	buckets   [HistoryBucketCount]int8
	head      int
	size      int
	tickMod   int
	tickAcc   int
	lastState string
}

func newRingBuffer(granularitySec int) *ringBuffer {
	rb := &ringBuffer{tickMod: granularitySec}
	for i := range rb.buckets {
		rb.buckets[i] = -1
	}
	return rb
}

func (rb *ringBuffer) current() int {
	if rb.size == 0 {
		return -1
	}
	return (rb.head - 1 + HistoryBucketCount) % HistoryBucketCount
}

func (rb *ringBuffer) upgrade(newState string) {
	p := int8(statePriority(newState))
	if rb.size == 0 {
		rb.buckets[rb.head] = p
		rb.head = (rb.head + 1) % HistoryBucketCount
		rb.size = 1
	} else {
		cur := rb.current()
		if p > rb.buckets[cur] {
			rb.buckets[cur] = p
		}
	}
	rb.lastState = newState
}

func (rb *ringBuffer) tick() {
	rb.tickAcc++
	if rb.tickAcc < rb.tickMod {
		return
	}
	rb.tickAcc = 0

	p := int8(statePriority(rb.lastState))
	rb.buckets[rb.head] = p
	rb.head = (rb.head + 1) % HistoryBucketCount
	if rb.size < HistoryBucketCount {
		rb.size++
	}
}

func (rb *ringBuffer) snapshot() []string {
	if rb.size == 0 {
		return nil
	}
	out := make([]string, rb.size)
	start := (rb.head - rb.size + HistoryBucketCount) % HistoryBucketCount
	for i := 0; i < rb.size; i++ {
		out[i] = priorityToState(rb.buckets[(start+i)%HistoryBucketCount])
	}
	return out
}

// restore pre-populates the buffer from a saved snapshot (oldest→newest).
func (rb *ringBuffer) restore(states []string) {
	if len(states) == 0 {
		return
	}
	if len(states) > HistoryBucketCount {
		states = states[len(states)-HistoryBucketCount:]
	}
	for i := range rb.buckets {
		rb.buckets[i] = -1
	}
	for i, s := range states {
		rb.buckets[i] = int8(statePriority(s))
	}
	n := len(states)
	rb.head = n % HistoryBucketCount
	rb.size = n
	rb.lastState = states[n-1]
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

type sessionBuffers struct {
	mu   sync.Mutex
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

// HistoryTracker maintains per-session rolling state buffers in memory.
// Three granularities (1 s / 10 s / 60 s) are kept in parallel; within each
// bucket priority aggregation (waiting > working > ready) determines the state.
// When saveDir is non-empty the tracker persists state to history.json in that
// directory so history survives daemon restarts.
type HistoryTracker struct {
	mu       sync.Mutex
	sessions map[string]*sessionBuffers
	saveDir  string
}

// NewHistoryTracker creates a HistoryTracker without persistence.
func NewHistoryTracker() *HistoryTracker {
	return &HistoryTracker{sessions: make(map[string]*sessionBuffers)}
}

// NewHistoryTrackerWithDir creates a HistoryTracker that persists state to
// saveDir/history.json. Call Load() to restore a previous run's state.
func NewHistoryTrackerWithDir(saveDir string) *HistoryTracker {
	return &HistoryTracker{
		sessions: make(map[string]*sessionBuffers),
		saveDir:  saveDir,
	}
}

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

func (h *HistoryTracker) Remove(sessionID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.sessions, sessionID)
}

// Run starts the internal 1-second ticker. Saves state every 60 ticks and on
// shutdown. Blocks until ctx is cancelled.
func (h *HistoryTracker) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	tickCount := 0
	for {
		select {
		case <-ctx.Done():
			h.save()
			return
		case <-ticker.C:
			h.tick()
			tickCount++
			if tickCount%60 == 0 {
				h.save()
			}
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

type historyFile struct {
	Version  int                            `json:"version"`
	Sessions map[string]map[string][]string `json:"sessions"`
}

// Load restores state from saveDir/history.json. Silent on missing or corrupt
// files — the tracker just starts empty.
func (h *HistoryTracker) Load() {
	if h.saveDir == "" {
		return
	}
	b, err := os.ReadFile(filepath.Join(h.saveDir, "history.json"))
	if err != nil {
		return
	}
	var hf historyFile
	if err := json.Unmarshal(b, &hf); err != nil || hf.Version != 1 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for sid, granMap := range hf.Sessions {
		sb := newSessionBuffers()
		for gStr, states := range granMap {
			g, err := strconv.Atoi(gStr)
			if err != nil || !validGranularity(g) {
				continue
			}
			sb.bufs[granularityIndex(g)].restore(states)
		}
		h.sessions[sid] = sb
	}
}

func (h *HistoryTracker) save() {
	if h.saveDir == "" {
		return
	}
	h.mu.Lock()
	data := make(map[string]map[string][]string, len(h.sessions))
	for sid, sb := range h.sessions {
		sb.mu.Lock()
		m := make(map[string][]string, 3)
		for _, g := range validGranularities {
			if snap := sb.bufs[granularityIndex(g)].snapshot(); len(snap) > 0 {
				m[strconv.Itoa(g)] = snap
			}
		}
		sb.mu.Unlock()
		if len(m) > 0 {
			data[sid] = m
		}
	}
	h.mu.Unlock()

	b, err := json.Marshal(historyFile{Version: 1, Sessions: data})
	if err != nil {
		return
	}
	if err := os.MkdirAll(h.saveDir, 0700); err != nil {
		return
	}
	tmp := filepath.Join(h.saveDir, "history.json.tmp")
	if err := os.WriteFile(tmp, b, 0600); err != nil {
		return
	}
	_ = os.Rename(tmp, filepath.Join(h.saveDir, "history.json"))
}
