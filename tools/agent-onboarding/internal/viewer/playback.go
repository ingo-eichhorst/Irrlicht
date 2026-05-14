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
	"strconv"
	"sync"
	"time"

	"irrlicht/core/adapters/outbound/websocket"
	"irrlicht/core/application/services"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
	"irrlicht/tools/agent-onboarding/internal/replay"
)

// Playback represents one active replay session. Only one Playback may
// be active per Manager at a time — starting a new one stops the
// previous, mirroring how a real daemon serves one set of sessions.
type Playback struct {
	ID       string
	Agent    string
	Subtree  string // "scenarios" | "regression"
	Scenario string
	Mode     string // "viewer-internal" | "isolated-daemon"
	Speed    float64

	StartedAt time.Time

	// Viewer-internal mode plumbing.
	broadcaster outbound.PushBroadcaster
	machine     *replay.StateMachine
	cancel      context.CancelFunc

	// DashboardURL is what the frontend opens in an iframe. For
	// viewer-internal mode it's `/dashboard`; for isolated-daemon it's
	// `http://127.0.0.1:<port>/`.
	DashboardURL string

	// Isolated-daemon mode plumbing (populated by daemon_launcher).
	DaemonPort int

	// Paused tracks the UI-perceived state; the state machine handles
	// the actual pause via its command channel.
	Paused bool
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
}

// NewPlaybackManager wires the manager and its shared WebSocket hub.
// repoRoot is needed to read platforms/web/index.html for the embedded
// dashboard view.
func NewPlaybackManager(repoRoot string) *PlaybackManager {
	broadcaster := services.NewPushService()
	hub := websocket.NewHub(broadcaster, nil) // no per-session history snapshots in replay mode
	return &PlaybackManager{
		repoRoot:    repoRoot,
		broadcaster: broadcaster,
		hub:         hub,
	}
}

// Current returns the active playback, or nil if none.
func (m *PlaybackManager) Current() *Playback {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.current
}

// Start a viewer-internal playback. Stops any existing playback first.
func (m *PlaybackManager) StartViewerInternal(agent, subtree, scenario string, speed float64) (*Playback, error) {
	if !slugRE.MatchString(agent) || !slugRE.MatchString(scenario) {
		return nil, fmt.Errorf("invalid agent or scenario id")
	}
	if subtree != "scenarios" && subtree != "regression" {
		return nil, fmt.Errorf("subtree must be 'scenarios' or 'regression'")
	}
	eventsPath := filepath.Join(m.repoRoot, "replaydata", "agents", agent, subtree, scenario, "events.jsonl")
	events, err := replay.LoadEvents(eventsPath)
	if err != nil {
		return nil, fmt.Errorf("load events: %w", err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("scenario has no events.jsonl entries")
	}

	m.stopCurrent()

	machine := replay.New(events, m.broadcaster, speed)
	// Apply event 0 synchronously BEFORE returning to the frontend so
	// the dashboard's initial /api/v1/sessions fetch sees a non-empty
	// snapshot. Otherwise there's a race window where Run()'s
	// goroutine hasn't yet scheduled and the dashboard renders
	// "AWAITING SESSIONS" until the next state change ~15s later.
	machine.PrimeFirstEvent()
	ctx, cancel := context.WithCancel(context.Background())

	pb := &Playback{
		ID:          newPlaybackID(),
		Agent:       agent,
		Subtree:     subtree,
		Scenario:    scenario,
		Mode:        "viewer-internal",
		Speed:       speed,
		StartedAt:   time.Now().UTC(),
		broadcaster: m.broadcaster,
		machine:     machine,
		cancel:      cancel,
		DashboardURL:   "/dashboard",
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
func (m *PlaybackManager) Snapshot() []*session.SessionState {
	m.mu.Lock()
	pb := m.current
	m.mu.Unlock()
	if pb == nil || pb.machine == nil {
		return nil
	}
	return pb.machine.Snapshot()
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
}

type startResp struct {
	PlaybackID   string `json:"playback_id"`
	DashboardURL string `json:"dashboard_url"`
	Mode         string `json:"mode"`
	TotalMs      int64  `json:"total_ms"`
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

	switch req.Mode {
	case "viewer-internal":
		pb, err := m.StartViewerInternal(req.Agent, req.Subtree, req.Scenario, req.Speed)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, startResp{
			PlaybackID: pb.ID, DashboardURL: pb.DashboardURL, Mode: pb.Mode,
			TotalMs: pb.machine.TotalDurationMs(),
		})

	case "isolated-daemon":
		pb, err := m.StartIsolatedDaemon(req.Agent, req.Subtree, req.Scenario, req.Speed)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, startResp{
			PlaybackID: pb.ID, DashboardURL: pb.DashboardURL, Mode: pb.Mode,
		})

	default:
		http.Error(w, "mode must be 'viewer-internal' or 'isolated-daemon'", http.StatusBadRequest)
	}
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
	if pb.DaemonPort > 0 {
		resp["daemon_port"] = pb.DaemonPort
		resp["dashboard_url"] = pb.DashboardURL
	}
	writeJSON(w, resp)
}

// handleAgents returns a minimal agent metadata list compatible with the
// dashboard's /api/v1/agents consumer. Synthesized from a hardcoded
// adapter table here — the daemon's real list isn't reachable from the
// viewer, and we only need enough fidelity for the dashboard to render
// session rows correctly.
func (m *PlaybackManager) handleAgents(w http.ResponseWriter, r *http.Request) {
	type agentEntry struct {
		Name         string `json:"name"`
		DisplayName  string `json:"display_name"`
		IconSVGLight string `json:"icon_svg_light"`
		IconSVGDark  string `json:"icon_svg_dark"`
	}
	entries := []agentEntry{
		{Name: "claudecode", DisplayName: "Claude Code", IconSVGLight: stubSVG, IconSVGDark: stubSVG},
		{Name: "codex", DisplayName: "Codex", IconSVGLight: stubSVG, IconSVGDark: stubSVG},
		{Name: "aider", DisplayName: "aider", IconSVGLight: stubSVG, IconSVGDark: stubSVG},
		{Name: "pi", DisplayName: "Pi", IconSVGLight: stubSVG, IconSVGDark: stubSVG},
		{Name: "opencode", DisplayName: "OpenCode", IconSVGLight: stubSVG, IconSVGDark: stubSVG},
	}
	writeJSON(w, entries)
}

const stubSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 14 14"><circle cx="7" cy="7" r="5" fill="#888"/></svg>`

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

// handleSessions returns the current synthetic session list in the
// EXACT shape the dashboard expects — which is whatever the daemon's
// session.BuildDashboard produces. Reuse rather than reinvent so the
// dashboard's render path Just Works against our synthetic state.
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
	writeJSON(w, groups)
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

// handleDashboard serves the embedded irrlicht dashboard. Reads
// platforms/web/index.html from the repo root at request time so a
// `git pull` of dashboard changes Just Works without restarting the
// viewer.
func (m *PlaybackManager) handleDashboard(w http.ResponseWriter, r *http.Request) {
	path := filepath.Join(m.repoRoot, "platforms", "web", "index.html")
	b, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, fmt.Sprintf("could not read %s: %v", path, err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}
