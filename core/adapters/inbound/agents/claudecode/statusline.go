// statusline.go provides the HTTP handler for receiving Claude Code's
// statusline JSON. Claude Code is configured (via settings.json's
// statusLine.command) to pipe the statusline payload to a curl that POSTs
// here. The handler extracts rate_limits (subscription quota data only
// available to Claude.ai Pro/Max users) and pushes the snapshot through
// to the tailer for the matching session.
//
// Statusline JSON shape (Pro/Max account):
//
//	{
//	  "session_id":     "...",
//	  "transcript_path":"/path/to/session.jsonl",
//	  "rate_limits": {
//	    "five_hour": { "used_percentage": 16,    "resets_at": 1778761800 },
//	    "seven_day": { "used_percentage": 14.0,  "resets_at": 1779188400 }
//	  }
//	}
//
// API-key users, Bedrock, and Vertex don't get the rate_limits block — the
// handler simply records nothing in that case.
package claudecode

import (
	"net/http"
	"time"

	"irrlicht/core/adapters/inbound/agents/hookjson"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// statuslineNow is a package-level clock the handler uses to stamp incoming
// snapshots. Indirected so tests can pin time deterministically.
var statuslineNow = time.Now

// statuslinePayload models the fields the handler reads from Claude Code's
// statusline JSON. Anything outside `rate_limits` is currently ignored.
type statuslinePayload struct {
	SessionID      string                `json:"session_id"`
	TranscriptPath string                `json:"transcript_path"`
	RateLimits     *statuslineRateLimits `json:"rate_limits,omitempty"`
}

// statuslineRateLimits mirrors the on-the-wire shape Claude Code sends —
// flat `five_hour` / `seven_day` keys (subset of the Codex schema). The
// 7-day percentage often carries floating-point noise (14.000000000000002);
// we keep it as-is and let the UI round.
type statuslineRateLimits struct {
	FiveHour *statuslineWindow `json:"five_hour,omitempty"`
	SevenDay *statuslineWindow `json:"seven_day,omitempty"`
}

type statuslineWindow struct {
	UsedPercentage float64 `json:"used_percentage"`
	ResetsAt       int64   `json:"resets_at"`
}

// RateLimitIngester is the narrow interface the statusline handler depends
// on. Satisfied by outbound.MetricsCollector — broken out so tests can
// supply a fake without depending on the broader port surface.
type RateLimitIngester interface {
	IngestRateLimit(transcriptPath string, snap *session.RateLimitSnapshot)
}

// logComponentStatuslineReceiver is the Logger component tag for the
// statusline HTTP handler below.
const logComponentStatuslineReceiver = "statusline-receiver"

// NewStatuslineHandler returns the HTTP handler for
// POST /api/v1/hooks/claudecode/statusline. The handler returns 200 with an
// empty body on success; on bad input or missing transcript_path it returns
// the appropriate 4xx. It never blocks: target.IngestRateLimit is a no-op
// when the session hasn't been seen yet, so the handler returns immediately.
//
// The returned hookjson.HookHandler is an http.Handler carrying the confiner it
// guards caller-supplied transcript paths with (issue #1390).
//
// gate is the consent check for the "statusline" permission; while not
// granted the payload is dropped with 200. A nil gate means no gating —
// used by tests.
func NewStatuslineHandler(target RateLimitIngester, gate ConsentGranter, log outbound.Logger) hookjson.HookHandler {
	return hookjson.NewHandler(transcriptConfiner(),
		hookjson.RequireConsent(gate, AdapterName, PermissionKeyStatusline),
		func(consent hookjson.Consent, c *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) {
			serveStatuslineRequest(target, consent, log, c, w, r)
		})
}

// serveStatuslineRequest is NewStatuslineHandler's request logic, pulled out of
// the returned closure so the constructor reads like its two siblings in
// hooks.go and the branching isn't counted at the closure's extra nesting
// depth.
func serveStatuslineRequest(target RateLimitIngester, consent hookjson.Consent, log outbound.Logger, confiner *hookjson.PathConfiner, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// The ONE permission this receiver acts under, and the whole of what it
	// declares (issue #1488). It owes no "transcripts" consent: the path it
	// forwards is a map key for metrics.IngestRateLimit, never opened — #1466
	// is about the READ, and there is none here. Because the set is published
	// on the handler, that judgement is now stated in one place and the
	// contract grades exactly it.
	if !consent.Granted(PermissionKeyStatusline) {
		w.WriteHeader(http.StatusOK)
		return
	}

	// The statusline endpoint sits on the same local, unauthenticated mux
	// as the hook receivers and takes the same caller-supplied path, so it
	// is confined by the same rule (issue #1361). Its sink is a map lookup
	// rather than an open, so this is not an arbitrary-read guard — it is
	// what keeps this receiver keying the tailer map with the SAME spelling
	// the hook receiver now uses. Two spellings of one file are two
	// sessions, and the rate-limit snapshot would land on neither.
	//
	// This is the receiver #1361's first cut forgot, three lines from the one
	// it was fixing. Going through DecodeConfined is what makes forgetting it
	// a build failure rather than a review catch (issue #1389).
	var payload statuslinePayload
	if !hookjson.DecodeConfined(w, r, log, logComponentStatuslineReceiver, consent, confiner, &payload,
		func(p *statuslinePayload) string { return p.TranscriptPath },
		func(p *statuslinePayload, confined string) { p.TranscriptPath = confined },
	) {
		return
	}
	transcriptPath := payload.TranscriptPath

	sessionID := sessionIDFromTranscriptPath(transcriptPath)
	snap := statuslineToSnapshot(payload.RateLimits)
	if snap == nil {
		// API-key / Bedrock / Vertex users — nothing to record. Ack and move on.
		log.LogInfo(logComponentStatuslineReceiver, sessionID, "received statusline tick with no rate_limits block")
		w.WriteHeader(http.StatusOK)
		return
	}

	target.IngestRateLimit(payload.TranscriptPath, snap)
	log.LogInfo(logComponentStatuslineReceiver, sessionID, "ingested rate-limit snapshot")
	w.WriteHeader(http.StatusOK)
}

// statuslineToSnapshot converts Claude Code's two-window statusline payload
// into the domain RateLimitSnapshot shape. Returns nil when both windows
// are absent — the API-key / no-subscription case.
//
// Mapping:
//   - five_hour → WindowMinutes=300
//   - seven_day → WindowMinutes=10080
//
// SampledAt is derived from statuslineNow() rather than any payload
// timestamp: Claude Code's statusline JSON doesn't carry one, and the
// request landed within the last few hundred milliseconds.
func statuslineToSnapshot(rl *statuslineRateLimits) *session.RateLimitSnapshot {
	if rl == nil || (rl.FiveHour == nil && rl.SevenDay == nil) {
		return nil
	}
	snap := &session.RateLimitSnapshot{
		SampledAt: statuslineNow().Unix(),
	}
	if rl.FiveHour != nil {
		snap.Windows = append(snap.Windows, session.RateLimitWindow{
			UsedPercent:   rl.FiveHour.UsedPercentage,
			WindowMinutes: 300,
			ResetsAt:      rl.FiveHour.ResetsAt,
		})
	}
	if rl.SevenDay != nil {
		snap.Windows = append(snap.Windows, session.RateLimitWindow{
			UsedPercent:   rl.SevenDay.UsedPercentage,
			WindowMinutes: 10080,
			ResetsAt:      rl.SevenDay.ResetsAt,
		})
	}
	return snap
}
