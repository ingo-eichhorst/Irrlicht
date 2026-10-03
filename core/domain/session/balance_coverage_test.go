package session

import "testing"

func confirmedBalance(scope, accountRef, quotaID string) *RateLimitSnapshot {
	return &RateLimitSnapshot{
		Credits:             &CreditsSnapshot{HasCredits: true, Balance: 12.5, BalanceObserved: true, Currency: "USD"},
		Provider:            "openrouter",
		AttributionQuality:  AttributionQualityConfirmed,
		QuotaScope:          scope,
		ConfirmedAccountRef: accountRef,
		QuotaID:             quotaID,
	}
}

// TestBalanceCovers drives every axis BalanceCovers fails closed on. The
// "byok" and "different key" rows are the targets of the two committed
// mutation fixtures named in BalanceCovers' doc comment.
func TestBalanceCovers(t *testing.T) {
	keyA := confirmedBalance(BalanceScopeAPIKey, "", "key-a")
	credits := func(scope, ref string) BalanceUse {
		return BalanceUse{Provider: "openrouter", Arrangement: BillingArrangementProviderCredits, Scope: scope, ScopeRef: ref}
	}
	unconfirmed := confirmedBalance(BalanceScopeAPIKey, "", "key-a")
	unconfirmed.AttributionQuality = ""
	noCredits := confirmedBalance(BalanceScopeAPIKey, "", "key-a")
	noCredits.Credits = nil

	cases := []struct {
		name string
		bal  *RateLimitSnapshot
		use  BalanceUse
		want bool
	}{
		{"same provider, credits arrangement, same key", keyA, credits(BalanceScopeAPIKey, "key-a"), true},
		{"same account", confirmedBalance(BalanceScopeAccount, "acct-1", ""), credits(BalanceScopeAccount, "acct-1"), true},

		{"a credits balance never covers a BYOK request", keyA,
			BalanceUse{Provider: "openrouter", Arrangement: BillingArrangementBYOK, Scope: BalanceScopeAPIKey, ScopeRef: "key-a"}, false},
		{"an unestablished arrangement is not credits", keyA,
			BalanceUse{Provider: "openrouter", Scope: BalanceScopeAPIKey, ScopeRef: "key-a"}, false},

		{"one key's balance never covers a session on a different key", keyA, credits(BalanceScopeAPIKey, "key-b"), false},
		{"a different account", confirmedBalance(BalanceScopeAccount, "acct-1", ""), credits(BalanceScopeAccount, "acct-2"), false},
		{"a balance bound to no confirmed scope covers nothing", confirmedBalance(BalanceScopeAccount, "", ""), credits(BalanceScopeAccount, ""), false},
		{"an unrecognised balance scope covers nothing", confirmedBalance("workspace", "acct-1", "acct-1"), credits("workspace", "acct-1"), false},
		{"scope kinds must agree", confirmedBalance(BalanceScopeAccount, "x", "x"), credits(BalanceScopeAPIKey, "x"), false},

		{"a different provider", keyA, BalanceUse{Provider: ProviderDeepSeek, Arrangement: BillingArrangementProviderCredits, Scope: BalanceScopeAPIKey, ScopeRef: "key-a"}, false},
		{"an unconfirmed balance", unconfirmed, credits(BalanceScopeAPIKey, "key-a"), false},
		{"a snapshot with no credits", noCredits, credits(BalanceScopeAPIKey, "key-a"), false},
		{"nil", nil, credits(BalanceScopeAPIKey, "key-a"), false},
	}
	for _, c := range cases {
		if got := BalanceCovers(c.bal, c.use); got != c.want {
			t.Errorf("%s: BalanceCovers = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestBillingArrangementsIsClosed pins the vocabulary: "" is not a member,
// and is what an unestablished arrangement reads as.
func TestBillingArrangementsIsClosed(t *testing.T) {
	if len(BillingArrangements) != 2 {
		t.Fatalf("BillingArrangements = %v, want exactly provider-credits and byok", BillingArrangements)
	}
	for _, a := range BillingArrangements {
		if a == "" {
			t.Fatal(`"" must not be a billing arrangement — it means "not established"`)
		}
	}
}
