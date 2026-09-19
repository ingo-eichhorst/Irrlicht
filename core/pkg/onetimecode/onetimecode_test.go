package onetimecode

// Direct unit tests for the leaf algorithm, over MemoryStore (and a fake
// Store for error propagation and the Key-argument mutation). Most of these
// are LOCKS: normalization, IsPresentedCode's shape check, the outstanding
// cap and the failure window are unchanged from push/codes.go's pre-#1963
// version (#1901 already established the property each one pins), so their
// green here is a lock, not red-first proof of a new fix — push's own
// push/codes_test.go (unedited, the lift's lock) already proves the
// identical algorithm through push.Service's RAM path.
//
// Two properties genuinely are new in #1963 and each owes a mutation seen
// red (AGENTS.md: anything a change adds gets a mutation, not just a green
// test). Both were run against real source edits, captured, and reverted;
// this table transcribes what was actually seen, not what was planned.
//
//	Mutation (source edit)                                         | Test(s) that went red                              | What the red showed
//	----------------------------------------------------------------|-----------------------------------------------------|------------------------------------------------------------
//	Redeem: m.store.Key(normalizeCode(code)) -> m.store.Key(code)   | TestKeyReceivesNormalizedFormAtMintAndRedeem         | Not the arguments-differ assertion at the bottom of the
//	(feeds Key the raw candidate instead of the normalized form)    |                                                       | test (never reached) — Redeem itself returned
//	                                                                |                                                       | ErrCodeInvalid ("Redeem(\"rhgc-fjvs\"): onetimecode:
//	                                                                |                                                       | invalid or expired code") and the earlier `if err != nil
//	                                                                |                                                       | { t.Fatalf }` check failed instead: with fakeStore's
//	                                                                |                                                       | identity Key, hashing the raw lowercased/hyphenated
//	                                                                |                                                       | candidate no longer equals the normalized key Mint
//	                                                                |                                                       | stored, so the code stops matching at all for any
//	                                                                |                                                       | non-canonical presentation — a stronger, more direct
//	                                                                |                                                       | demonstration of the bug than a bookkeeping mismatch.
//	sweepExpired: `kept := make([]Record, 0, len(recs))` ->         | none (full package suite green; push and enroll       | The doc comment's stated risk — a Store's Load result
//	`kept := recs[:0]` (filters in place instead of into a          | suites green too)                                    | being some other caller's live backing array — is not
//	fresh slice)                                                    |                                                       | exercised by either concrete Store: MemoryStore.Load and
//	                                                                |                                                       | fileEnrollStore.Load each already return a fresh copy
//	                                                                |                                                       | (own `out := make(...)`), and within Manager itself recs
//	                                                                |                                                       | is reassigned immediately after every sweepExpired call,
//	                                                                |                                                       | so nothing retains the pre-sweep slice to be corrupted.
//	                                                                |                                                       | The allocation is defensive-API-contract hygiene — a
//	                                                                |                                                       | future Store implementation could reasonably return a
//	                                                                |                                                       | slice it still owns — not something today's callers
//	                                                                |                                                       | observably need. Reported as the finding it is, not
//	                                                                |                                                       | dressed up as a red.

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// testClock is the injected clock these tests share. No test here sleeps —
// TTLs and the failure window are driven entirely by advancing it.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func newTestClock() *testClock {
	return &testClock{t: time.Unix(1_700_000_000, 0)}
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeStore is a Store whose Load/Save/Key behavior is overridable, used to
// test error propagation out of Mint/Redeem and to record the arguments
// Key was called with.
type fakeStore struct {
	loadRecs []Record
	loadErr  error
	saveErr  error
	keyCalls []string // every code Key was called with, in call order
}

func (f *fakeStore) Load() ([]Record, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	out := make([]Record, len(f.loadRecs))
	copy(out, f.loadRecs)
	return out, nil
}

func (f *fakeStore) Save(recs []Record) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	f.loadRecs = recs
	return nil
}

func (f *fakeStore) Key(code string) string {
	f.keyCalls = append(f.keyCalls, code)
	return code // identity, like MemoryStore — the digest choice is a persisted-store concern, not this test's
}

// LOCK: normalizeCode's folding, exercised through Redeem's public surface
// (normalizeCode itself is unexported). Mirrors push/codes_test.go's
// TestRedeemRoundTripAndNormalization; the algorithm moved into this leaf
// unchanged in #1963, so this pins pre-existing behavior.
func TestRedeemNormalizesPresentedForm(t *testing.T) {
	m := NewManager(nil, NewMemoryStore())
	variants := []struct {
		name      string
		transform func(string) string
	}{
		{"as presented", func(c string) string { return c }},
		{"lowercase", strings.ToLower},
		{"no dash", func(c string) string { return strings.ReplaceAll(c, "-", "") }},
		{"inner spaces", func(c string) string {
			raw := strings.ReplaceAll(c, "-", "")
			return raw[:2] + " " + raw[2:4] + " " + raw[4:6] + " " + raw[6:]
		}},
		{"leading and trailing space", func(c string) string { return "  " + c + "  " }},
	}
	for _, v := range variants {
		t.Run(v.name, func(t *testing.T) {
			code, _, err := m.Mint("acme", "")
			if err != nil {
				t.Fatalf("Mint: %v", err)
			}
			presented := v.transform(code)
			ws, _, err := m.Redeem(presented)
			if err != nil {
				t.Fatalf("Redeem(%q): %v", presented, err)
			}
			if ws != "acme" {
				t.Fatalf("workspace = %q, want %q", ws, "acme")
			}
		})
	}
}

// LOCK: IsPresentedCode's shape check, unchanged from push/codes.go.
func TestIsPresentedCodeBoundaries(t *testing.T) {
	tests := []struct {
		name string
		code string
		want bool
	}{
		{"valid", "ABCD-2345", true},
		{"empty string", "", false},
		{"too short", "ABCD-23", false},
		{"too long", "ABCD-234567", false},
		{"dash in the wrong position", "AB-CD2345", false},
		{"no dash at all", "ABCD23456", false},
		{"character outside Alphabet (O, excluded)", "ABCD-234O", false},
		{"character outside Alphabet (lowercase)", "abcd-2345", false},
		// "ABCD-23é" is exactly CodeLen+1 (9) bytes — 7 single-byte runes
		// plus é's 2 bytes — so it passes the length and dash-position
		// checks and is rejected by the alphabet-membership check the rune
		// loop performs, not by length (a longer multi-byte input, e.g.
		// "ABCD-234€", is 11 bytes and would be rejected on length alone,
		// duplicating the "too long" case above without ever reaching the
		// rune loop).
		{"multi-byte rune", "ABCD-23é", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPresentedCode(tt.code); got != tt.want {
				t.Fatalf("IsPresentedCode(%q) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

// LOCK: the outstanding-code cap sweeps expired records before refusing.
// Mirrors push/codes_test.go's TestMintCapAt32SweepsExpiredFirst.
func TestMintCapSweepsExpiredFirst(t *testing.T) {
	clk := newTestClock()
	m := NewManager(clk.now, NewMemoryStore())
	for i := range MaxOutstanding {
		if _, _, err := m.Mint("acme", ""); err != nil {
			t.Fatalf("mint %d: %v", i, err)
		}
	}
	if _, _, err := m.Mint("acme", ""); !errors.Is(err, ErrTooManyCodes) {
		t.Fatalf("mint past cap = %v, want ErrTooManyCodes", err)
	}
	clk.advance(CodeTTL)
	if _, _, err := m.Mint("acme", ""); err != nil {
		t.Fatalf("mint after expiry sweep: %v", err)
	}
}

// LOCK: the redeem-failure window drains by clock alone. Mirrors
// push/codes_test.go's TestRedeemFailureRateLimit.
func TestRedeemFailureWindowDrains(t *testing.T) {
	clk := newTestClock()
	m := NewManager(clk.now, NewMemoryStore())
	good, _, err := m.Mint("acme", "")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	for i := range FailureLimit {
		if _, _, err := m.Redeem("ZZZZ-ZZZZ"); !errors.Is(err, ErrCodeInvalid) {
			t.Fatalf("failure %d = %v, want ErrCodeInvalid", i, err)
		}
	}
	if _, _, err := m.Redeem(good); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("redeem(correct) while tripped = %v, want ErrRateLimited", err)
	}
	clk.advance(FailureWindow + time.Second)
	ws, _, err := m.Redeem(good)
	if err != nil {
		t.Fatalf("redeem after window drained: %v", err)
	}
	if ws != "acme" {
		t.Fatalf("workspace = %q, want %q", ws, "acme")
	}
}

// New coverage (no prior lock — the old RAM-only push/codes.go had no Store
// abstraction, so Load/Save had no error path to test before #1963):
// Mint/Redeem propagate a Load error unchanged.
func TestMintPropagatesLoadError(t *testing.T) {
	wantErr := errors.New("disk on fire")
	m := NewManager(nil, &fakeStore{loadErr: wantErr})
	if _, _, err := m.Mint("acme", ""); !errors.Is(err, wantErr) {
		t.Fatalf("Mint load error = %v, want %v", err, wantErr)
	}
}

func TestRedeemPropagatesLoadError(t *testing.T) {
	wantErr := errors.New("disk on fire")
	m := NewManager(nil, &fakeStore{loadErr: wantErr})
	if _, _, err := m.Redeem("ABCD-2345"); !errors.Is(err, wantErr) {
		t.Fatalf("Redeem load error = %v, want %v", err, wantErr)
	}
}

// New coverage: Mint/Redeem propagate a Save error unchanged.
func TestMintPropagatesSaveError(t *testing.T) {
	wantErr := errors.New("disk full")
	m := NewManager(nil, &fakeStore{saveErr: wantErr})
	if _, _, err := m.Mint("acme", ""); !errors.Is(err, wantErr) {
		t.Fatalf("Mint save error = %v, want %v", err, wantErr)
	}
}

func TestRedeemPropagatesSaveError(t *testing.T) {
	wantErr := errors.New("disk full")
	// Seed a matching record directly (bypassing Mint, which would itself
	// hit saveErr) so Redeem's Load finds a match and reaches its own Save.
	store := &fakeStore{
		loadRecs: []Record{{Key: "ABCD2345", Workspace: "acme", ExpiresAt: time.Now().Add(time.Hour)}},
		saveErr:  wantErr,
	}
	m := NewManager(nil, store)
	if _, _, err := m.Redeem("ABCD-2345"); !errors.Is(err, wantErr) {
		t.Fatalf("Redeem save error = %v, want %v", err, wantErr)
	}
}

// TestKeyReceivesNormalizedFormAtMintAndRedeem proves Store.Key is always
// called with the ALREADY-NORMALIZED code, at both Mint and Redeem — the
// property that lets a hyphenated, lowercased or re-spaced presentation of
// the same code still match after going through a digest-keyed Store
// (enrollment's file store hashes Key's return value; a Key call fed the
// raw, un-normalized candidate at Redeem would compute the wrong digest and
// silently never match). See this file's header table for the mutation
// that was run against this test and seen red.
func TestKeyReceivesNormalizedFormAtMintAndRedeem(t *testing.T) {
	store := &fakeStore{}
	m := NewManager(nil, store)

	code, _, err := m.Mint("acme", "")
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if len(store.keyCalls) != 1 {
		t.Fatalf("Key called %d time(s) during Mint, want 1", len(store.keyCalls))
	}
	mintArg := store.keyCalls[0]
	if len(mintArg) != CodeLen || strings.Contains(mintArg, "-") {
		t.Fatalf("Mint's Key argument %q is not the normalized %d-char form", mintArg, CodeLen)
	}

	// Redeem with a deliberately non-normalized presentation.
	presented := strings.ToLower(code)
	if _, _, err := m.Redeem(presented); err != nil {
		t.Fatalf("Redeem(%q): %v", presented, err)
	}
	if len(store.keyCalls) != 2 {
		t.Fatalf("Key called %d time(s) total, want 2 (one Mint, one Redeem)", len(store.keyCalls))
	}
	redeemArg := store.keyCalls[1]
	if redeemArg != mintArg {
		t.Fatalf("Redeem's Key argument %q != Mint's %q — Key was not called with the normalized form both times", redeemArg, mintArg)
	}
}
