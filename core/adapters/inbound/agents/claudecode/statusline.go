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
	"encoding/json"
	"net/http"
	"time"

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

// NewStatuslineHandler returns the HTTP handler for
// POST /api/v1/hooks/claudecode/statusline. The handler returns 200 with an
// empty body on success; on bad input or missing transcript_path it returns
// the appropriate 4xx. It never blocks: target.IngestRateLimit is a no-op
// when the session hasn't been seen yet, so the handler returns immediately.
//
// gate is the consent check for the "statusline" permission; while not
// granted the payload is dropped with 200. A nil gate means no gating —
// used by tests.
func NewStatuslineHandler(target RateLimitIngester, gate ConsentGranter, log outbound.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		if gate != nil && !gate.Granted(AdapterName, PermissionKeyStatusline) {
			w.WriteHeader(http.StatusOK)
			return
		}

		var payload statuslinePayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad request: invalid JSON", http.StatusBadRequest)
			return
		}

		if payload.TranscriptPath == "" {
			http.Error(w, "bad request: missing transcript_path", http.StatusBadRequest)
			return
		}

		sessionID := sessionIDFromTranscriptPath(payload.TranscriptPath)
		snap := statuslineToSnapshot(payload.RateLimits)
		if snap == nil {
			// API-key / Bedrock / Vertex users — nothing to record. Ack and move on.
			log.LogInfo("statusline-receiver", sessionID, "received statusline tick with no rate_limits block")
			w.WriteHeader(http.StatusOK)
			return
		}

		target.IngestRateLimit(payload.TranscriptPath, snap)
		log.LogInfo("statusline-receiver", sessionID, "ingested rate-limit snapshot")
		w.WriteHeader(http.StatusOK)
	}
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
