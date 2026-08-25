package services

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	// in-memory as int8(bucketNoData); only surfaces when bit-packing for
	// transport.
	statePriorityNoData = 3

	// bucketNoData is the IN-MEMORY sentinel for a bucket with no renderable
	// state — an unfilled slot, or a state this build cannot encode (see
	// statePriority). Deliberately negative rather than reusing the wire code
	// 3: a negative value loses every max-merge in upgrade(), so an
	// unencodable transition can never overwrite activity the bucket already
	// observed, whereas 3 would outrank waiting and blank it.
	bucketNoData = -1
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

// statePriority maps a lifecycle state onto the 2-bit code the history bar
// transports. Anything without a code becomes bucketNoData — a blank slot on
// both clients, never green (#1807).
//
// The cases below are the ENCODABLE set, which is deliberately NOT
// session.CanonicalStates(): they differ by exactly session.StateError, which
// is canonical (#1798) yet has no code, because all four slots of the 2-bit
// field are spent (ready/working/waiting/no-data). Widening the field is a
// wire-format change across Go, Swift and JS and belongs to #1805; this
// function must therefore not be rewritten as a session.IsCanonicalState
// check, which would classify `error` as encodable and hand a 5th value to
// packPriorities' 2-bit mask. A canonical state added later lands on
// bucketNoData too, which is the safe direction: blank, not a wrong colour.
//
// #1807 — THE DECISION, AND ITS WIRE CONSEQUENCE.
//
// Before this, the default arm returned statePriorityReady, so `error` and any
// name written by a newer daemon both painted GREEN, and save() then rewrote
// history.json with the coerced value — the same destroy-the-user's-data shape
// #1797/#1806 removed from the session-file reader, one file over.
//
// The choice made here is PRESERVE, not downgrade: an unencodable bucket keeps
// its verbatim state string in ringBuffer.unencodable, so snapshot() — and
// therefore save() — writes back exactly what was read. Only the RENDERED
// value degrades. Consequences, stated because they are the part a reader
// needs and #1815 exists for the case that was neither handled nor mentioned:
//
//   - The wire format is UNCHANGED: still 60 buckets × 2 bits = 15 bytes = 20
//     base64 chars, still four codes. No client decoder moves, and neither
//     does the length assertion in SessionManager+History.swift, irrlicht.js
//     or decodeHistoryString. An unencodable bucket ships as
//     statePriorityNoData, which Swift's historyPriorityToState and the web's
//     HISTORY_PRIORITY_TO_STATE already map to "" and both render as a blank
//     slot rather than a colour.
//   - history.json can therefore now contain a state string this build cannot
//     render. That is intentional — it is what makes a downgrade
//     non-destructive, and #1805 will be able to display those buckets once
//     the encoding widens. A build OLDER than this fix reads such a bucket as
//     ready/green, exactly as it already did with the value it wrote itself,
//     so nothing regresses for it.
//   - Aggregation is unaffected — the activity already recorded in the open
//     bucket survives, which is #1805's documented interim behaviour ("the
//     strip shows what the session was doing, not that it errored"). See
//     upgrade()'s merge comment for the mechanism. Making an unencodable state
//     WIN that merge instead would need the clients to accept a downgrade,
//     which their priority-based merge cannot express — i.e. a client change,
//     which is precisely what this fix is scoped to avoid.
//   - WHAT A USER SEES, stated because it is a real change and the only
//     alternatives are lies: that carry-forward protection covers the single
//     open bucket, not the strip. tick() seals each new bucket from lastState,
//     so a session that STAYS unencodable blanks its whole strip within one
//     ring (60 buckets: 60 s at 1 s, 1 h at 60 s) and both clients then render
//     an empty track, having trimmed the leading no-data slots away. Since
//     #1812/#1813 put real sessions into `error`, that is the common case, not
//     a corner. It is still the right trade: the 2-bit field's only other
//     codes are ready/working/waiting, so anything but no-data would paint a
//     colour the session is not in — which is the bug being fixed. The row's
//     state icon beside the strip is red throughout, so the user is not
//     misled, and #1805 is what makes the strip itself say `error`.
func statePriority(s string) int {
	switch s {
	case session.StateWaiting:
		return statePriorityWaiting
	case session.StateWorking:
		return statePriorityWorking
	case session.StateReady:
		return statePriorityReady
	default:
		return bucketNoData
	}
}

// wireCode maps an in-memory bucket priority onto the 2-bit code clients
// decode, so nothing outside this file ever sees the negative sentinel.
//
// A sign test rather than `== bucketNoData` on purpose: the wire contract is
// "0..3 and nothing else", so any negative belongs on the no-data code,
// including one a later sentinel might add. That is the whole invariant —
// TestHistoryTracker_NoNegativePriorityReachesTheWire pins it end to end,
// because PushMessage.Buckets/.Priority are plain int8 and neither the hub nor
// json.Marshal would object to a -1 going out.
func wireCode(p int8) int8 {
	if p < 0 {
		return statePriorityNoData
	}
	return p
}

// ringBuffer is a fixed-size circular buffer of state strings.
type ringBuffer struct {
	buckets [HistoryBucketCount]int8
	// unencodable holds the verbatim state string for those buckets whose
	// priority is bucketNoData BECAUSE the state has no 2-bit code — keyed by
	// bucket index, nil until one appears (#1807). It is what lets save()
	// write back a value this build cannot render instead of erasing it.
	// Genuinely empty slots are absent from the map, not stored as "".
	unencodable map[int]string
	head        int
	size        int
	tickMod     int
	tickAcc     int
	lastState   string
}

func newRingBuffer(granularitySec int) *ringBuffer {
	rb := &ringBuffer{tickMod: granularitySec}
	for i := range rb.buckets {
		rb.buckets[i] = bucketNoData
	}
	return rb
}

// setBucket writes bucket i from a state string, keeping the verbatim value
// when the state has no 2-bit code. Every write to buckets[] goes through here
// so buckets and unencodable cannot drift apart.
func (rb *ringBuffer) setBucket(i int, state string) {
	p := statePriority(state)
	rb.buckets[i] = int8(p)
	if p == bucketNoData && state != "" {
		if rb.unencodable == nil {
			rb.unencodable = make(map[int]string, 1)
		}
		rb.unencodable[i] = state
		return
	}
	// Guarded rather than unconditional: this is the common path (every
	// encodable tick, for every session, every second), and `delete` on a nil
	// map is a real out-of-line runtime call — ~1.0 ns/op unguarded vs
	// ~0.25 ns/op behind this len check (measured, go test -bench, go1.25
	// darwin/arm64), which is most of the per-tick cost this field added.
	if len(rb.unencodable) > 0 {
		delete(rb.unencodable, i)
	}
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
		rb.setBucket(rb.head, newState)
		rb.head = (rb.head + 1) % HistoryBucketCount
		rb.size = 1
	} else {
		cur := rb.current()
		// `>=`, not `>`, and the difference is load-bearing (#1807). An
		// unencodable state carries bucketNoData, which is below every real
		// priority, so it still cannot displace activity the bucket observed.
		// What the equal case buys is bucketNoData meeting bucketNoData: the
		// bucket holds nothing, so recording the verbatim string overwrites
		// nothing, and a live `error` stops being dropped purely because of
		// which tick phase it arrived in. A tie between two REAL priorities is
		// a no-op write — each code maps to exactly one state name, so the
		// value is the one already there.
		if p >= rb.buckets[cur] {
			rb.setBucket(cur, newState)
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

	rb.setBucket(rb.head, rb.lastState)
	p := rb.buckets[rb.head]
	rb.head = (rb.head + 1) % HistoryBucketCount
	if rb.size < HistoryBucketCount {
		rb.size++
	}
	// The caller ships this straight to clients, so hand back the wire code:
	// a sealed bucket carrying an unencodable state (or a session that has not
	// reported one yet) is no-data, which both decoders render blank.
	return true, wireCode(p)
}

func (rb *ringBuffer) snapshot() []string {
	if rb.size == 0 {
		return nil
	}
	out := make([]string, rb.size)
	start := (rb.head - rb.size + HistoryBucketCount) % HistoryBucketCount
	for i := 0; i < rb.size; i++ {
		idx := (start + i) % HistoryBucketCount
		// A state this build cannot encode round-trips verbatim, so save()
		// writes back what it read rather than a coerced value (#1807). Only a
		// no-data bucket can carry one, and asking that first is deliberate
		// twice over: it gates on the priority rather than trusting the map, so
		// a future write straight to buckets[] degrades to a blank bucket
		// instead of resurrecting a dead state name into a live one — and it
		// skips the map lookup entirely for every encodable bucket.
		if rb.buckets[idx] == bucketNoData {
			if s, ok := rb.unencodable[idx]; ok {
				out[i] = s
				continue
			}
		}
		out[i] = priorityToState(rb.buckets[idx])
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
		out[dst+i] = uint8(wireCode(rb.buckets[(start+i)%HistoryBucketCount]))
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
		rb.buckets[i] = bucketNoData
	}
	rb.unencodable = nil
	for i, s := range states {
		rb.setBucket(i, s)
	}
	n := len(states)
	rb.head = n % HistoryBucketCount
	rb.size = n
	rb.lastState = states[n-1]
}

// priorityToState is snapshot()'s half of the pair: it names the state a
// bucket's 2-bit code stands for, and is used only to build the []string that
// save() persists and Snapshot() returns — never to decode the wire, which the
// Swift and JS clients do themselves.
//
// Everything that is not one of the three encodable codes — an unfilled slot,
// or a bucket whose unencodable string has already been consumed by snapshot()
// — yields "" (no data). It must NOT fall back to session.StateReady: that is
// the mirror half of #1807's bug, where a blank bucket was persisted as `ready`
// and came back green on the next Load. "" is the same no-data spelling both
// client decoders already use.
func priorityToState(p int8) string {
	switch p {
	case statePriorityWaiting:
		return session.StateWaiting
	case statePriorityWorking:
		return session.StateWorking
	case statePriorityReady:
		return session.StateReady
	default:
		return ""
	}
}

type sessionBuffers struct {
	mu   sync.Mutex
	bufs [3]*ringBuffer // index 0=1s, 1=10s, 2=60s
	// tickGen[i] increments by 1 each time bufs[i] rolls a bucket. Captured
	// alongside bucket state under mu so snapshots and tick events can be
	// reconciled by the client (see EncodeWithGens / tick(): if a snapshot
	// already reflects a tick, the matching tick message arrives with a gen
	// equal to the snapshot's, and the client skips it).
	tickGen [3]uint64
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
	SessionID   string
	History     map[string]string // granularity → base64
	Generations map[string]uint64 // granularity → tick generation that produced History
	// Tick
	GranularitySec    int
	Buckets           map[string]int8   // sessionID → priority of the bucket that just rolled
	BucketGenerations map[string]uint64 // sessionID → tick generation after this roll
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
	emit(HistoryEvent{
		Kind:        HistoryEventSnapshot,
		SessionID:   sessionID,
		History:     enc.History,
		Generations: enc.Generations,
	})
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

	// An unencodable state produces no upgrade message. Both clients merge an
	// upgrade by priority and no-data is their lowest, so such a message could
	// only ever be discarded — and the ring above kept whatever it held, for
	// the same reason (see upgrade()). Sending nothing keeps client and server
	// in step without putting a value on the wire that means "downgrade this
	// bucket", which the 2-bit contract has no way to say (#1807).
	p := statePriority(newState)
	if emit != nil && p != bucketNoData {
		emit(HistoryEvent{
			Kind:      HistoryEventUpgrade,
			SessionID: sessionID,
			Priority:  int8(p),
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

// EncodedHistory is one session's bit-packed history together with the
// per-granularity tick generations that produced it. Both are read under the
// session's lock so a client receiving a snapshot+tick pair can dedupe by
// generation: see SessionManager.swift / index.html for the apply-or-skip
// logic.
type EncodedHistory struct {
	History     map[string]string // granularity ("1"/"10"/"60") → 20-char base64
	Generations map[string]uint64 // same keys as History
}

// Encode bit-packs the session's three rolling buffers and captures the
// matching tick generations atomically under the session lock. Returns false
// if the session is unknown.
func (h *HistoryTracker) Encode(sessionID string) (EncodedHistory, bool) {
	h.mu.Lock()
	sb, ok := h.sessions[sessionID]
	h.mu.Unlock()
	if !ok {
		return EncodedHistory{}, false
	}
	sb.mu.Lock()
	defer sb.mu.Unlock()
	out := EncodedHistory{
		History:     make(map[string]string, 3),
		Generations: make(map[string]uint64, 3),
	}
	for _, g := range validGranularities {
		gi := granularityIndex(g)
		bytes := packPriorities(sb.bufs[gi].encodePriorities())
		key := strconv.Itoa(g)
		out.History[key] = base64.StdEncoding.EncodeToString(bytes[:])
		out.Generations[key] = sb.tickGen[gi]
	}
	return out, true
}

// EncodeAll returns the bit-packed history for every known session.
func (h *HistoryTracker) EncodeAll() map[string]EncodedHistory {
	h.mu.Lock()
	sids := make([]string, 0, len(h.sessions))
	for sid := range h.sessions {
		sids = append(sids, sid)
	}
	h.mu.Unlock()
	out := make(map[string]EncodedHistory, len(sids))
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

	// Per-granularity buckets that rolled this tick (and the tick generation
	// at which each session's bucket rolled). Index matches granularityIndex
	// (0=1s, 1=10s, 2=60s). The generation is captured under sb.mu in the
	// same critical section that mutates the ring, so a concurrent Encode
	// either observes pre-tick (gen=N-1, pre-mutated buckets) or post-tick
	// (gen=N, post-mutated buckets) — never a mismatched pair.
	var rolled [3]map[string]int8
	var rolledGens [3]map[string]uint64
	for _, e := range entries {
		e.sb.mu.Lock()
		for gi, rb := range e.sb.bufs {
			ok, p := rb.tick()
			if !ok {
				continue
			}
			e.sb.tickGen[gi]++
			if rolled[gi] == nil {
				rolled[gi] = make(map[string]int8)
				rolledGens[gi] = make(map[string]uint64)
			}
			rolled[gi][e.sid] = p
			rolledGens[gi][e.sid] = e.sb.tickGen[gi]
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
			Kind:              HistoryEventTick,
			GranularitySec:    validGranularities[gi],
			Buckets:           m,
			BucketGenerations: rolledGens[gi],
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
		// Subagent histories are transient — their sessions are deleted when
		// the parent finishes, so a restored entry can only be a leak. Also
		// one-time-heals files bloated by the pre-#593 eviction gap (deletion
		// paths in PIDManager never evicted history).
		if strings.HasPrefix(sid, "agent-") {
			continue
		}
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
