// account.go reads which ChatGPT account a Codex home is logged into, so the
// daemon can attribute a session's quota snapshot to one account (#2095).
//
// Consent: it is gated by the adapter's own observe permission
// (PermissionKeyTranscripts), checked before any I/O; the permission text
// names the auth.json fields read. auth.json is decoded through the struct
// below, which has room for auth_mode and tokens.account_id and nothing
// else, so the id/access/refresh tokens and any API key in the same file are
// decoded by nothing and kept by nothing.
package codex

import (
	"encoding/json"
	"path/filepath"
	"regexp"

	"irrlicht/core/adapters/inbound/agents/accountfile"
	"irrlicht/core/domain/session"
	"irrlicht/core/ports/outbound"
)

// authFile is the allowlist for <CODEX_HOME>/auth.json.
type authFile struct {
	AuthMode string `json:"auth_mode"`
	Tokens   struct {
		AccountID string `json:"account_id"`
	} `json:"tokens"`
}

// AccountReader implements outbound.SessionAccountReader for Codex.
type AccountReader struct {
	granted func() bool
	cache   *accountfile.Cache
}

var _ outbound.SessionAccountReader = (*AccountReader)(nil)

// NewAccountReader builds the production reader. granted is consulted before
// every read; nil means never granted (fail closed).
func NewAccountReader(granted func() bool) *AccountReader {
	return newAccountReader(granted, accountfile.OSFS())
}

func newAccountReader(granted func() bool, fs accountfile.FS) *AccountReader {
	return &AccountReader{granted: granted, cache: accountfile.NewCache(fs)}
}

func (r *AccountReader) allowed() bool { return r != nil && r.granted != nil && r.granted() }

var rolloutDirDigits = regexp.MustCompile(`^\d{2,4}$`)

// authPathFor derives the auth.json beside the session's own rollout:
// <home>/sessions/YYYY/MM/DD/<rollout> -> <home>/auth.json. Any other shape
// returns "", so a CODEX_HOME override is honoured because the session's own
// path carries it, and a path outside that layout is attributed to nothing
// rather than to ~/.codex by default.
func authPathFor(transcriptPath string) string {
	if transcriptPath == "" || !filepath.IsAbs(transcriptPath) {
		return ""
	}
	dir := filepath.Dir(filepath.Clean(transcriptPath)) // .../DD
	for i := 0; i < 3; i++ {                            // DD, MM, YYYY must be digits
		if !rolloutDirDigits.MatchString(filepath.Base(dir)) {
			return ""
		}
		dir = filepath.Dir(dir)
	}
	if filepath.Base(dir) != "sessions" {
		return ""
	}
	return filepath.Join(filepath.Dir(dir), "auth.json")
}

// ReadHomeAccount implements outbound.SessionAccountReader. A chatgpt-mode
// auth.json yields tokens.account_id; any other mode is ok with Ref "" (an
// API-key home has no account).
func (r *AccountReader) ReadHomeAccount(s *session.SessionState) (outbound.HomeAccountObservation, bool) {
	if !r.allowed() || s == nil {
		return outbound.HomeAccountObservation{}, false
	}
	path := authPathFor(s.TranscriptPath)
	if path == "" {
		return outbound.HomeAccountObservation{}, false
	}
	return r.cache.Lookup(path, parseAuth)
}

func parseAuth(data []byte) (outbound.HomeAccountObservation, bool) {
	var f authFile
	if err := json.Unmarshal(data, &f); err != nil {
		return outbound.HomeAccountObservation{}, false
	}
	obs := outbound.HomeAccountObservation{}
	if f.AuthMode == "chatgpt" {
		obs.Ref = f.Tokens.AccountID
	}
	return obs, true
}

// TranscriptStart implements outbound.SessionAccountReader: the first
// top-level "timestamp" in the first accountfile.HeadBytes of the rollout
// (the session_meta line carries one).
func (r *AccountReader) TranscriptStart(s *session.SessionState) int64 {
	if !r.allowed() || s == nil || authPathFor(s.TranscriptPath) == "" {
		return 0
	}
	return accountfile.FirstTimestamp(r.cache.FS, s.TranscriptPath)
}
