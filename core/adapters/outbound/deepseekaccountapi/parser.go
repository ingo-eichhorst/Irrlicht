package deepseekaccountapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"irrlicht/core/domain/session"
)

// The response schema, from https://api-docs.deepseek.com/api/get-user-balance/
// (read 2026-10-03): `is_available` (boolean, required) and `balance_infos`
// (array, required) whose entries carry `currency` (enum CNY, USD),
// `total_balance`, `granted_balance` and `topped_up_balance` — all four
// required, and the three amounts are STRINGS ("110.00"), not numbers.
// total_balance is documented as "the total available balance, including the
// granted balance and the topped-up balance"; the page states no equation,
// so this parser does not check that the components sum to the total.
//
// balanceResponse is the strict allowlist this package ever reads. Decoding
// into it is the redaction boundary: a field not named here is dropped by
// json.Unmarshal and is unreachable from the rest of the package.
type balanceResponse struct {
	IsAvailable  *bool         `json:"is_available"`
	BalanceInfos []balanceInfo `json:"balance_infos"`
}

type balanceInfo struct {
	Currency        string  `json:"currency"`
	TotalBalance    *string `json:"total_balance"`
	GrantedBalance  *string `json:"granted_balance"`
	ToppedUpBalance *string `json:"topped_up_balance"`
}

// ErrNoBalance: the response named no balance at all (an empty or absent
// balance_infos). This is NOT a zero balance — a zero arrives as an entry
// whose total_balance is "0.00" — so it produces no snapshot rather than a
// manufactured zero.
var ErrNoBalance = errors.New("deepseekaccountapi: response carries no balance entry")

// ErrMultipleCurrencies: balance_infos carried more than one entry. The
// schema allows one entry per currency, and session.CreditsSnapshot holds
// one currency. Summing CNY and USD is meaningless and picking one would
// silently drop the other, so the parser refuses instead. Widening
// CreditsSnapshot is gated on a REAL multi-currency response (#2009 §5), and
// none has been observed.
var ErrMultipleCurrencies = errors.New("deepseekaccountapi: response carries balances in more than one currency")

// BuildSnapshot parses a raw balance response into a session.RateLimitSnapshot
// whose Credits carry the currency, all three components, and an observed
// flag that survives a zero. It rejects, rather than repairs, anything it
// cannot read with confidence: a missing is_available, a missing or empty
// currency (the clients render an unlabeled balance with "$", so an empty
// currency on a CNY account would print a wrong unit), a missing amount, and
// an amount that does not parse as a finite number.
//
// Four states stay distinct on the result: an observed zero is
// BalanceObserved=true with Balance=0 (and Total pointing at 0); an observed
// balance is never Unlimited (DeepSeek has no unlimited state); "unknown"
// and "missing" are both an error here, never a snapshot.
func BuildSnapshot(body []byte, now time.Time) (session.RateLimitSnapshot, error) {
	var resp balanceResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return session.RateLimitSnapshot{}, fmt.Errorf("deepseekaccountapi: response is not valid JSON: %w", err)
	}
	if resp.IsAvailable == nil {
		return session.RateLimitSnapshot{}, errors.New("deepseekaccountapi: response has no is_available")
	}
	switch len(resp.BalanceInfos) {
	case 0:
		return session.RateLimitSnapshot{}, ErrNoBalance
	case 1:
	default:
		return session.RateLimitSnapshot{}, ErrMultipleCurrencies
	}
	info := resp.BalanceInfos[0]

	currency := strings.TrimSpace(info.Currency)
	if currency == "" {
		return session.RateLimitSnapshot{}, errors.New("deepseekaccountapi: balance entry has no currency")
	}
	total, err := amount("total_balance", info.TotalBalance)
	if err != nil {
		return session.RateLimitSnapshot{}, err
	}
	granted, err := amount("granted_balance", info.GrantedBalance)
	if err != nil {
		return session.RateLimitSnapshot{}, err
	}
	toppedUp, err := amount("topped_up_balance", info.ToppedUpBalance)
	if err != nil {
		return session.RateLimitSnapshot{}, err
	}

	credits := &session.CreditsSnapshot{
		HasCredits: *resp.IsAvailable,
		Balance:    total,
		Total:      &total,
		Granted:    &granted,
		ToppedUp:   &toppedUp,
	}
	credits.Currency = currency
	credits.BalanceObserved = true

	return session.RateLimitSnapshot{
		Credits:   credits,
		SampledAt: now.Unix(),

		Provider: session.ProviderDeepSeek,
		// The response carries no account identifier, so the balance is
		// bound to no confirmed account: session.BalanceCovers then lets it
		// cover no session, rather than every DeepSeek session.
		QuotaScope:          session.BalanceScopeAccount,
		ConfirmedAccountRef: "",
		AttributionEvidence: "deepseek_balance_api",
		ObservationSource:   "account_api",
		AttributionQuality:  session.AttributionQualityConfirmed,
		LastSuccessAt:       now.Unix(),
		LastAttemptAt:       now.Unix(),
	}, nil
}

// amount parses one documented string amount. A missing amount is an error,
// not a zero: the schema marks all three required, and treating absence as
// 0.00 is the zero-versus-missing fold #2009 §1.3 forbids.
func amount(field string, v *string) (float64, error) {
	if v == nil {
		return 0, fmt.Errorf("deepseekaccountapi: balance entry has no %s", field)
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(*v), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("deepseekaccountapi: %s %q is not a finite number", field, *v)
	}
	return f, nil
}
