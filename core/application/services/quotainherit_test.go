package services

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"irrlicht/core/domain/session"
)

// stageAuth writes a fake home with the given auth files. Returns the
// home path; the caller's t.TempDir cleanup removes everything.
func stageAuth(t *testing.T, files map[string]any) string {
	t.Helper()
	home := t.TempDir()
	for rel, body := range files {
		path := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return home
}

// donorClaudeCode builds a claude-code session carrying a rate_limit
// snapshot stamped exactly as claudecode/statusline.go's statuslineToSnapshot
// stamps a real one, so this fixture exercises ProviderForSession's stamp
// read (and InheritRateLimits' donor path) the same way production data
// does — not a synthetic shape that happens to have the right SampledAt.
func donorClaudeCode(sampledAt int64, percent float64) *session.SessionState {
	return &session.SessionState{
		SessionID: "claudecode-donor",
		Adapter:   "claude-code",
		Metrics: &session.SessionMetrics{
			RateLimit: &session.RateLimitSnapshot{
				SampledAt: sampledAt,
				PlanType:  "max",
				Windows: []session.RateLimitWindow{
					{UsedPercent: percent, WindowMinutes: 300, ResetsAt: 99999999},
				},
				Provider:            ProviderAnthropic,
				ObservationSource:   "hook",
				AttributionEvidence: "claude_code_statusline",
				AttributionQuality:  session.AttributionQualityConfirmed,
			},
		},
	}
}

// donorCodex builds a codex session carrying a rate_limit snapshot stamped
// exactly as codex/parser.go's extractCodexRateLimits stamps a real one —
// same rationale as donorClaudeCode above, plus the ref the binder stamps.
func donorCodex(sampledAt int64, percent float64, ref string) *session.SessionState {
	return &session.SessionState{
		SessionID: "codex-donor",
		Adapter:   "codex",
		Metrics: &session.SessionMetrics{
			RateLimit: &session.RateLimitSnapshot{
				SampledAt: sampledAt,
				PlanType:  "plus",
				Windows: []session.RateLimitWindow{
					{UsedPercent: percent, WindowMinutes: 300, ResetsAt: 99999999},
				},
				Provider:            ProviderOpenAI,
				ObservationSource:   "transcript",
				AttributionEvidence: "codex_transcript_rate_limits",
				AttributionQuality:  session.AttributionQualityConfirmed,
				// What the AccountBinder stamps from the session's own
				// <CODEX_HOME>/auth.json (#2095); "" is an unconfirmed
				// (e.g. API-key) session, which never donates.
				ConfirmedAccountRef: ref,
			},
		},
	}
}

// allowAll is a granted func with every adapter's observe consent granted.
func allowAll(string) bool { return true }

func emptyWrapper(adapter, id string) *session.SessionState {
	return &session.SessionState{
		SessionID: id,
		Adapter:   adapter,
		Metrics:   &session.SessionMetrics{},
	}
}

func TestInheritRateLimits_PiOpenAIInheritsFromCodex(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".pi/agent/auth.json": map[string]any{
			"openai-codex": map[string]any{
				"type":      "oauth",
				"accountId": "acct-shared",
			},
		},
	})

	codex := donorCodex(1000, 22, "acct-shared")
	pi := emptyWrapper("pi", "pi-1")
	InheritRateLimits([]*session.SessionState{codex, pi}, home, allowAll)

	if pi.Metrics.RateLimit == nil {
		t.Fatal("expected pi session to inherit codex snapshot")
	}
	if pi.Metrics.RateLimit.SampledAt != 1000 {
		t.Errorf("inherited snapshot timestamp mismatch: got %d", pi.Metrics.RateLimit.SampledAt)
	}
}

func TestInheritRateLimits_PiNotInheritsOnAccountMismatch(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".pi/agent/auth.json": map[string]any{
			"openai-codex": map[string]any{
				"type":      "oauth",
				"accountId": "acct-different",
			},
		},
	})

	codex := donorCodex(1000, 22, "acct-codex")
	pi := emptyWrapper("pi", "pi-1")
	InheritRateLimits([]*session.SessionState{codex, pi}, home, allowAll)

	if pi.Metrics.RateLimit != nil {
		t.Fatal("expected no inheritance when account ids don't match")
	}
}

// TestInheritRateLimits_PiDoesNotInheritFromClaudeCode is #1994's red-first
// proof for the Anthropic-singleton removal (epic #1977 §3.2). This is
// TestInheritRateLimits_PiAnthropicInheritsFromClaudeCode, inverted: it used
// to assert that a pi(anthropic) session inherits a Claude Code snapshot with
// no account anchor at all. That inheritance is exactly what the singleton
// removal forbids — Claude Code's OAuth account lives in the macOS keychain,
// never on disk, so no evidence can confirm the two sessions share an
// account, and IsSingleton() used to let an empty AccountID match any
// same-provider wrapper regardless. Sharing without a confirmed account is
// the defect; this snapshot must now stay put on its own session.
func TestInheritRateLimits_PiDoesNotInheritFromClaudeCode(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".pi/agent/auth.json": map[string]any{
			"anthropic": map[string]any{"type": "oauth"},
		},
	})

	cc := donorClaudeCode(2000, 47)
	pi := emptyWrapper("pi", "pi-1")
	InheritRateLimits([]*session.SessionState{cc, pi}, home, allowAll)

	if pi.Metrics.RateLimit != nil {
		t.Fatalf("expected pi(anthropic) NOT to inherit from claude code (no confirmed account anchor exists for Anthropic), got %+v", pi.Metrics.RateLimit)
	}
}

// TestInheritRateLimits_PiPrefersOpenAIOverAnthropic used to pin an
// empirical "prefer OpenAI over Anthropic when both configured" tie-break
// (recipientKey's old doc comment: "either choice is defensible for a v1").
// #1994 / epic #1977 §3.1 replaces that empirical preference with evidence:
// there is no longer a real choice to make here, because an "anthropic"
// entry in pi's auth.json can never match a donor (readPiInheritKey no
// longer even reads it — see donorKey's claude-code case). Renamed and
// rewritten to assert what actually resolves the case now: pi still
// inherits codex's snapshot because its account id matches a real donor,
// regardless of whether anthropic is ALSO configured alongside it.
func TestInheritRateLimits_PiInheritsFromCodexRegardlessOfAnthropicConfig(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".pi/agent/auth.json": map[string]any{
			"openai-codex": map[string]any{"type": "oauth", "accountId": "acct-x"},
			"anthropic":    map[string]any{"type": "oauth"},
		},
	})

	codex := donorCodex(1000, 22, "acct-x")
	cc := donorClaudeCode(2000, 47)
	pi := emptyWrapper("pi", "pi-1")
	InheritRateLimits([]*session.SessionState{codex, cc, pi}, home, allowAll)

	if pi.Metrics.RateLimit == nil {
		t.Fatal("expected inheritance")
	}
	if pi.Metrics.RateLimit.SampledAt != 1000 {
		t.Errorf("expected codex (openai) donor via matching account id; got SampledAt=%d", pi.Metrics.RateLimit.SampledAt)
	}
}

// TestInheritRateLimits_FreshestDonorWinsOnTies used two claude-code donors
// before #1994 removed the Anthropic singleton; rewritten to two codex
// donors on the SAME confirmed account, since that's the only donor path
// that still shares snapshots across sessions. buildDonorMap must still pick
// the freshest of several donors backing one account, not merely the first
// or last one seen.
func TestInheritRateLimits_FreshestDonorWinsOnTies(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".pi/agent/auth.json": map[string]any{
			"openai-codex": map[string]any{
				"type":      "oauth",
				"accountId": "acct-shared",
			},
		},
	})

	stale := donorCodex(1000, 80, "acct-shared")
	fresh := donorCodex(5000, 20, "acct-shared")
	pi := emptyWrapper("pi", "pi-1")
	InheritRateLimits([]*session.SessionState{stale, fresh, pi}, home, allowAll)

	if pi.Metrics.RateLimit == nil {
		t.Fatal("expected pi to inherit a snapshot")
	}
	if pi.Metrics.RateLimit.SampledAt != 5000 {
		t.Errorf("expected fresh donor (5000), got %d", pi.Metrics.RateLimit.SampledAt)
	}
}

func TestInheritRateLimits_DoesNotOverwriteOwnSnapshot(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".pi/agent/auth.json": map[string]any{
			"anthropic": map[string]any{"type": "oauth"},
		},
	})

	cc := donorClaudeCode(2000, 47)
	pi := &session.SessionState{
		SessionID: "pi-own",
		Adapter:   "pi",
		Metrics: &session.SessionMetrics{
			RateLimit: &session.RateLimitSnapshot{SampledAt: 9999, PlanType: "self"},
		},
	}
	InheritRateLimits([]*session.SessionState{cc, pi}, home, allowAll)

	if pi.Metrics.RateLimit.SampledAt != 9999 || pi.Metrics.RateLimit.PlanType != "self" {
		t.Errorf("inheritance overwrote a session's own snapshot: %+v", pi.Metrics.RateLimit)
	}
}

func TestInheritRateLimits_NoDonorIsNoOp(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".pi/agent/auth.json": map[string]any{
			"anthropic": map[string]any{"type": "oauth"},
		},
	})

	pi := emptyWrapper("pi", "pi-1")
	InheritRateLimits([]*session.SessionState{pi}, home, allowAll)

	if pi.Metrics.RateLimit != nil {
		t.Fatal("expected no inheritance when no donor exists")
	}
}

// makeJWT builds a synthetic JWT with the given JSON payload string.
// The signature is junk — we never verify it.
func makeJWT(payloadJSON string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(payloadJSON))
	return fmt.Sprintf("%s.%s.fakesig", header, payload)
}

func TestOpenCodeJWTAccountID(t *testing.T) {
	claimKey := "https://api.openai.com/auth.chatgpt_account_id"
	cases := []struct {
		name  string
		token string
		want  string
	}{
		{
			name:  "valid jwt with claim",
			token: makeJWT(fmt.Sprintf(`{%q:"acct-jwt123"}`, claimKey)),
			want:  "acct-jwt123",
		},
		{
			name:  "valid jwt missing claim",
			token: makeJWT(`{"sub":"user@example.com"}`),
			want:  "",
		},
		{
			name:  "too few segments",
			token: "header.payload",
			want:  "",
		},
		{
			name:  "bad base64 in payload",
			token: "header.!!!.sig",
			want:  "",
		},
		{
			name:  "claim value is not a string",
			token: makeJWT(fmt.Sprintf(`{%q:42}`, claimKey)),
			want:  "",
		},
		{
			name:  "empty token",
			token: "",
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := openCodeJWTAccountID(tc.token); got != tc.want {
				t.Errorf("openCodeJWTAccountID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInheritRateLimits_OpenCodeOpenAIInheritsFromCodex(t *testing.T) {
	const accountID = "acct-jwt123"
	claimKey := "https://api.openai.com/auth.chatgpt_account_id"
	jwt := makeJWT(fmt.Sprintf(`{%q:%q}`, claimKey, accountID))

	home := stageAuth(t, map[string]any{
		".local/share/opencode/auth.json": map[string]any{
			"openai-oauth": map[string]any{
				"type":         "oauth",
				"access_token": jwt,
			},
		},
	})

	codex := donorCodex(1000, 35, accountID)
	opencode := emptyWrapper("opencode", "opencode-1")
	InheritRateLimits([]*session.SessionState{codex, opencode}, home, allowAll)

	if opencode.Metrics.RateLimit == nil {
		t.Fatal("expected opencode session to inherit codex snapshot via JWT account_id")
	}
	if opencode.Metrics.RateLimit.SampledAt != 1000 {
		t.Errorf("inherited snapshot timestamp mismatch: got %d", opencode.Metrics.RateLimit.SampledAt)
	}
}

func TestInheritRateLimits_OpenCodeOpenAINoInheritWhenJWTMissing(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".local/share/opencode/auth.json": map[string]any{
			"openai-oauth": map[string]any{
				"type": "oauth",
				// no access_token field
			},
		},
	})

	codex := donorCodex(1000, 35, "acct-xyz")
	opencode := emptyWrapper("opencode", "opencode-1")
	InheritRateLimits([]*session.SessionState{codex, opencode}, home, allowAll)

	if opencode.Metrics.RateLimit != nil {
		t.Fatal("expected no inheritance when access_token is absent")
	}
}

func TestInheritRateLimits_CodexAPIKeyDoesNotDonate(t *testing.T) {
	// Codex on API-key auth (auth_mode != "chatgpt") shouldn't be treated as
	// a subscription donor — its rate_limit reflects the API-key bucket, not
	// the user's ChatGPT subscription. The codex AccountReader yields no ref
	// for a non-chatgpt auth.json (codex TestAccountReader_AuthModes), so the
	// binder stamps none and the snapshot stays unconfirmed ("").
	home := stageAuth(t, map[string]any{
		".pi/agent/auth.json": map[string]any{
			"openai-codex": map[string]any{"type": "oauth", "accountId": "acct-x"},
		},
	})

	codex := donorCodex(1000, 22, "")
	pi := emptyWrapper("pi", "pi-1")
	InheritRateLimits([]*session.SessionState{codex, pi}, home, allowAll)

	if pi.Metrics.RateLimit != nil {
		t.Fatal("api-key codex should not donate to pi")
	}
}

// A claude-code snapshot carrying a confirmed ref IS a donor under
// (anthropic, ref), but no recipient can match it: pi's anthropic block and
// opencode's anthropic-oauth block carry no Anthropic account id.
func TestInheritRateLimits_ClaudeCodeDonorWithRefMatchesNoRecipient(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".pi/agent/auth.json": map[string]any{
			"anthropic": map[string]any{"type": "oauth", "accountId": "acct-claude"},
		},
	})
	cc := donorClaudeCode(2000, 47)
	cc.Metrics.RateLimit.ConfirmedAccountRef = "acct-claude"
	if _, ok := donorKey(cc); !ok {
		t.Fatal("a claude-code snapshot with a confirmed ref should be a donor")
	}
	pi := emptyWrapper("pi", "pi-1")
	InheritRateLimits([]*session.SessionState{cc, pi}, home, allowAll)
	if pi.Metrics.RateLimit != nil {
		t.Fatalf("pi inherited an Anthropic snapshot: %+v", pi.Metrics.RateLimit)
	}
}

// TestProviderForSession pins the adapter map before #1994: claude-code and
// codex resolved unconditionally by adapter name, and pi/opencode resolved
// via configured auth.json credentials (this function used to take a
// userHome parameter to read it). #1994's evidence rule ("a native quota
// snapshot the agent itself emitted is confirmed evidence; a configured
// credential list is NOT evidence; every other session resolves to an
// explicit unknown") replaces both: claude-code/codex now require their OWN
// rate_limit snapshot, and pi/opencode — which never emit one themselves,
// and whose auth.json this function no longer reads at all — always
// resolve to "".
func TestProviderForSession(t *testing.T) {
	if got := ProviderForSession(nil); got != "" {
		t.Errorf("nil: got %q, want \"\"", got)
	}
	if got := ProviderForSession(&session.SessionState{Adapter: "aider"}); got != "" {
		t.Errorf("aider (unmapped): got %q, want \"\"", got)
	}

	// claude-code/codex without their own rate_limit snapshot: no
	// session-specific evidence, so unknown — NOT the adapter's usual
	// provider. This also covers claude-code sessions running against
	// Bedrock/Vertex, which never emit the Anthropic consumer snapshot.
	if got := ProviderForSession(&session.SessionState{Adapter: "claude-code", Metrics: &session.SessionMetrics{}}); got != "" {
		t.Errorf("claude-code (no quota evidence): got %q, want \"\"", got)
	}
	if got := ProviderForSession(&session.SessionState{Adapter: "codex", Metrics: &session.SessionMetrics{}}); got != "" {
		t.Errorf("codex (no quota evidence): got %q, want \"\"", got)
	}
	// ...and nil Metrics must not panic either.
	if got := ProviderForSession(&session.SessionState{Adapter: "claude-code"}); got != "" {
		t.Errorf("claude-code (nil Metrics): got %q, want \"\"", got)
	}

	// claude-code/codex WITH their own rate_limit snapshot: confirmed.
	ccWithSnapshot := donorClaudeCode(1000, 10)
	if got := ProviderForSession(ccWithSnapshot); got != ProviderAnthropic {
		t.Errorf("claude-code (has quota evidence): got %q, want %q", got, ProviderAnthropic)
	}
	codexWithSnapshot := donorCodex(1000, 10, "")
	if got := ProviderForSession(codexWithSnapshot); got != ProviderOpenAI {
		t.Errorf("codex (has quota evidence): got %q, want %q", got, ProviderOpenAI)
	}

	// pi/opencode never emit their own rate_limit snapshot, so they always
	// resolve to unknown for COST ATTRIBUTION — even when auth.json names a
	// provider unambiguously (this function no longer reads auth.json at
	// all, so there is nothing left to stage). That configured credential
	// list is real evidence for INHERITANCE (recipientKey, checked
	// elsewhere against a matching donor's actual snapshot) but not for
	// billing identity here.
	if got := ProviderForSession(emptyWrapper("pi", "pi-1")); got != "" {
		t.Errorf("pi (no own evidence): got %q, want \"\"", got)
	}
	if got := ProviderForSession(emptyWrapper("opencode", "oc-1")); got != "" {
		t.Errorf("opencode (no own evidence): got %q, want \"\"", got)
	}
}

// TestInheritRateLimits_PiIdentityStampDoesNotBlockQuotaDonation is issue
// #2005's proof for a design deviation: Pi's model_change handler
// (core/adapters/inbound/agents/pi/parser.go) now stamps a Pi session's own
// Metrics.RateLimit with identity evidence alone — Provider +
// AttributionQuality, no Windows, no Credits, no ConfirmedAccountRef — the
// instant a model_change names a mapped route. Before InheritRateLimits'
// applyDonors was adjusted to check quota data rather than mere presence,
// that identity-only stamp made hasOwnRateLimit true and silently blocked
// the existing, tested Pi<-Codex quota-window donation
// (TestInheritRateLimits_PiOpenAIInheritsFromCodex above) for every Pi
// session that ever reports a route — which is effectively all of them, per
// the committed regression fixtures. This session carries exactly the shape
// applyPiModelChangeProvider produces.
func TestInheritRateLimits_PiIdentityStampDoesNotBlockQuotaDonation(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".pi/agent/auth.json": map[string]any{
			"openai-codex": map[string]any{
				"type":      "oauth",
				"accountId": "acct-shared",
			},
		},
	})

	codex := donorCodex(1000, 22, "acct-shared")
	pi := &session.SessionState{
		SessionID: "pi-1",
		Adapter:   "pi",
		Metrics: &session.SessionMetrics{
			RateLimit: &session.RateLimitSnapshot{
				Provider:            ProviderOpenAI,
				ObservationSource:   "transcript",
				AttributionEvidence: "pi_transcript_model_change",
				AttributionQuality:  session.AttributionQualityConfirmed,
			},
		},
	}
	InheritRateLimits([]*session.SessionState{codex, pi}, home, allowAll)

	if len(pi.Metrics.RateLimit.Windows) == 0 {
		t.Fatal("expected pi's identity-only stamp to be superseded by the matched codex donor's real quota windows")
	}
	if ProviderForSession(pi) != ProviderOpenAI {
		t.Errorf("ProviderForSession(pi) = %q, want %q — donation must not lose the confirmed provider stamp", ProviderForSession(pi), ProviderOpenAI)
	}
}
