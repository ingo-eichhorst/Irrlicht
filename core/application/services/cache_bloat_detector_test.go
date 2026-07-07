package services

import (
	"encoding/json"
	"strings"
	"testing"

	"irrlicht/core/domain/lifecycle"
	"irrlicht/core/domain/session"
)

const testNow int64 = 2_000_000_000

type fakeLister struct{ sessions []*session.SessionState }

func (f *fakeLister) ListAll() ([]*session.SessionState, error) { return f.sessions, nil }

type fakeRecorder struct{ events []lifecycle.Event }

func (f *fakeRecorder) Record(ev lifecycle.Event) { f.events = append(f.events, ev) }

func testCfg() CacheBloatConfig {
	return CacheBloatConfig{BaselineDays: 14, Threshold: 1.4, VersionDeltaTokens: 10000, MinTurns: 3}
}

func newDetector(lister sessionLister, rec cacheBloatRecorder, cfg CacheBloatConfig) *CacheBloatDetector {
	d := NewCacheBloatDetector(lister, rec, cfg)
	d.now = func() int64 { return testNow }
	return d
}

// histSession builds a completed session for the baseline/version history.
func histSession(project, adapter, version string, turns int, perTurn, ageSecs int64) *session.SessionState {
	return &session.SessionState{
		ProjectName: project,
		Adapter:     adapter,
		UpdatedAt:   testNow - ageSecs,
		Metrics: &session.SessionMetrics{
			AgentVersion:           version,
			CompletedTurns:         turns,
			CumCacheCreationTokens: perTurn * int64(turns),
		},
	}
}

func liveSession(project, adapter, version string) *session.SessionState {
	return &session.SessionState{
		SessionID:   "live-1",
		ProjectName: project,
		Adapter:     adapter,
		Metrics:     &session.SessionMetrics{AgentVersion: version},
	}
}

// driveTurns simulates n completed turns, each adding perTurn cache-creation
// tokens: a working pass (IsAgentDone=false) then a turn_done pass (rising edge).
func driveTurns(d *CacheBloatDetector, st *session.SessionState, perTurn int64, n int) {
	for i := 0; i < n; i++ {
		st.Metrics.LastEventType = "user_message"
		d.OnActivity(st)
		st.Metrics.CumCacheCreationTokens += perTurn
		st.Metrics.LastEventType = "turn_done"
		d.OnActivity(st)
	}
}

// Scenario A: two adapter versions, the newer one bloated → glyph fires,
// tooltip names the version, exactly one cache_bloat_detected event.
func TestCacheBloat_ScenarioA_VersionAttribution(t *testing.T) {
	hist := []*session.SessionState{
		histSession("proj", "claude-code", "1.0.0", 4, 5000, 10*secondsPerDay),
		histSession("proj", "claude-code", "1.0.0", 4, 5000, 9*secondsPerDay),
		histSession("proj", "claude-code", "1.0.0", 4, 5000, 8*secondsPerDay),
		histSession("proj", "claude-code", "2.0.0", 4, 20000, 2*secondsPerDay),
		histSession("proj", "claude-code", "2.0.0", 4, 20000, 1*secondsPerDay),
		histSession("proj", "claude-code", "2.0.0", 4, 20000, 1*secondsPerDay),
	}
	rec := &fakeRecorder{}
	d := newDetector(&fakeLister{hist}, rec, testCfg())

	live := liveSession("proj", "claude-code", "2.0.0")
	driveTurns(d, live, 20000, 3)

	if !live.Metrics.CacheBloat {
		t.Fatal("expected CacheBloat glyph to fire")
	}
	tip := live.Metrics.CacheBloatTooltip
	if !strings.Contains(tip, "2.0.0") || !strings.Contains(tip, "1.0.0") || !strings.Contains(tip, "claude-code") {
		t.Errorf("tooltip should name both versions and adapter, got %q", tip)
	}
	explanation := live.Metrics.CacheBloatExplanation
	if !strings.Contains(explanation, tip) {
		t.Errorf("explanation should fold in the tooltip verbatim, got %q", explanation)
	}
	if !strings.Contains(explanation, "prompt-cache tokens well above normal") {
		t.Errorf("explanation missing the plain-language base sentence, got %q", explanation)
	}
	if len(rec.events) != 1 {
		t.Fatalf("expected exactly 1 event, got %d", len(rec.events))
	}
	ev := rec.events[0]
	if ev.Kind != lifecycle.KindCacheBloatDetected {
		t.Errorf("wrong event kind: %s", ev.Kind)
	}
	if ev.RegressingVersion != "2.0.0" || ev.PriorVersion != "1.0.0" {
		t.Errorf("version fields wrong: regressing=%q prior=%q", ev.RegressingVersion, ev.PriorVersion)
	}
	if ev.DeltaTokens != 15000 {
		t.Errorf("delta_tokens = %d, want 15000", ev.DeltaTokens)
	}
	if ev.Project != "proj" || ev.Adapter != "claude-code" {
		t.Errorf("project/adapter wrong: %q %q", ev.Project, ev.Adapter)
	}
}

// Scenario B: one adapter version, bloated → glyph fires WITHOUT version
// attribution (no false naming).
func TestCacheBloat_ScenarioB_NoFalseAttribution(t *testing.T) {
	hist := []*session.SessionState{
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 5*secondsPerDay),
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 4*secondsPerDay),
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 3*secondsPerDay),
	}
	rec := &fakeRecorder{}
	d := newDetector(&fakeLister{hist}, rec, testCfg())

	live := liveSession("proj", "claude-code", "1.0.0")
	driveTurns(d, live, 10000, 3)

	if !live.Metrics.CacheBloat {
		t.Fatal("expected CacheBloat glyph to fire")
	}
	if live.Metrics.CacheBloatTooltip != "" {
		t.Errorf("tooltip must be empty (no attribution), got %q", live.Metrics.CacheBloatTooltip)
	}
	if strings.Contains(live.Metrics.CacheBloatExplanation, "Likely tied to") {
		t.Errorf("explanation must not fabricate an attribution, got %q", live.Metrics.CacheBloatExplanation)
	}
	if live.Metrics.CacheBloatExplanation == "" {
		t.Error("explanation should still be set when the glyph fires, even without attribution")
	}
	if len(rec.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(rec.events))
	}
	if rec.events[0].RegressingVersion != "" {
		t.Errorf("regressing_version must be empty, got %q", rec.events[0].RegressingVersion)
	}
}

// Scenario C: threshold=0 disables the rule entirely (kill switch).
func TestCacheBloat_ScenarioC_KillSwitch(t *testing.T) {
	hist := []*session.SessionState{
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 3*secondsPerDay),
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 2*secondsPerDay),
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 1*secondsPerDay),
	}
	cfg := testCfg()
	cfg.Threshold = 0
	rec := &fakeRecorder{}
	d := newDetector(&fakeLister{hist}, rec, cfg)

	live := liveSession("proj", "claude-code", "1.0.0")
	driveTurns(d, live, 1_000_000, 6) // absurdly bloated

	if live.Metrics.CacheBloat {
		t.Error("kill switch: glyph must not fire")
	}
	if len(rec.events) != 0 {
		t.Errorf("kill switch: no events, got %d", len(rec.events))
	}
}

// Scenario D: only 2 completed turns with high cache_creation → no glyph
// (variance guard fires below MinTurns).
func TestCacheBloat_ScenarioD_VarianceGuard(t *testing.T) {
	hist := []*session.SessionState{
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 3*secondsPerDay),
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 2*secondsPerDay),
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 1*secondsPerDay),
	}
	rec := &fakeRecorder{}
	d := newDetector(&fakeLister{hist}, rec, testCfg())

	live := liveSession("proj", "claude-code", "1.0.0")
	driveTurns(d, live, 100000, 2) // 2 turns only

	if live.Metrics.CacheBloat {
		t.Error("variance guard: glyph must not fire below MinTurns")
	}
	if live.Metrics.CompletedTurns != 2 {
		t.Errorf("CompletedTurns = %d, want 2", live.Metrics.CompletedTurns)
	}
	if len(rec.events) != 0 {
		t.Errorf("variance guard: no events, got %d", len(rec.events))
	}
}

// Dedupe: repeated detection on the same (project, version) emits one event.
func TestCacheBloat_DedupePerProjectVersion(t *testing.T) {
	hist := []*session.SessionState{
		histSession("proj", "claude-code", "1.0.0", 4, 5000, 10*secondsPerDay),
		histSession("proj", "claude-code", "1.0.0", 4, 5000, 9*secondsPerDay),
		histSession("proj", "claude-code", "2.0.0", 4, 20000, 2*secondsPerDay),
		histSession("proj", "claude-code", "2.0.0", 4, 20000, 1*secondsPerDay),
	}
	rec := &fakeRecorder{}
	d := newDetector(&fakeLister{hist}, rec, testCfg())

	live := liveSession("proj", "claude-code", "2.0.0")
	driveTurns(d, live, 20000, 8) // keep tripping

	if len(rec.events) != 1 {
		t.Fatalf("expected 1 event despite repeated detection, got %d", len(rec.events))
	}
}

// Baseline lookback: sessions older than the window are excluded, so a stale
// low baseline can't suppress detection (and vice versa).
func TestCacheBloat_BaselineExcludesOldSessions(t *testing.T) {
	hist := []*session.SessionState{
		// In-window, low → baseline ~5000.
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 3*secondsPerDay),
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 2*secondsPerDay),
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 1*secondsPerDay),
		// Out-of-window, huge — must be ignored (else baseline balloons and
		// the live session would never trip).
		histSession("proj", "claude-code", "1.0.0", 5, 10_000_000, 30*secondsPerDay),
	}
	rec := &fakeRecorder{}
	d := newDetector(&fakeLister{hist}, rec, testCfg())

	live := liveSession("proj", "claude-code", "1.0.0")
	driveTurns(d, live, 10000, 3)

	if !live.Metrics.CacheBloat {
		t.Error("old huge session should be excluded from baseline; rule should trip")
	}
}

// The glyph clears on a later turn boundary once the rolling median drops back
// under the threshold (the rule is re-evaluated every turn).
func TestCacheBloat_ClearsWhenMedianDropsBack(t *testing.T) {
	hist := []*session.SessionState{
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 3*secondsPerDay),
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 2*secondsPerDay),
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 1*secondsPerDay),
	}
	d := newDetector(&fakeLister{hist}, &fakeRecorder{}, testCfg())

	live := liveSession("proj", "claude-code", "1.0.0")
	driveTurns(d, live, 20000, 3) // baseline 5000, median 20000 → trips
	if !live.Metrics.CacheBloat {
		t.Fatal("expected glyph to fire after bloated turns")
	}
	driveTurns(d, live, 1000, 4) // median falls to 1000 → below 5000×1.4
	if live.Metrics.CacheBloat {
		t.Error("glyph should clear once median drops back under threshold")
	}
	if live.Metrics.CacheBloatTooltip != "" {
		t.Errorf("tooltip should clear too, got %q", live.Metrics.CacheBloatTooltip)
	}
	if live.Metrics.CacheBloatExplanation != "" {
		t.Errorf("explanation should clear too, got %q", live.Metrics.CacheBloatExplanation)
	}
}

// A bloated prior snapshot of the SAME session must not raise the baseline it's
// judged against (self-exclusion). With few samples, including it would lift
// p25 enough to mask the regression.
func TestCacheBloat_ExcludesSelfFromBaseline(t *testing.T) {
	selfSnapshot := histSession("proj", "claude-code", "1.0.0", 4, 20000, 1*secondsPerDay)
	selfSnapshot.SessionID = "live-1" // same id as the live session
	hist := []*session.SessionState{
		histSession("proj", "claude-code", "1.0.0", 4, 5000, 2*secondsPerDay), // SID ""
		selfSnapshot,
	}
	d := newDetector(&fakeLister{hist}, &fakeRecorder{}, testCfg())

	live := liveSession("proj", "claude-code", "1.0.0") // SessionID "live-1"
	driveTurns(d, live, 10000, 3)                       // median 10000

	// Excluding self → baseline p25 = 5000 → trips (10000 > 7000). Including
	// self (20000) would push p25 to ~8750 → trip needs >12250 → no trip.
	if !live.Metrics.CacheBloat {
		t.Error("self snapshot must be excluded from baseline; rule should trip")
	}
}

type capturingLogger struct {
	eventType, sessionID, message string
	calls                         int
}

func (c *capturingLogger) LogInfo(eventType, sessionID, message string) {
	c.eventType, c.sessionID, c.message = eventType, sessionID, message
	c.calls++
}
func (c *capturingLogger) LogError(string, string, string)                      {}
func (c *capturingLogger) LogProcessingTime(string, string, int64, int, string) {}
func (c *capturingLogger) Close() error                                         { return nil }

// The events.log sink writes a parseable cache_bloat_detected JSON line
// carrying the issue's field set — the ir:agent-releases consumer's contract.
func TestLoggerCacheBloatSink_WritesStructuredEvent(t *testing.T) {
	lg := &capturingLogger{}
	sink := NewLoggerCacheBloatSink(lg)
	sink.Record(lifecycle.Event{
		Kind:              lifecycle.KindCacheBloatDetected,
		SessionID:         "sess-9",
		Adapter:           "claude-code",
		Project:           "proj",
		RegressingVersion: "2.0.0",
		PriorVersion:      "1.0.0",
		DeltaTokens:       15000,
		BaselineMedian:    5000,
		CurrentMedian:     20000,
	})
	if lg.calls != 1 {
		t.Fatalf("expected 1 LogInfo call, got %d", lg.calls)
	}
	if lg.eventType != "cache_bloat_detected" {
		t.Errorf("eventType = %q, want cache_bloat_detected", lg.eventType)
	}
	if lg.sessionID != "sess-9" {
		t.Errorf("sessionID = %q", lg.sessionID)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lg.message), &got); err != nil {
		t.Fatalf("message is not valid JSON: %v (%q)", err, lg.message)
	}
	for k, want := range map[string]any{
		"project": "proj", "adapter": "claude-code",
		"regressing_version": "2.0.0", "prior_version": "1.0.0",
		"delta_tokens": float64(15000), "session_id": "sess-9",
	} {
		if got[k] != want {
			t.Errorf("field %q = %v, want %v", k, got[k], want)
		}
	}
}

// A different adapter's history must not contaminate the baseline.
func TestCacheBloat_BaselineFiltersByAdapter(t *testing.T) {
	hist := []*session.SessionState{
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 3*secondsPerDay),
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 2*secondsPerDay),
		histSession("proj", "claude-code", "1.0.0", 5, 5000, 1*secondsPerDay),
		// Same project, different adapter, huge — must not affect claude-code.
		histSession("proj", "codex", "9.9.9", 5, 10_000_000, 1*secondsPerDay),
	}
	rec := &fakeRecorder{}
	d := newDetector(&fakeLister{hist}, rec, testCfg())

	live := liveSession("proj", "claude-code", "1.0.0")
	driveTurns(d, live, 10000, 3)

	if !live.Metrics.CacheBloat {
		t.Error("other-adapter history must not raise the claude-code baseline")
	}
}
