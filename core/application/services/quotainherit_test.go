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
			},
		},
	}
}

func donorCodex(sampledAt int64, percent float64) *session.SessionState {
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
			},
		},
	}
}

func emptyWrapper(adapter, id string) *session.SessionState {
	return &session.SessionState{
		SessionID: id,
		Adapter:   adapter,
		Metrics:   &session.SessionMetrics{},
	}
}

func TestInheritRateLimits_PiOpenAIInheritsFromCodex(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".codex/auth.json": map[string]any{
			"auth_mode": "chatgpt",
			"tokens":    map[string]any{"account_id": "acct-shared"},
		},
		".pi/agent/auth.json": map[string]any{
			"openai-codex": map[string]any{
				"type":      "oauth",
				"accountId": "acct-shared",
			},
		},
	})

	codex := donorCodex(1000, 22)
	pi := emptyWrapper("pi", "pi-1")
	InheritRateLimits([]*session.SessionState{codex, pi}, home)

	if pi.Metrics.RateLimit == nil {
		t.Fatal("expected pi session to inherit codex snapshot")
	}
	if pi.Metrics.RateLimit.SampledAt != 1000 {
		t.Errorf("inherited snapshot timestamp mismatch: got %d", pi.Metrics.RateLimit.SampledAt)
	}
}

func TestInheritRateLimits_PiNotInheritsOnAccountMismatch(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".codex/auth.json": map[string]any{
			"auth_mode": "chatgpt",
			"tokens":    map[string]any{"account_id": "acct-codex"},
		},
		".pi/agent/auth.json": map[string]any{
			"openai-codex": map[string]any{
				"type":      "oauth",
				"accountId": "acct-different",
			},
		},
	})

	codex := donorCodex(1000, 22)
	pi := emptyWrapper("pi", "pi-1")
	InheritRateLimits([]*session.SessionState{codex, pi}, home)

	if pi.Metrics.RateLimit != nil {
		t.Fatal("expected no inheritance when account ids don't match")
	}
}

func TestInheritRateLimits_PiAnthropicInheritsFromClaudeCode(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".pi/agent/auth.json": map[string]any{
			"anthropic": map[string]any{"type": "oauth"},
		},
	})

	cc := donorClaudeCode(2000, 47)
	pi := emptyWrapper("pi", "pi-1")
	InheritRateLimits([]*session.SessionState{cc, pi}, home)

	if pi.Metrics.RateLimit == nil {
		t.Fatal("expected pi(anthropic) to inherit from claude code")
	}
	if pi.Metrics.RateLimit.SampledAt != 2000 {
		t.Errorf("expected donor snapshot, got SampledAt=%d", pi.Metrics.RateLimit.SampledAt)
	}
}

func TestInheritRateLimits_PiPrefersOpenAIOverAnthropic(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".codex/auth.json": map[string]any{
			"auth_mode": "chatgpt",
			"tokens":    map[string]any{"account_id": "acct-x"},
		},
		".pi/agent/auth.json": map[string]any{
			"openai-codex": map[string]any{"type": "oauth", "accountId": "acct-x"},
			"anthropic":    map[string]any{"type": "oauth"},
		},
	})

	codex := donorCodex(1000, 22)
	cc := donorClaudeCode(2000, 47)
	pi := emptyWrapper("pi", "pi-1")
	InheritRateLimits([]*session.SessionState{codex, cc, pi}, home)

	if pi.Metrics.RateLimit == nil {
		t.Fatal("expected inheritance")
	}
	if pi.Metrics.RateLimit.SampledAt != 1000 {
		t.Errorf("expected codex (openai) donor wins; got SampledAt=%d", pi.Metrics.RateLimit.SampledAt)
	}
}

func TestInheritRateLimits_FreshestDonorWinsOnTies(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".pi/agent/auth.json": map[string]any{
			"anthropic": map[string]any{"type": "oauth"},
		},
	})

	stale := donorClaudeCode(1000, 80)
	fresh := donorClaudeCode(5000, 20)
	pi := emptyWrapper("pi", "pi-1")
	InheritRateLimits([]*session.SessionState{stale, fresh, pi}, home)

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
	InheritRateLimits([]*session.SessionState{cc, pi}, home)

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
	InheritRateLimits([]*session.SessionState{pi}, home)

	if pi.Metrics.RateLimit != nil {
		t.Fatal("expected no inheritance when no donor exists")
	}
}

// writeCodexAuth stages a synthetic ~/.codex/auth.json under home.
// Raw bytes (`raw`) trump structured body when both are set, so we
// can exercise malformed-JSON cases.
func writeCodexAuth(t *testing.T, home string, body any, raw string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, ".codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".codex", "auth.json")
	var data []byte
	if raw != "" {
		data = []byte(raw)
	} else {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadCodexAccountID_ErrorPaths(t *testing.T) {
	cases := []struct {
		name string
		body any
		raw  string
		want string
	}{
		{
			name: "valid chatgpt mode",
			body: map[string]any{"auth_mode": "chatgpt", "tokens": map[string]any{"account_id": "acct-x"}},
			want: "acct-x",
		},
		{
			name: "api-key mode returns empty",
			body: map[string]any{"auth_mode": "apikey", "tokens": map[string]any{"account_id": "ignored"}},
			want: "",
		},
		{
			name: "missing auth_mode returns empty",
			body: map[string]any{"tokens": map[string]any{"account_id": "ignored"}},
			want: "",
		},
		{
			name: "missing tokens field returns empty",
			body: map[string]any{"auth_mode": "chatgpt"},
			want: "",
		},
		{
			name: "malformed JSON returns empty",
			raw:  "{not json",
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			writeCodexAuth(t, home, tc.body, tc.raw)
			if got := readCodexAccountID(home); got != tc.want {
				t.Errorf("readCodexAccountID = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadCodexAccountID_MissingFileReturnsEmpty(t *testing.T) {
	if got := readCodexAccountID(t.TempDir()); got != "" {
		t.Errorf("expected empty for missing file, got %q", got)
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
		".codex/auth.json": map[string]any{
			"auth_mode": "chatgpt",
			"tokens":    map[string]any{"account_id": accountID},
		},
		".local/share/opencode/auth.json": map[string]any{
			"openai-oauth": map[string]any{
				"type":         "oauth",
				"access_token": jwt,
			},
		},
	})

	codex := donorCodex(1000, 35)
	opencode := emptyWrapper("opencode", "opencode-1")
	InheritRateLimits([]*session.SessionState{codex, opencode}, home)

	if opencode.Metrics.RateLimit == nil {
		t.Fatal("expected opencode session to inherit codex snapshot via JWT account_id")
	}
	if opencode.Metrics.RateLimit.SampledAt != 1000 {
		t.Errorf("inherited snapshot timestamp mismatch: got %d", opencode.Metrics.RateLimit.SampledAt)
	}
}

func TestInheritRateLimits_OpenCodeOpenAINoInheritWhenJWTMissing(t *testing.T) {
	home := stageAuth(t, map[string]any{
		".codex/auth.json": map[string]any{
			"auth_mode": "chatgpt",
			"tokens":    map[string]any{"account_id": "acct-xyz"},
		},
		".local/share/opencode/auth.json": map[string]any{
			"openai-oauth": map[string]any{
				"type": "oauth",
				// no access_token field
			},
		},
	})

	codex := donorCodex(1000, 35)
	opencode := emptyWrapper("opencode", "opencode-1")
	InheritRateLimits([]*session.SessionState{codex, opencode}, home)

	if opencode.Metrics.RateLimit != nil {
		t.Fatal("expected no inheritance when access_token is absent")
	}
}

func TestInheritRateLimits_CodexAPIKeyDoesNotDonate(t *testing.T) {
	// Codex on API-key auth (auth_mode != "chatgpt") shouldn't be
	// treated as a subscription donor — its rate_limit reflects the
	// API-key bucket, not the user's ChatGPT subscription.
	home := stageAuth(t, map[string]any{
		".codex/auth.json": map[string]any{
			"auth_mode": "apikey",
			"tokens":    map[string]any{"account_id": "ignored"},
		},
		".pi/agent/auth.json": map[string]any{
			"openai-codex": map[string]any{"type": "oauth", "accountId": "acct-x"},
		},
	})

	codex := donorCodex(1000, 22)
	pi := emptyWrapper("pi", "pi-1")
	InheritRateLimits([]*session.SessionState{codex, pi}, home)

	if pi.Metrics.RateLimit != nil {
		t.Fatal("api-key codex should not donate to pi")
	}
}
