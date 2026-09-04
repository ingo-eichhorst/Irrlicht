package push

// Pairing codes are the one-time bridge that carries a workspace from an
// authenticated client token into the device token minted at redeem
// (docs/mobile-notifications-arc42.md §8.1, §6.1). They live in RAM only:
// a relay restart mid-pairing just means regenerating the code (§8.6).

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"math/big"
	"strings"
	"time"
)

const (
	// codeAlphabet drops the ambiguous I/L/O/U/0/1 so a code survives being
	// read off a phone screen or typed from memory.
	codeAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"
	codeLen      = 8

	// CodeTTL bounds how long a minted code stays redeemable. Pairing is an
	// interactive scan-and-type flow; ten minutes is generous for a human
	// and short for an attacker.
	CodeTTL = 10 * time.Minute

	// maxOutstandingCodes caps the unexpired set. A runaway minter is an
	// anomaly, not a queue — one dashboard pairing one phone needs one code.
	maxOutstandingCodes = 32

	// redeemFailureLimit failed redeems within redeemFailureWindow refuse
	// every further redeem — even of a correct code — until the rolling
	// window drains. With the constant-time match below this is the online
	// brute-force bound on the code space.
	redeemFailureLimit  = 10
	redeemFailureWindow = time.Minute
)

var (
	// ErrTooManyCodes reports a saturated outstanding set; the HTTP surface
	// maps it to 429.
	ErrTooManyCodes = errors.New("push: too many outstanding pairing codes")

	// ErrRateLimited reports a saturated redeem-failure window; mapped to
	// 429. Deliberately distinct from ErrCodeInvalid so a legitimate user
	// mid-brute-force sees "try later", not "your code is wrong".
	ErrRateLimited = errors.New("push: too many failed pairing attempts, retry later")

	// ErrCodeInvalid is the single uniform redeem failure: unknown, expired
	// and already-used codes are deliberately indistinguishable, so the
	// endpoint cannot be used as an oracle for which codes ever existed.
	ErrCodeInvalid = errors.New("push: invalid or expired pairing code")
)

// pairingCode is one outstanding code: the canonical 8-char form it is
// matched against, the workspace it inherits from its minter, and when it
// stops being redeemable.
type pairingCode struct {
	normalized string
	workspace  string
	expiresAt  time.Time
}

// MintCode mints a single-use pairing code bound to workspace, presented as
// XXXX-XXXX. Expired codes are swept first, so an abandoned pairing session
// never wedges the cap.
func (s *Service) MintCode(workspace string) (code string, ttl time.Duration, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.sweepExpiredLocked(now)
	if len(s.codes) >= maxOutstandingCodes {
		return "", 0, ErrTooManyCodes
	}
	var b strings.Builder
	for range codeLen {
		// rand.Int, not a byte mod: 256 % len(alphabet) != 0, and a biased
		// code alphabet quietly shrinks the search space.
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(codeAlphabet))))
		if err != nil {
			return "", 0, err
		}
		b.WriteByte(codeAlphabet[n.Int64()])
	}
	norm := b.String()
	s.codes = append(s.codes, pairingCode{normalized: norm, workspace: workspace, expiresAt: now.Add(CodeTTL)})
	return norm[:4] + "-" + norm[4:], CodeTTL, nil
}

// Redeem exchanges a code for the workspace it was minted in, consuming it.
// Failures are ErrCodeInvalid (uniform — see the var) or ErrRateLimited; the
// rate limit is checked before any lookup, so a saturated window refuses a
// correct code too. Candidate comparison is constant-time and walks every
// outstanding code without early exit.
func (s *Service) Redeem(code string) (workspace string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	s.pruneFailuresLocked(now)
	if len(s.failures) >= redeemFailureLimit {
		return "", ErrRateLimited
	}
	s.sweepExpiredLocked(now) // an expired code fails exactly like an unknown one
	want := []byte(normalizeCode(code))
	match := -1
	for i := range s.codes {
		if subtle.ConstantTimeCompare([]byte(s.codes[i].normalized), want) == 1 {
			match = i
		}
	}
	if match < 0 {
		s.failures = append(s.failures, now)
		return "", ErrCodeInvalid
	}
	ws := s.codes[match].workspace
	s.codes = append(s.codes[:match], s.codes[match+1:]...) // single use
	return ws, nil
}

// normalizeCode folds the presentations a human might type back to the
// canonical form: case-insensitive, dashes and spaces ignored.
func normalizeCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, "-", "")
	return strings.ReplaceAll(code, " ", "")
}

// sweepExpiredLocked drops codes at or past their expiry. Caller holds mu.
func (s *Service) sweepExpiredLocked(now time.Time) {
	kept := s.codes[:0]
	for _, c := range s.codes {
		if now.Before(c.expiresAt) {
			kept = append(kept, c)
		}
	}
	s.codes = kept
}

// pruneFailuresLocked drops redeem failures older than the rolling window.
// Caller holds mu.
func (s *Service) pruneFailuresLocked(now time.Time) {
	kept := s.failures[:0]
	for _, f := range s.failures {
		if now.Sub(f) < redeemFailureWindow {
			kept = append(kept, f)
		}
	}
	s.failures = kept
}
