// Package onetimecode is the shared one-time-code algorithm behind both
// phone pairing (core/cmd/irrlichtrelay/push/codes.go) and desktop
// enrollment (core/cmd/irrlichtrelay's /api/v1/enroll routes, #1963): an
// 8-character code over an alphabet with the ambiguous I/L/O/U/0/1 dropped,
// a bounded TTL, single use, a cap on outstanding codes, a rolling
// redeem-failure window that locks out even a correct code, a constant-time
// match, and one uniform failure so a redeem endpoint can never be used as
// an oracle for which codes ever existed.
//
// Persistence is a seam (Store): the two callers need different shapes — a
// RAM store keeps a pairing code's plaintext in the minting process only
// (push/codes.go's original shape, since mint and redeem both happen inside
// the one serving relay); a persisted store computes an opaque digest per
// code instead, because an enrollment code must be mintable by a separate,
// short-lived CLI process with no relay running, and never wants the
// plaintext at rest. Manager owns everything else — generation, the cap,
// the TTL sweep, the failure window and the constant-time comparison —
// once, so the security property is implemented once for both callers
// rather than twice (docs/mobile-notifications-arc42.md §8.1, §8.6).
//
// This package sits under core/pkg/, the layer core/architecture_test.go's
// "pkg must not import adapters or application" rule constrains — it stays
// clear of that rule trivially, importing stdlib only (crypto/rand,
// crypto/subtle, errors, math/big, strings, sync, time; verified by reading
// this file's own import block, not by assertion). Both consumers —
// core/cmd/irrlichtrelay/push and core/cmd/irrlichtrelay's enroll_*.go
// files — live under core/cmd/irrlichtrelay, which carries no import
// restriction of its own in that test.
package onetimecode

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"math/big"
	"strings"
	"sync"
	"time"
)

const (
	// Alphabet drops the ambiguous I/L/O/U/0/1 so a code survives being read
	// off a screen or typed from memory.
	Alphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

	// CodeLen is the number of alphabet characters in a code, before the
	// presented XXXX-XXXX dash is inserted.
	CodeLen = 8

	// CodeTTL bounds how long a minted code stays redeemable. Both callers
	// are interactive, human-paced exchanges: ten minutes is generous for a
	// person and short for an attacker.
	CodeTTL = 10 * time.Minute

	// MaxOutstanding caps a Manager's unexpired set. A runaway minter is an
	// anomaly, not a queue — one operator pairing one device needs one code.
	MaxOutstanding = 32

	// FailureLimit failed redeems within FailureWindow refuse every further
	// redeem on that Manager — even of a correct code — until the rolling
	// window drains. With the constant-time match below this is the online
	// brute-force bound on the code space.
	FailureLimit  = 10
	FailureWindow = time.Minute
)

var (
	// ErrTooManyCodes reports a saturated outstanding set.
	ErrTooManyCodes = errors.New("onetimecode: too many outstanding codes")

	// ErrRateLimited reports a saturated redeem-failure window. Deliberately
	// distinct from ErrCodeInvalid so a legitimate user mid-brute-force sees
	// "try later", not "your code is wrong".
	ErrRateLimited = errors.New("onetimecode: too many failed redeem attempts, retry later")

	// ErrCodeInvalid is the single uniform redeem failure: unknown, expired
	// and already-used codes are deliberately indistinguishable, so a caller
	// cannot use redeem as an oracle for which codes ever existed.
	ErrCodeInvalid = errors.New("onetimecode: invalid or expired code")
)

// Record is one outstanding code, as a Store persists it. Key is the value
// a redeem candidate is matched against: for an in-memory store this is the
// code's own normalized form (safe because it never leaves that process);
// for a persisted store it is an opaque digest (see Store.Key), so the
// plaintext code is never written to disk. Workspace and Label are
// minter-supplied metadata carried through unchanged to Manager.Redeem's
// return; a caller with no notion of a label (phone pairing) mints with one
// empty.
type Record struct {
	Key       string
	Workspace string
	Label     string
	ExpiresAt time.Time
}

// Store is the persistence seam behind Manager. Manager calls Load at the
// top of every Mint and Redeem, mutates the returned slice in memory, and
// calls Save with the result before returning — so a Store implementation
// need not itself be safe for concurrent use; Manager's own lock already
// serializes every call into it.
type Store interface {
	// Load returns the current record set.
	Load() ([]Record, error)
	// Save persists the record set exactly as given — a full replace, not a
	// delta.
	Save([]Record) error
	// Key derives the value a redeem candidate is matched against from the
	// already-normalized code text. Identity for a RAM store; a fixed-width
	// digest (e.g. SHA-256 hex) for anything persisted, computed the same
	// way at mint and at redeem so a hyphenated, lowercased or re-spaced
	// presentation of the same code still matches.
	Key(normalizedCode string) string
}

// Manager owns mint, redeem, single-use consumption, expiry sweep and the
// rolling redeem-failure window over one Store. Safe for concurrent use.
//
// The redeem-failure window lives on the Manager itself, in RAM only, never
// through Store: Redeem only ever runs inside the one process serving
// requests (a code-minting CLI process never redeems), so nothing requires
// the window to survive a restart or be visible cross-process — unlike the
// records themselves, which a separate minting process must be able to
// write for a redeeming process to see.
type Manager struct {
	now   func() time.Time
	store Store

	mu       sync.Mutex
	failures []time.Time
}

// NewManager builds a Manager over store. now is the injected clock; nil
// means time.Now.
func NewManager(now func() time.Time, store Store) *Manager {
	if now == nil {
		now = time.Now
	}
	return &Manager{now: now, store: store}
}

// Mint mints a single-use code bound to workspace and label (label may be
// empty), presented as XXXX-XXXX. Expired records are swept first, so an
// abandoned session never wedges the cap.
func (m *Manager) Mint(workspace, label string) (code string, ttl time.Duration, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	recs, err := m.store.Load()
	if err != nil {
		return "", 0, err
	}
	recs = sweepExpired(recs, now)
	if len(recs) >= MaxOutstanding {
		// Persist the sweep even though mint itself is refused — an
		// abandoned session's expired record should not keep counting
		// against the cap on the very next mint attempt either.
		if err := m.store.Save(recs); err != nil {
			return "", 0, err
		}
		return "", 0, ErrTooManyCodes
	}
	normalized, err := randomCode()
	if err != nil {
		return "", 0, err
	}
	key := m.store.Key(normalized)
	recs = append(recs, Record{Key: key, Workspace: workspace, Label: label, ExpiresAt: now.Add(CodeTTL)})
	if err := m.store.Save(recs); err != nil {
		return "", 0, err
	}
	return normalized[:4] + "-" + normalized[4:], CodeTTL, nil
}

// Redeem exchanges a code for the workspace/label it was minted with,
// consuming it. Failures are ErrCodeInvalid (uniform — see the var) or
// ErrRateLimited; the rate limit is checked before any lookup, so a
// saturated window refuses a correct code too. Candidate comparison is
// constant-time and walks every outstanding record without early exit.
func (m *Manager) Redeem(code string) (workspace, label string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.failures = pruneFailures(m.failures, now)
	if len(m.failures) >= FailureLimit {
		return "", "", ErrRateLimited
	}
	recs, err := m.store.Load()
	if err != nil {
		return "", "", err
	}
	recs = sweepExpired(recs, now) // an expired code fails exactly like an unknown one
	want := []byte(m.store.Key(normalizeCode(code)))
	match := -1
	for i := range recs {
		if subtle.ConstantTimeCompare([]byte(recs[i].Key), want) == 1 {
			match = i
		}
	}
	if match < 0 {
		m.failures = append(m.failures, now)
		// Deliberately not persisted: the sweep above is re-derived from
		// ExpiresAt on every future Load, so nothing is lost by skipping a
		// disk write on a failed guess — and a Save that itself failed here
		// would force a choice between returning a distinguishable error
		// (breaking the uniform-failure property) or swallowing it silently.
		return "", "", ErrCodeInvalid
	}
	ws, lbl := recs[match].Workspace, recs[match].Label
	recs = append(recs[:match], recs[match+1:]...) // single use
	if err := m.store.Save(recs); err != nil {
		return "", "", err
	}
	return ws, lbl, nil
}

// normalizeCode folds the presentations a human might type back to the
// canonical form: case-insensitive, dashes and spaces ignored.
func normalizeCode(code string) string {
	code = strings.ToUpper(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, "-", "")
	return strings.ReplaceAll(code, " ", "")
}

// IsPresentedCode reports whether code has the exact XXXX-XXXX form emitted
// by Mint. HTTP handoff routes use it before reflecting a path value into an
// install manifest or page.
func IsPresentedCode(code string) bool {
	if len(code) != CodeLen+1 || code[4] != '-' {
		return false
	}
	for i, r := range code {
		if i == 4 {
			continue
		}
		if !strings.ContainsRune(Alphabet, r) {
			return false
		}
	}
	return true
}

// randomCode returns CodeLen characters drawn uniformly from Alphabet.
// rand.Int, not a byte mod: 256 % len(Alphabet) != 0, and a biased alphabet
// quietly shrinks the search space.
func randomCode() (string, error) {
	var b strings.Builder
	for range CodeLen {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(Alphabet))))
		if err != nil {
			return "", err
		}
		b.WriteByte(Alphabet[n.Int64()])
	}
	return b.String(), nil
}

// sweepExpired returns the subset of recs not yet at their expiry. Always
// allocates a fresh slice rather than filtering in place: Store's own
// contract only promises Manager may "mutate the returned slice in memory"
// before Save (see the Store doc), not that Manager may alias a Store's
// internal state afterward — a Store implementation is free to return a
// slice it still holds a reference to. Filtering in place would risk
// corrupting such a Store's view for a caller that never reaches Save.
// Today's two concrete Stores (MemoryStore and enrollment's
// fileEnrollStore) both already return a fresh copy from Load — verified by
// reading each — so this allocation is not load-bearing against either of
// them specifically; it is margin for whatever Store comes next.
func sweepExpired(recs []Record, now time.Time) []Record {
	kept := make([]Record, 0, len(recs))
	for _, r := range recs {
		if now.Before(r.ExpiresAt) {
			kept = append(kept, r)
		}
	}
	return kept
}

// pruneFailures returns the subset of failures still inside FailureWindow.
// Always allocates a fresh slice, mirroring sweepExpired's defensive shape
// for consistency — even though failures never comes from a Store at all
// (it is Manager's own m.failures field, never something Load returns), so
// the aliasing risk sweepExpired guards against does not apply here; this
// is uniformity between the two filters, not a fact about this slice's
// origin.
func pruneFailures(failures []time.Time, now time.Time) []time.Time {
	kept := make([]time.Time, 0, len(failures))
	for _, f := range failures {
		if now.Sub(f) < FailureWindow {
			kept = append(kept, f)
		}
	}
	return kept
}

// MemoryStore is the in-process Store: records live only in RAM, keyed by
// their own plaintext normalized form — safe because that value never
// leaves this process (docs/mobile-notifications-arc42.md §8.6: "Pairing
// codes | RAM | no"). Safe for concurrent use, though in practice only ever
// called through a Manager, whose lock already serializes access.
type MemoryStore struct {
	mu   sync.Mutex
	recs []Record
}

// NewMemoryStore returns an empty in-process Store, ready to hand to
// NewManager.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{}
}

func (s *MemoryStore) Load() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, len(s.recs))
	copy(out, s.recs)
	return out, nil
}

func (s *MemoryStore) Save(recs []Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, len(recs))
	copy(out, recs)
	s.recs = out
	return nil
}

// Key is the identity function: a RAM store never writes the code to disk,
// so there is nothing to hash.
func (s *MemoryStore) Key(normalizedCode string) string {
	return normalizedCode
}
