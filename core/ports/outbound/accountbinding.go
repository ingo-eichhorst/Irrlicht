package outbound

import "irrlicht/core/domain/session"

// HomeAccountObservation is what one agent home's identity file says about
// the logged-in account right now. It carries no email, name or credential.
type HomeAccountObservation struct {
	// Home is the absolute path of the identity file — the home's stable key.
	Home string
	// Ref is the provider-issued account id; "" when the file names no account.
	Ref string
	// Product is the tier/product of that account (Claude Code: organizationType).
	Product string
	// ModTime is the identity file's mtime in Unix seconds.
	ModTime int64
}

// SessionAccountReader reads the account a session's agent home is logged
// into, consent-gated by the implementation.
type SessionAccountReader interface {
	// ReadHomeAccount returns ok=false when consent is not granted (checked
	// before any I/O), the session has no transcript path, or the path lies
	// under no home this reader attributes. A readable identity file that
	// names no account is ok=true with Ref "". A transient read or parse
	// failure returns the last good observation for that home when there is
	// one, else ok=false.
	ReadHomeAccount(s *session.SessionState) (HomeAccountObservation, bool)
	// TranscriptStart is the Unix second of the first timestamped line of the
	// session's transcript (bounded read), 0 when unknown or not granted.
	TranscriptStart(s *session.SessionState) int64
}

// HomeAccountStore persists the daemon's per-home account tracking so a
// switch made while the daemon was down is seen as a change on restart.
type HomeAccountStore interface {
	Load() (map[string]session.HomeAccount, error)
	Save(map[string]session.HomeAccount) error
}
