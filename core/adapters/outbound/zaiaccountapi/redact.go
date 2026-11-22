package zaiaccountapi

import "encoding/json"

// RedactQuotaResponse is the redaction boundary for a captured response: it
// decodes raw through quotaResponse (parser.go, the same allowlist Parse
// uses) and re-marshals only that, so a field this package never learned
// to read cannot reach a committed fixture. Absent fields stay absent: the
// allowlist holds pointers with omitempty, so the first probe's missing
// five-hour nextResetTime survives as missing rather than becoming 0.
func RedactQuotaResponse(raw []byte) ([]byte, error) {
	var resp quotaResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	return json.Marshal(resp)
}
