package deepseekaccountapi

import "encoding/json"

// redactedResponse is the only shape a captured DeepSeek balance fixture may
// hold on disk: the same allowlist balanceResponse decodes, re-marshaled
// through this distinct type so a field this package never learned to read
// cannot reach a committed fixture.
type redactedResponse struct {
	IsAvailable  *bool                 `json:"is_available,omitempty"`
	BalanceInfos []redactedBalanceInfo `json:"balance_infos,omitempty"`
}

// redactedBalanceInfo keeps an absent field absent: a capture must be able to
// tell a missing currency or amount from an empty one, which is the shape
// question a live probe exists to settle.
type redactedBalanceInfo struct {
	Currency        *string `json:"currency,omitempty"`
	TotalBalance    *string `json:"total_balance,omitempty"`
	GrantedBalance  *string `json:"granted_balance,omitempty"`
	ToppedUpBalance *string `json:"topped_up_balance,omitempty"`
}

// RedactBalanceResponse decodes raw through the same field allowlist
// balanceResponse names (parser.go), with pointers so absence survives, and
// re-marshals only that subset. The live-probe program calls
// it before any byte reaches disk.
func RedactBalanceResponse(raw []byte) ([]byte, error) {
	var resp struct {
		IsAvailable  *bool                 `json:"is_available"`
		BalanceInfos []redactedBalanceInfo `json:"balance_infos"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	return json.MarshalIndent(redactedResponse(resp), "", "  ")
}
