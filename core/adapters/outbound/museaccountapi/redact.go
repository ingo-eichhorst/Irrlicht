package museaccountapi

import "encoding/json"

// redactedResponse is the ONLY shape a captured Muse account-quota fixture
// may hold on disk — the same allowlist subscriptionResponse decodes
// (parser.go), re-marshaled through THIS distinct type rather than the raw
// bytes so a field this package never learned to read structurally cannot
// reach a committed fixture even by accident (e.g. a caller passing the raw
// response straight to os.WriteFile instead of through this function).
type redactedResponse struct {
	IsSubsActive *bool              `json:"is_subs_active,omitempty"`
	SubsTierName string             `json:"subs_tier_name,omitempty"`
	SubsUsage    *redactedSubsUsage `json:"subs_usage,omitempty"`
}

type redactedSubsUsage struct {
	Window *quotaWindowJSON `json:"window,omitempty"`
	Weekly *quotaWindowJSON `json:"weekly,omitempty"`
}

// RedactSubscriptionResponse is issue #2007 §1.3 and §7's redaction
// boundary: "Retain only approved, redacted fields. A recorded fixture must
// carry nothing else." It decodes raw through subscriptionResponse's own
// allowlist (parser.go — the same decode BuildSnapshot uses) and
// re-marshals ONLY that allowlisted subset, so a field this package was
// never told to read (the pinned herdr source: the account's API key, name,
// email) cannot survive into the returned bytes even if raw carries it.
// This is what the live-probe program (museaccountapi_live_probe_test.go)
// calls before ANY byte reaches disk, and what
// TestRedactSubscriptionResponse_DropsNonApprovedFields (mutation fixture
// #2) protects.
func RedactSubscriptionResponse(raw []byte) ([]byte, error) {
	var resp subscriptionResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	redacted := redactedResponse{
		IsSubsActive: resp.IsSubsActive,
		SubsTierName: resp.SubsTierName,
	}
	if resp.SubsUsage != nil {
		redacted.SubsUsage = &redactedSubsUsage{
			Window: resp.SubsUsage.Window,
			Weekly: resp.SubsUsage.Weekly,
		}
	}
	return json.MarshalIndent(redacted, "", "  ")
}
