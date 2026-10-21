package session

// HomeAccount is the account one agent home's identity file names, and the
// earliest Unix second that home is known to have held that value
// continuously until now. Since is a lower bound on "when did this account
// become current", never an exact switch time: it is the identity file's
// mtime at first observation, capped at the observation time.
type HomeAccount struct {
	Ref   string `json:"ref,omitempty"`
	Since int64  `json:"since"`
}

// AccountBinding records which account a session's quota readings may be
// attributed to. It is decided once, at the first pass that can see both the
// home's account and the session's start, and can only ever be cleared
// afterwards — a switch of the home's account mid-session makes every later
// reading unattributable, and switching back does not undo that.
type AccountBinding struct {
	// Decided is set by the first Advance. An undecided binding is retried on
	// every pass; a decided one with an empty Ref is a permanent "unconfirmed".
	Decided bool `json:"decided,omitempty"`
	// Ref is the account the session was bound to; empty when unconfirmed.
	Ref string `json:"ref,omitempty"`
	// ClearedAt is the Unix second from which Ref no longer applies (the
	// home's Since for the value that replaced Ref). Zero while still bound.
	ClearedAt int64 `json:"cleared_at,omitempty"`
}

// Advance applies one observation of the home's account. sessionStart is the
// earliest time the session is known to have existed (Unix seconds). The rule
// fails closed:
//   - undecided: bind to home.Ref only when the home already held that
//     account, unchanged, at or before sessionStart. Otherwise the session is
//     decided as unconfirmed and never binds later.
//   - bound and not yet cleared: a different account (including a logout,
//     Ref "") clears the binding as of home.Since. Sticky.
func (b AccountBinding) Advance(home HomeAccount, sessionStart int64) AccountBinding {
	if !b.Decided {
		b.Decided = true
		if home.Ref != "" && home.Since <= sessionStart {
			b.Ref = home.Ref
		}
		return b
	}
	if b.Ref != "" && b.ClearedAt == 0 && home.Ref != b.Ref {
		b.ClearedAt = home.Since
	}
	return b
}

// RefAt returns the account a reading sampled at sampledAt (Unix seconds) is
// attributable to, or "" when it is not.
func (b AccountBinding) RefAt(sampledAt int64) string {
	if b.Ref == "" {
		return ""
	}
	if b.ClearedAt != 0 && sampledAt >= b.ClearedAt {
		return ""
	}
	return b.Ref
}
