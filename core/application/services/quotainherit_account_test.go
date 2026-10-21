package services

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/domain/session"
)

// stampedCodexDonor is a codex session whose snapshot the AccountBinder has
// already stamped with ref (#2095): the donor identity comes from the stamp,
// never from a file read at inheritance time.
func stampedCodexDonor(transcript, ref string) *session.SessionState {
	s := donorCodex(1000, 22, ref)
	s.TranscriptPath = transcript
	return s
}

// B1 (red-first): a codex session under an alternate CODEX_HOME donates under
// the account its snapshot was stamped with, not the one in ~/.codex/auth.json.
func TestInheritRateLimits_DonorIdentityComesFromTheStampNotTheHome(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".codex/auth.json": map[string]any{
			"auth_mode": "chatgpt",
			"tokens":    map[string]any{"account_id": "acct-Y"},
		},
		".pi/agent/auth.json": map[string]any{
			"openai-codex": map[string]any{"type": "oauth", "accountId": "acct-X"},
		},
	})
	codex := stampedCodexDonor(filepath.Join(t.TempDir(), "alt", "sessions", "2026", "01", "02", "rollout-x.jsonl"), "acct-X")
	pi := emptyWrapper("pi", "pi-1")
	InheritRateLimits([]*session.SessionState{codex, pi}, home, allowAll)
	if pi.Metrics.RateLimit == nil {
		t.Fatal("expected pi (acct-X) to inherit from the codex session stamped acct-X")
	}
}

// B2 (red-first): with no adapter's observe consent granted, the inheritance
// pass reads no auth.json at all. Observed through the package cache, which
// holds an entry (even a negative one) for every path the pass touched.
func TestInheritRateLimits_ReadsNoAuthFileWithoutConsent(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".codex/auth.json": map[string]any{
			"auth_mode": "chatgpt",
			"tokens":    map[string]any{"account_id": "acct-X"},
		},
		".pi/agent/auth.json": map[string]any{
			"openai-codex": map[string]any{"type": "oauth", "accountId": "acct-X"},
		},
		".local/share/opencode/auth.json": map[string]any{
			"openai-oauth": map[string]any{"type": "oauth", "access_token": makeJWT(`{"https://api.openai.com/auth.chatgpt_account_id":"acct-X"}`)},
		},
	})
	codex := stampedCodexDonor("/x/sessions/2026/01/02/rollout-x.jsonl", "acct-X")
	pi := emptyWrapper("pi", "pi-1")
	oc := emptyWrapper("opencode", "oc-1")
	InheritRateLimits([]*session.SessionState{codex, pi, oc}, home, func(string) bool { return false })

	authFileCache.mu.Lock()
	defer authFileCache.mu.Unlock()
	for _, rel := range []string{".codex/auth.json", ".pi/agent/auth.json", ".local/share/opencode/auth.json"} {
		if _, touched := authFileCache.entries[filepath.Join(home, rel)]; touched {
			t.Errorf("%s was read without consent", rel)
		}
	}
	if pi.Metrics.RateLimit != nil || oc.Metrics.RateLimit != nil {
		t.Error("a wrapper inherited without consent")
	}
}

// Each wrapper's recipient read is gated on ITS OWN adapter's consent: with
// only opencode granted, pi's auth.json is never touched and pi inherits
// nothing; opencode inherits. (Guard: mutated by
// tools/lib/account-consent-mutations_test.sh.)
func TestInheritRateLimits_RecipientReadIsGatedPerAdapter(t *testing.T) {
	for _, tc := range []struct {
		granted        string
		piRead, ocRead bool
	}{
		{"pi", true, false},
		{"opencode", false, true},
		{"codex", false, false},
	} {
		t.Run(tc.granted, func(t *testing.T) {
			home := stageAuth(t, map[string]any{
				".pi/agent/auth.json": map[string]any{
					"openai-codex": map[string]any{"type": "oauth", "accountId": "acct-G"},
				},
				".local/share/opencode/auth.json": map[string]any{
					"openai-oauth": map[string]any{"type": "oauth", "access_token": makeJWT(`{"https://api.openai.com/auth.chatgpt_account_id":"acct-G"}`)},
				},
			})
			codex := stampedCodexDonor("/x/sessions/2026/01/02/rollout-x.jsonl", "acct-G")
			pi := emptyWrapper("pi", "pi-1")
			oc := emptyWrapper("opencode", "oc-1")
			InheritRateLimits([]*session.SessionState{codex, pi, oc}, home, func(a string) bool { return a == "codex" || a == tc.granted })

			authFileCache.mu.Lock()
			defer authFileCache.mu.Unlock()
			_, piTouched := authFileCache.entries[filepath.Join(home, ".pi/agent/auth.json")]
			_, ocTouched := authFileCache.entries[filepath.Join(home, ".local/share/opencode/auth.json")]
			if piTouched != tc.piRead || ocTouched != tc.ocRead {
				t.Errorf("granted=%s: pi read=%v opencode read=%v, want %v/%v", tc.granted, piTouched, ocTouched, tc.piRead, tc.ocRead)
			}
			if (pi.Metrics.RateLimit != nil) != tc.piRead || (oc.Metrics.RateLimit != nil) != tc.ocRead {
				t.Errorf("granted=%s: inherited pi=%v opencode=%v", tc.granted, pi.Metrics.RateLimit != nil, oc.Metrics.RateLimit != nil)
			}
		})
	}
}

// nil granted fails closed.
func TestInheritRateLimits_NilGrantedReadsNothing(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".pi/agent/auth.json": map[string]any{
			"openai-codex": map[string]any{"type": "oauth", "accountId": "acct-N"},
		},
	})
	codex := stampedCodexDonor("/x/sessions/2026/01/02/rollout-x.jsonl", "acct-N")
	pi := emptyWrapper("pi", "pi-1")
	InheritRateLimits([]*session.SessionState{codex, pi}, home, nil)
	authFileCache.mu.Lock()
	defer authFileCache.mu.Unlock()
	if _, touched := authFileCache.entries[filepath.Join(home, ".pi/agent/auth.json")]; touched || pi.Metrics.RateLimit != nil {
		t.Errorf("nil granted: touched=%v inherited=%v", touched, pi.Metrics.RateLimit != nil)
	}
}

// The opencode access token is not retained in the package cache.
func TestAuthFileCache_DoesNotRetainTheOpenCodeAccessToken(t *testing.T) {
	const secret = "SECRET-TOKEN-PART"
	jwt := makeJWT(`{"https://api.openai.com/auth.chatgpt_account_id":"acct-T","x":"` + secret + `"}`)
	home := stageAuth(t, map[string]any{
		".local/share/opencode/auth.json": map[string]any{"openai-oauth": map[string]any{"type": "oauth", "access_token": jwt}},
	})
	if key, ok := readOpenCodeInheritKey(home); !ok || key.AccountID != "acct-T" {
		t.Fatalf("key=%+v ok=%v", key, ok)
	}
	authFileCache.mu.Lock()
	defer authFileCache.mu.Unlock()
	if got := fmt.Sprintf("%+v", authFileCache.entries[filepath.Join(home, ".local/share/opencode/auth.json")]); strings.Contains(got, secret) || strings.Contains(got, jwt) {
		t.Errorf("cache entry retains the access token: %s", got)
	}
}

// A1-3 (red-first): a donor contributes only while ITS adapter's consent is
// granted. On a codex observe revoke Stamp stops running for codex sessions,
// so a persisted ref on the snapshot would otherwise keep donating.
func TestInheritRateLimits_DonorWithoutItsOwnConsentDoesNotDonate(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".pi/agent/auth.json": map[string]any{
			"openai-codex": map[string]any{"type": "oauth", "accountId": "acct-D"},
		},
	})
	codex := stampedCodexDonor("/x/sessions/2026/01/02/rollout-x.jsonl", "acct-D")
	pi := emptyWrapper("pi", "pi-1")
	InheritRateLimits([]*session.SessionState{codex, pi}, home, func(a string) bool { return a == "pi" })
	if pi.Metrics.RateLimit != nil {
		t.Error("pi inherited from a codex donor whose codex consent is not granted")
	}
}
