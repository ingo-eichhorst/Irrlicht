package viewer

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/adapters/outbound/websocket"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
	"irrlicht/tools/agent-onboarding/internal/replay"
)

// archiveNameRE constrains the optional `recording` field on /api/replay/start
// so a caller can't escape the scenario directory via "..". Matches the
// shape promote-recording.sh produces: <timestamp>_irrlichd-<version>,
// where the version is a semver+sha string like "0.3.13+4662be4.dirty" —
// `+` and `.` are both legal characters in archive names.
var archiveNameRE = regexp.MustCompile(`^[A-Za-z0-9._:+-]+$`)

// Playback represents one active replay session. Only one Playback may
// be active per Manager at a time — starting a new one stops the
// previous, mirroring how a real daemon serves one set of sessions.
type Playback struct {
	ID       string
	Agent    string
	Subtree  string // "scenarios" | "regression"
	Scenario string
	Mode     string // "viewer-internal" — kept as a field for future modes
	Speed    float64

	StartedAt time.Time

	broadcaster outbound.PushBroadcaster
	machine     *replay.StateMachine
	cancel      context.CancelFunc

	// DashboardURL is what the frontend opens in an iframe.
	DashboardURL string

	// EventsDir is the directory the events.jsonl / transcript.jsonl
	// pair was loaded from. Top-level recording → scenarioDir;
	// archived recording → scenarioDir/recordings/<name>. handleStart
	// reuses it for the turns lookup so transcript turns line up with
	// the same events the state machine is replaying.
	EventsDir string

	// Recording is the archive name when this playback is replaying
	// an archived recording, or "" for the top-level (latest).
	Recording string

	// Paused tracks the UI-perceived state; the state machine handles
	// the actual pause via its command channel.
	Paused bool

	// Degraded is true when this playback's timeline was synthesized from
	// the transcript (no daemon-recorded events.jsonl sidecar). The arc is
	// reconstructed via the shared classifier engine, but without a sidecar
	// it can't be byte-faithful — the UI badges it so a reconstructed
	// timeline isn't mistaken for a recorded one.
	Degraded bool

	// enricher is the broadcaster decorator that populates
	// SessionState.Metrics on session events. Held on the Playback so
	// the Snapshot path (GET /api/v1/sessions) can apply the same
	// enrichment as the broadcast path (WebSocket stream) — otherwise
	// the dashboard's initial fetch shows empty metrics and only
	// catches up after the next state transition.
	enricher *metricsEnricher
}

// wsHandler is the subset of websocket.NewHub's return we exercise. The
// concrete type (websocket.hub) is unexported, so we can't name it
// directly — store it as an interface instead.
type wsHandler interface {
	ServeWS(http.ResponseWriter, *http.Request)
}

// PlaybackManager holds the at-most-one current playback plus the
// shared WebSocket hub that the dashboard connects to.
type PlaybackManager struct {
	repoRoot string

	mu      sync.Mutex
	current *Playback

	// hub + broadcaster live FOR THE MANAGER, not per-playback, so the
	// dashboard's persistent WebSocket connection survives playback
	// switches. Starting a new playback re-points the StateMachine at
	// the same broadcaster; subscribed clients see session_created /
	// session_deleted as the old playback's sessions roll off.
	broadcaster outbound.PushBroadcaster
	hub         wsHandler

	// diag exposes the logged broadcaster's ring buffer for the
	// /api/replay/diag endpoint. Same underlying value as broadcaster;
	// kept as a separate field so we don't lose the concrete type behind
	// the PushBroadcaster interface.
	diag *loggedBroadcaster

	// metricsEnabled toggles construction of a per-playback
	// metrics.Adapter inside the enricher. Always true for the real
	// viewer; tests can leave it false to keep their fixtures simple.
	// A fresh collector per playback is intentional: metrics.Adapter
	// caches per-transcript tailers, and after one pass the tailer's
	// lastOffset sits at EOF — reusing it on a re-replay would read
	// zero bytes and return total_tokens=0.
	metricsEnabled bool
}

// NewPlaybackManager wires the manager and its shared WebSocket hub.
// repoRoot is needed to read platforms/web/index.html for the embedded
// dashboard view.
//
// connectSnapshots: when a new WebSocket client (the dashboard inside
// the iframe) connects, we ship session_created for every currently
// live session. Without this, fast-speed playbacks (≥10×) burn through
// their events BEFORE the WS connection completes, leaving the
// dashboard stuck on "AWAITING SESSIONS". With it, late connections
// always see the current state.
func NewPlaybackManager(repoRoot string) *PlaybackManager {
	push := services.NewPushService()
	logged := newLoggedBroadcaster(push)
	m := &PlaybackManager{
		repoRoot:       repoRoot,
		broadcaster:    logged,
		diag:           logged,
		metricsEnabled: true,
	}
	m.hub = websocket.NewHub(logged, m.connectSnapshots)
	return m
}

// loggedBroadcaster wraps a PushBroadcaster and records the last N
// broadcasts in a ring buffer. Used by the viewer's diagnostic endpoint
// to verify what the state machine actually emitted vs. what the
// dashboard received over the wire.
type loggedBroadcaster struct {
	inner outbound.PushBroadcaster

	mu      sync.Mutex
	entries []broadcastEntry
}

type subscriberCounter interface {
	Subscribers() int
}

type broadcastEntry struct {
	Ts        time.Time `json:"ts"`
	Type      string    `json:"type"`
	SessionID string    `json:"session_id"`
	State     string    `json:"state"`
	SubCount  int       `json:"subs"`
}

const broadcastLogCap = 200

func newLoggedBroadcaster(inner outbound.PushBroadcaster) *loggedBroadcaster {
	return &loggedBroadcaster{
		inner:   inner,
		entries: make([]broadcastEntry, 0, broadcastLogCap),
	}
}

func (l *loggedBroadcaster) Broadcast(msg outbound.PushMessage) {
	// Normalize the broadcast session shape so live WS updates carry the
	// same fields as the initial /api/v1/sessions fetch. Without this,
	// the dashboard's Object.assign(a, s) on a session_updated rewrites
	// the agent's adapter from "claudecode" (set by handleSessions) back
	// to the raw "claude-code" string, losing the brand icon. We also
	// fill in ProjectName so freshly-arrived sessions land in the right
	// group instead of "unknown".
	if msg.Session != nil {
		cp := *msg.Session
		cp.Adapter = normalizeAdapter(cp.Adapter)
		if cp.ProjectName == "" {
			cp.ProjectName = inferProjectName(&cp)
		}
		msg.Session = &cp
	}
	entry := broadcastEntry{Ts: time.Now().UTC(), Type: msg.Type}
	if msg.Session != nil {
		entry.SessionID = msg.Session.SessionID
		entry.State = msg.Session.State
	}
	if sc, ok := l.inner.(subscriberCounter); ok {
		entry.SubCount = sc.Subscribers()
	}
	l.mu.Lock()
	if len(l.entries) >= broadcastLogCap {
		copy(l.entries, l.entries[1:])
		l.entries = l.entries[:broadcastLogCap-1]
	}
	l.entries = append(l.entries, entry)
	l.mu.Unlock()
	l.inner.Broadcast(msg)
}

func (l *loggedBroadcaster) Subscribe() chan outbound.PushMessage {
	return l.inner.Subscribe()
}

func (l *loggedBroadcaster) Unsubscribe(ch chan outbound.PushMessage) {
	l.inner.Unsubscribe(ch)
}

func (l *loggedBroadcaster) snapshot() []broadcastEntry {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]broadcastEntry, len(l.entries))
	copy(out, l.entries)
	return out
}

// connectSnapshots is invoked by the WebSocket hub when a new client
// attaches. It returns one session_created PushMessage per session
// currently in the active playback's state map, so a dashboard that
// connects mid-playback (or after a fast-speed playback completed)
// still renders the right view.
func (m *PlaybackManager) connectSnapshots() []outbound.PushMessage {
	snap := m.Snapshot()
	if len(snap) == 0 {
		return nil
	}
	out := make([]outbound.PushMessage, 0, len(snap))
	for _, s := range snap {
		s.Adapter = normalizeAdapter(s.Adapter)
		if s.ProjectName == "" {
			s.ProjectName = inferProjectName(s)
		}
		cp := *s
		out = append(out, outbound.PushMessage{
			Type:    outbound.PushTypeCreated,
			Session: &cp,
		})
	}
	return out
}

// Current returns the active playback, or nil if none.
func (m *PlaybackManager) Current() *Playback {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// Start a viewer-internal playback. Stops any existing playback first.
// recording: "" → top-level events.jsonl; non-empty → archived recording
// under <scenarioDir>/recordings/<recording>/.
func (m *PlaybackManager) StartViewerInternal(agent, subtree, scenario string, speed float64, recording string) (*Playback, error) {
	if !slugRE.MatchString(agent) || !slugRE.MatchString(scenario) {
		return nil, fmt.Errorf("invalid agent or scenario id")
	}
	if subtree != "scenarios" && subtree != "regression" {
		return nil, fmt.Errorf("subtree must be 'scenarios' or 'regression'")
	}
	scenarioDir := filepath.Join(m.repoRoot, "replaydata", "agents", agent, subtree, scenario)
	eventsDir := scenarioDir
	if recording != "" {
		if !archiveNameRE.MatchString(recording) {
			return nil, fmt.Errorf("invalid recording archive name")
		}
		eventsDir = filepath.Join(scenarioDir, "recordings", recording)
		if _, err := os.Stat(filepath.Join(eventsDir, "events.jsonl")); err != nil {
			return nil, fmt.Errorf("archive %q has no events.jsonl", recording)
		}
	}
	events, degraded, err := replay.LoadEventsOrSynthesize(eventsDir, agent)
	if err != nil {
		return nil, fmt.Errorf("load events: %w", err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("scenario %s has neither events.jsonl nor a usable transcript", scenario)
	}

	m.stopCurrent()

	// Per-playback metrics enricher: wraps the manager's shared
	// broadcaster so each session row carries model / tokens / cost /
	// context % from the recorded transcript.jsonl. Fresh wrapper per
	// playback so its sessionID cache doesn't bleed across recordings.
	var bc outbound.PushBroadcaster = m.broadcaster
	var enricher *metricsEnricher
	if m.metricsEnabled {
		enricher = newMetricsEnricher(m.broadcaster, buildMetricsCollector(), eventsDir)
		bc = enricher
	}
	machine := replay.New(events, bc, speed)
	// Apply event 0 synchronously BEFORE returning to the frontend so
	// the dashboard's initial /api/v1/sessions fetch sees a non-empty
	// snapshot. Otherwise there's a race window where Run()'s
	// goroutine hasn't yet scheduled and the dashboard renders
	// "AWAITING SESSIONS" until the next state change ~15s later.
	machine.PrimeFirstEvent()
	ctx, cancel := context.WithCancel(context.Background())

	pb := &Playback{
		ID:           newPlaybackID(),
		Agent:        agent,
		Subtree:      subtree,
		Scenario:     scenario,
		Mode:         "viewer-internal",
		Speed:        speed,
		StartedAt:    time.Now().UTC(),
		broadcaster:  m.broadcaster,
		machine:      machine,
		cancel:       cancel,
		DashboardURL: "/dashboard",
		EventsDir:    eventsDir,
		Recording:    recording,
		Degraded:     degraded,
		enricher:     enricher,
	}

	m.mu.Lock()
	m.current = pb
	m.mu.Unlock()

	go func() {
		machine.Run(ctx)
	}()
	return pb, nil
}

// Pause halts the current playback's emission.
func (m *PlaybackManager) Pause() {
	m.mu.Lock()
	pb := m.current
	m.mu.Unlock()
	if pb != nil && pb.machine != nil {
		pb.machine.Pause()
		pb.Paused = true
	}
}

// Resume continues emission after Pause.
func (m *PlaybackManager) Resume() {
	m.mu.Lock()
	pb := m.current
	m.mu.Unlock()
	if pb != nil && pb.machine != nil {
		pb.machine.Resume()
		pb.Paused = false
	}
}

// Stop terminates the current playback.
func (m *PlaybackManager) Stop() {
	m.stopCurrent()
}

// SeekMs jumps the playhead to the given offset (ms from recording start).
// Named with the Ms suffix to avoid the io.Seeker.Seek collision the
// stdmethods linter flags on the bare `Seek` name.
func (m *PlaybackManager) SeekMs(offsetMs int64) {
	m.mu.Lock()
	pb := m.current
	m.mu.Unlock()
	if pb != nil && pb.machine != nil {
		pb.machine.SeekToOffset(offsetMs)
	}
}

// SetSpeed updates playback rate live.
func (m *PlaybackManager) SetSpeed(speed float64) {
	m.mu.Lock()
	pb := m.current
	m.mu.Unlock()
	if pb != nil && pb.machine != nil {
		pb.machine.SetSpeed(speed)
		pb.Speed = speed
	}
}

// Snapshot returns the current synthetic sessions (whatever the state
// machine has accumulated so far). Used by GET /api/v1/sessions.
// Sessions are enriched with metrics (model / tokens / cost / context %)
// when a per-playback enricher is configured, so the dashboard's
// initial fetch carries the same metrics shape the WebSocket stream
// emits — no "metrics-after-first-update" gap.
func (m *PlaybackManager) Snapshot() []*session.SessionState {
	m.mu.Lock()
	pb := m.current
	m.mu.Unlock()
	if pb == nil || pb.machine == nil {
		return nil
	}
	sessions := pb.machine.Snapshot()
	if pb.enricher != nil {
		for i, s := range sessions {
			if s.Metrics == nil {
				if mm := pb.enricher.lookup(s.SessionID, s.Adapter); mm != nil {
					cp := *s
					cp.Metrics = mm
					sessions[i] = &cp
				}
			}
		}
	}
	return sessions
}

// stopCurrent cancels the current playback and clears it. Safe to call
// when there is no current playback.
func (m *PlaybackManager) stopCurrent() {
	m.mu.Lock()
	pb := m.current
	m.current = nil
	m.mu.Unlock()
	if pb == nil {
		return
	}
	if pb.cancel != nil {
		pb.cancel()
	}
	if pb.machine != nil {
		<-pb.machine.Done()
	}
	// Tell the dashboard that everything is gone.
	for _, s := range pb.Snapshot() {
		m.broadcaster.Broadcast(outbound.PushMessage{
			Type:    outbound.PushTypeDeleted,
			Session: s,
		})
	}
}

// Snapshot helper on Playback for the stopCurrent close-down loop.
func (p *Playback) Snapshot() []*session.SessionState {
	if p.machine == nil {
		return nil
	}
	return p.machine.Snapshot()
}

func newPlaybackID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// =====================================================================
// HTTP handlers
// =====================================================================

// registerPlaybackRoutes attaches all replay-related routes onto mux.
func (m *PlaybackManager) registerPlaybackRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/replay/start", m.handleStart)
	mux.HandleFunc("POST /api/replay/pause", m.handlePause)
	mux.HandleFunc("POST /api/replay/resume", m.handleResume)
	mux.HandleFunc("POST /api/replay/stop", m.handleStop)
	mux.HandleFunc("POST /api/replay/seek", m.handleSeek)
	mux.HandleFunc("POST /api/replay/speed", m.handleSpeed)
	mux.HandleFunc("GET /api/replay/status", m.handleStatus)
	mux.HandleFunc("GET /api/replay/diag", m.handleDiag)

	// Daemon-compatible endpoints the embedded dashboard consumes.
	mux.HandleFunc("GET /api/v1/agents", m.handleAgents)
	mux.HandleFunc("GET /api/v1/sessions", m.handleSessions)
	mux.HandleFunc("GET /api/v1/sessions/stream", m.hub.ServeWS)

	mux.HandleFunc("GET /dashboard", m.handleDashboard)
}

type startReq struct {
	Agent    string  `json:"agent"`
	Subtree  string  `json:"subtree"`
	Scenario string  `json:"scenario"`
	Mode     string  `json:"mode"`
	Speed    float64 `json:"speed"`
	// Recording is the archive name under <scenarioDir>/recordings/.
	// Empty (or absent) means replay the top-level events.jsonl.
	Recording string `json:"recording,omitempty"`
}

type startResp struct {
	PlaybackID   string               `json:"playback_id"`
	DashboardURL string               `json:"dashboard_url"`
	Mode         string               `json:"mode"`
	TotalMs      int64                `json:"total_ms"`
	Events       []replay.EventMarker `json:"events,omitempty"`
	Turns        []replay.TurnMarker  `json:"turns,omitempty"`
}

func (m *PlaybackManager) handleStart(w http.ResponseWriter, r *http.Request) {
	var req startReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Speed <= 0 {
		req.Speed = 1.0
	}
	if req.Mode == "" {
		req.Mode = "viewer-internal"
	}

	// Mode is currently a single-value field reserved for future
	// extension (e.g. a real isolated-daemon mode someday). Anything
	// other than empty / "viewer-internal" is rejected so a caller
	// doesn't silently get a different behavior than they expected.
	if req.Mode != "" && req.Mode != "viewer-internal" {
		http.Error(w, "unsupported mode: "+req.Mode, http.StatusBadRequest)
		return
	}
	pb, err := m.StartViewerInternal(req.Agent, req.Subtree, req.Scenario, req.Speed, req.Recording)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Load turns from the same directory the events came from so the
	// transcript-derived turn lane aligns with the replay timeline. For
	// archives, that's <scenarioDir>/recordings/<name>; for latest, the
	// scenario dir itself.
	turns := replay.LoadTurnMarkers(pb.EventsDir, pb.machine.Anchor())
	writeJSON(w, startResp{
		PlaybackID: pb.ID, DashboardURL: pb.DashboardURL, Mode: pb.Mode,
		TotalMs: pb.machine.TotalDurationMs(),
		Events:  pb.machine.EventMarkers(),
		Turns:   turns,
	})
}

func (m *PlaybackManager) handlePause(w http.ResponseWriter, r *http.Request) {
	m.Pause()
	w.WriteHeader(http.StatusNoContent)
}

func (m *PlaybackManager) handleResume(w http.ResponseWriter, r *http.Request) {
	m.Resume()
	w.WriteHeader(http.StatusNoContent)
}

func (m *PlaybackManager) handleStop(w http.ResponseWriter, r *http.Request) {
	m.Stop()
	w.WriteHeader(http.StatusNoContent)
}

func (m *PlaybackManager) handleSeek(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("offset_ms")
	n, err := strconv.ParseInt(q, 10, 64)
	if err != nil || n < 0 {
		http.Error(w, "offset_ms required and non-negative", http.StatusBadRequest)
		return
	}
	m.SeekMs(n)
	w.WriteHeader(http.StatusNoContent)
}

func (m *PlaybackManager) handleSpeed(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("speed")
	f, err := strconv.ParseFloat(q, 64)
	if err != nil || f <= 0 {
		http.Error(w, "speed required and >0", http.StatusBadRequest)
		return
	}
	m.SetSpeed(f)
	w.WriteHeader(http.StatusNoContent)
}

func (m *PlaybackManager) handleStatus(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	pb := m.current
	m.mu.Unlock()
	if pb == nil {
		writeJSON(w, map[string]any{"active": false})
		return
	}
	resp := map[string]any{
		"active":      true,
		"playback_id": pb.ID,
		"agent":       pb.Agent,
		"subtree":     pb.Subtree,
		"scenario":    pb.Scenario,
		"mode":        pb.Mode,
		"speed":       pb.Speed,
		"paused":      pb.Paused,
		"degraded":    pb.Degraded,
	}
	if pb.machine != nil {
		// offset_ms is the live wall-clock-driven scrubber position so
		// the bar advances every poll instead of freezing between events.
		// cursor_offset_ms is the last-applied-event timestamp, kept for
		// debugging and as a future "snap to event" affordance.
		resp["offset_ms"] = pb.machine.LivePlayheadMs()
		resp["cursor_offset_ms"] = pb.machine.CursorOffsetMs()
		resp["total_ms"] = pb.machine.TotalDurationMs()
	}
	resp["dashboard_url"] = pb.DashboardURL
	writeJSON(w, resp)
}

// handleAgents returns the same shape as the daemon's /api/v1/agents
// endpoint, sourced from agents.All() so the dashboard renders each
// session row with the real per-adapter brand icon (Claude Code, Codex,
// Pi, aider, OpenCode) instead of a grey-circle stub. Names are
// normalized to match the spellings handleSessions writes onto
// SessionState.Adapter (e.g. "claude-code" → "claudecode") so the
// dashboard's session-to-agent join keys correctly.
func (m *PlaybackManager) handleAgents(w http.ResponseWriter, r *http.Request) {
	type agentEntry struct {
		Name         string `json:"name"`
		DisplayName  string `json:"display_name"`
		IconSVGLight string `json:"icon_svg_light"`
		IconSVGDark  string `json:"icon_svg_dark"`
	}
	all := agents.All()
	entries := make([]agentEntry, 0, len(all))
	for _, a := range all {
		entries = append(entries, agentEntry{
			Name:         normalizeAdapter(a.Identity.Name),
			DisplayName:  a.Identity.DisplayName,
			IconSVGLight: a.Identity.IconSVGLight,
			IconSVGDark:  a.Identity.IconSVGDark,
		})
	}
	writeJSON(w, entries)
}

// normalizeAdapter maps the various adapter-name spellings present in
// historical recordings to the canonical slug the agents-endpoint
// returns. Recordings made before #319's adapter-name unification used
// "claude-code" (hyphenated); recent ones use "claudecode". The
// dashboard joins sessions to agents by string equality, so a mismatch
// here means the session row renders without its branded icon.
func normalizeAdapter(a string) string {
	switch a {
	case "claude-code":
		return "claudecode"
	case "":
		return "claudecode"
	}
	return a
}

// handleSessions returns the current synthetic session list in the EXACT
// shape the daemon's GET /api/v1/sessions produces: an object with a `groups`
// array (session.BuildDashboard output). The real daemon also carries
// `provider_costs`; the replay viewer has no cost tracker, so that field is
// omitted. Reuse rather than reinvent so the dashboard's render path Just
// Works against our synthetic state.
func (m *PlaybackManager) handleSessions(w http.ResponseWriter, r *http.Request) {
	// Normalize adapter spellings BEFORE BuildDashboard runs so the
	// dashboard's session→agent matching keys correctly. Historical
	// recordings carry "claude-code" (hyphenated); the dashboard's
	// /api/v1/agents list uses "claudecode".
	snap := m.Snapshot()
	for _, s := range snap {
		s.Adapter = normalizeAdapter(s.Adapter)
		// ProjectName drives the dashboard's group name. Recordings
		// without one collapse under "unknown"; fill it in from the
		// transcript path's parent dir so each session shows up under
		// its own row.
		if s.ProjectName == "" {
			s.ProjectName = inferProjectName(s)
		}
	}
	groups := session.BuildDashboard(snap, nil)
	if groups == nil {
		// The dashboard's initial fetch handler bails on `null` (early
		// return; never sets dashboardGroups). Coerce to an empty array
		// so the frontend keeps a valid state and the WebSocket can
		// hydrate it from session_created messages.
		groups = []*session.AgentGroup{}
	}
	writeJSON(w, map[string]any{"groups": groups})
}

// inferProjectName derives a fallback project name from the session's
// transcript path or CWD so the dashboard doesn't lump every replayed
// session under "unknown".
func inferProjectName(s *session.SessionState) string {
	if s.CWD != "" {
		return filepath.Base(s.CWD)
	}
	if s.TranscriptPath != "" {
		return filepath.Base(filepath.Dir(s.TranscriptPath))
	}
	return "replay"
}

// handleDashboard serves the live session dashboard. There are two
// frontends in this repo and exactly one is authoritative for the live
// session view: platforms/web/index.html — the SAME dashboard the daemon
// ships. The viewer reads it from the repo root at request time so a
// `git pull` of dashboard changes Just Works without rebuilding the
// viewer. (The viewer's OWN embedded internal/viewer/web/ SPA is a
// separate, deliberate surface — the catalog / scenario browser — and is
// never the live session UI; see the package doc in server.go.)
//
// We inject a tiny non-invasive "ws-diag" script that wraps
// window.WebSocket so every received message is mirrored to the console
// with prefix "[ws-diag]", letting us compare server broadcasts
// (/api/replay/diag) against what the iframe actually receives — without
// editing the authoritative production index.html.
func (m *PlaybackManager) handleDashboard(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(m.repoRoot, "platforms", "web", "index.html")
	b, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, fmt.Sprintf("could not read %s: %v", path, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(injectBeforeClosingTag(string(b), wsDiagScript)))
}

// injectBeforeClosingTag inserts script just before the first closing
// </head> (falling back to </body>), matched case-insensitively. This
// replaces a brittle exact-string `strings.Index(html, "</head>")` splice:
// if the dashboard markup ever changes the tag's casing or drops the
// <head>, the diagnostic script is appended at the end and the divergence
// is logged, instead of being silently dropped.
func injectBeforeClosingTag(html, script string) string {
	lower := strings.ToLower(html)
	for _, tag := range []string{"</head>", "</body>"} {
		if i := strings.Index(lower, tag); i >= 0 {
			return html[:i] + script + html[i:]
		}
	}
	logViewerError("handleDashboard: dashboard HTML has no </head> or </body>; appending ws-diag script at end")
	return html + script
}

// handleDiag returns the recent broadcast log captured by the
// loggedBroadcaster decorator. Pair this output with the dashboard
// iframe's console (the wsDiagScript prints every received message) to
// see where messages diverge between server and client.
func (m *PlaybackManager) handleDiag(w http.ResponseWriter, r *http.Request) {
	subs := 0
	if l := m.diag; l != nil {
		if sc, ok := l.inner.(subscriberCounter); ok {
			subs = sc.Subscribers()
		}
	}
	out := map[string]any{
		"subscribers_now": subs,
	}
	if m.diag != nil {
		out["broadcasts"] = m.diag.snapshot()
	}
	writeJSON(w, out)
}

// wsDiagScript is injected into the served dashboard HTML to mirror every
// WebSocket message to console.debug with prefix "[ws-diag]". It is a
// non-invasive wrapper: the dashboard's `new WebSocket(...)` call hits
// the wrapper, which forwards to the real constructor and attaches a
// listener BEFORE returning to caller. The dashboard's own onmessage
// handler still fires normally.
const wsDiagScript = `<script>
(function(){
  if (typeof window === 'undefined' || !window.WebSocket) return;
  var Orig = window.WebSocket;
  function Tapped(url, protocols) {
    var s = protocols === undefined ? new Orig(url) : new Orig(url, protocols);
    try {
      console.debug('[ws-diag] open(req)', url);
      s.addEventListener('open', function(){ console.debug('[ws-diag] open'); });
      s.addEventListener('close', function(e){ console.debug('[ws-diag] close', e && e.code, e && e.reason); });
      s.addEventListener('error', function(){ console.debug('[ws-diag] error'); });
      s.addEventListener('message', function(e){
        var preview = e && e.data ? String(e.data).slice(0, 220) : '';
        console.debug('[ws-diag] msg', preview);
      });
    } catch (err) {
      console.debug('[ws-diag] tap-failed', err && err.message);
    }
    return s;
  }
  Tapped.prototype = Orig.prototype;
  for (var k in Orig) { try { Tapped[k] = Orig[k]; } catch(_) {} }
  window.WebSocket = Tapped;
})();
</script>
`
