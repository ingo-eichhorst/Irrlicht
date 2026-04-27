package services

import (
	"context"
	"encoding/base64"
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
	// statePriorityNoData encodes empty/unfilled buckets on the wire. Stored
	// in-memory as int8(-1); only surfaces when bit-packing for transport.
	statePriorityNoData = 3
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

// tick advances the ring by one granularity-second when its accumulator
// reaches the threshold. Returns (rolled, priority) so callers can build
// per-granularity Tick events without re-reading the ring.
func (rb *ringBuffer) tick() (bool, int8) {
	rb.tickAcc++
	if rb.tickAcc < rb.tickMod {
		return false, 0
	}
	rb.tickAcc = 0

	p := int8(statePriority(rb.lastState))
	rb.buckets[rb.head] = p
	rb.head = (rb.head + 1) % HistoryBucketCount
	if rb.size < HistoryBucketCount {
		rb.size++
	}
	return true, p
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

// encodePriorities returns the buffer's 60 buckets oldest→newest as a slice of
// 2-bit priority codes (0/1/2 = ready/working/waiting, 3 = no-data). Unfilled
// slots in a partially-filled ring pad the front so the newest bucket is always
// at index 59.
func (rb *ringBuffer) encodePriorities() [HistoryBucketCount]uint8 {
	var out [HistoryBucketCount]uint8
	for i := range out {
		out[i] = statePriorityNoData
	}
	if rb.size == 0 {
		return out
	}
	start := (rb.head - rb.size + HistoryBucketCount) % HistoryBucketCount
	dst := HistoryBucketCount - rb.size
	for i := 0; i < rb.size; i++ {
		p := rb.buckets[(start+i)%HistoryBucketCount]
		if p < 0 {
			out[dst+i] = statePriorityNoData
		} else {
			out[dst+i] = uint8(p)
		}
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

// HistoryEventKind identifies the wire-message type a HistoryEvent maps to.
type HistoryEventKind int

const (
	// HistoryEventSnapshot carries the bit-packed history for one session.
	// Emitted on demand when a session is created or a client connects.
	HistoryEventSnapshot HistoryEventKind = iota
	// HistoryEventTick is a bulk per-granularity message: one map entry per
	// session with the priority of the bucket that just rolled. Emitted
	// once per granularity-second by the internal ticker.
	HistoryEventTick
	// HistoryEventUpgrade is a single-session transition that mutates the
	// current bucket of all three rings (the client merges with `max`).
	HistoryEventUpgrade
)

// HistoryEvent is the tagged event delivered to a HistoryTracker.EmitFunc.
// Only the fields matching Kind are populated.
type HistoryEvent struct {
	Kind HistoryEventKind
	// Snapshot
	SessionID string
	History   map[string]string
	// Tick
	GranularitySec int
	Buckets        map[string]int8
	// Upgrade
	Priority int8
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
	emit     func(HistoryEvent)
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

// SetEmitFunc installs a callback that receives history events (snapshots,
// ticks, upgrades) for fan-out over the WebSocket hub. Set to nil to disable
// emission. Must be called before Run() or any OnTransition() to avoid
// missing the early events of a session.
func (h *HistoryTracker) SetEmitFunc(fn func(HistoryEvent)) {
	h.mu.Lock()
	h.emit = fn
	h.mu.Unlock()
}

// EmitSnapshot ships the current bit-packed history for one session through
// the emit callback. Lazy-creates an empty session entry on first call so a
// brand-new session yields an all-no-data snapshot instead of being silently
// skipped — call alongside session_created broadcasts so newly-attached
// clients see a placeholder history bar before the first tick.
func (h *HistoryTracker) EmitSnapshot(sessionID string) {
	h.mu.Lock()
	if _, ok := h.sessions[sessionID]; !ok {
		h.sessions[sessionID] = newSessionBuffers()
	}
	emit := h.emit
	h.mu.Unlock()
	if emit == nil {
		return
	}
	enc, ok := h.Encode(sessionID)
	if !ok {
		return
	}
	emit(HistoryEvent{Kind: HistoryEventSnapshot, SessionID: sessionID, History: enc})
}

func (h *HistoryTracker) OnTransition(sessionID, newState string, _ time.Time) {
	h.mu.Lock()
	sb, ok := h.sessions[sessionID]
	if !ok {
		sb = newSessionBuffers()
		h.sessions[sessionID] = sb
	}
	emit := h.emit
	h.mu.Unlock()

	sb.mu.Lock()
	for _, rb := range sb.bufs {
		rb.upgrade(newState)
	}
	sb.mu.Unlock()

	if emit != nil {
		emit(HistoryEvent{
			Kind:      HistoryEventUpgrade,
			SessionID: sessionID,
			Priority:  int8(statePriority(newState)),
		})
	}
}

// historyEncodedBytes is the byte length of one bit-packed granularity:
// 60 buckets × 2 bits = 120 bits = 15 bytes. Base64-std encodes this to 20
// chars (no padding since 15 % 3 == 0).
const historyEncodedBytes = (HistoryBucketCount*2 + 7) / 8

// packPriorities bit-packs 60 2-bit priority codes into 15 bytes, MSB-first
// within each byte (oldest bucket in the high-order bits of byte 0).
func packPriorities(priorities [HistoryBucketCount]uint8) [historyEncodedBytes]byte {
	var out [historyEncodedBytes]byte
	for i, p := range priorities {
		byteIdx := i / 4
		shift := uint((3 - i%4) * 2)
		out[byteIdx] |= (p & 0x3) << shift
	}
	return out
}

// Encode bit-packs the session's three rolling buffers into a per-granularity
// map of base64-std strings (20 chars each, 60 buckets × 2 bits). Returns
// false if the session is unknown.
func (h *HistoryTracker) Encode(sessionID string) (map[string]string, bool) {
	h.mu.Lock()
	sb, ok := h.sessions[sessionID]
	h.mu.Unlock()
	if !ok {
		return nil, false
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	out := make(map[string]string, 3)
	for _, g := range validGranularities {
		bytes := packPriorities(sb.bufs[granularityIndex(g)].encodePriorities())
		out[strconv.Itoa(g)] = base64.StdEncoding.EncodeToString(bytes[:])
	}
	return out, true
}

// EncodeAll returns the bit-packed history for every known session, keyed by
// session ID. Inner map shape matches Encode().
func (h *HistoryTracker) EncodeAll() map[string]map[string]string {
	h.mu.Lock()
	sids := make([]string, 0, len(h.sessions))
	for sid := range h.sessions {
		sids = append(sids, sid)
	}
	h.mu.Unlock()
	out := make(map[string]map[string]string, len(sids))
	for _, sid := range sids {
		if enc, ok := h.Encode(sid); ok {
			out[sid] = enc
		}
	}
	return out
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
	type entry struct {
		sid string
		sb  *sessionBuffers
	}
	entries := make([]entry, 0, len(h.sessions))
	for sid, sb := range h.sessions {
		entries = append(entries, entry{sid, sb})
	}
	emit := h.emit
	h.mu.Unlock()

	// Per-granularity buckets that rolled this tick. Index matches
	// granularityIndex (0=1s, 1=10s, 2=60s).
	var rolled [3]map[string]int8
	for _, e := range entries {
		e.sb.mu.Lock()
		for gi, rb := range e.sb.bufs {
			ok, p := rb.tick()
			if !ok {
				continue
			}
			if rolled[gi] == nil {
				rolled[gi] = make(map[string]int8)
			}
			rolled[gi][e.sid] = p
		}
		e.sb.mu.Unlock()
	}

	if emit == nil {
		return
	}
	for gi, m := range rolled {
		if len(m) == 0 {
			continue
		}
		emit(HistoryEvent{
			Kind:           HistoryEventTick,
			GranularitySec: validGranularities[gi],
			Buckets:        m,
		})
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
