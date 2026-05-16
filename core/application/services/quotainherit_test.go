package services

import (
	"encoding/json"
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
