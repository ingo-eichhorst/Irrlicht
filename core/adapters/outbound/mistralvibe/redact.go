package mistralvibe

import "encoding/json"

// RedactUsage returns the allowlisted json object of a raw response, and
// nothing else: the shape replaydata/providers/mistral's fixture holds. It
// decodes through the same allowlist ParseUsage does, so a field this package
// never learned to read cannot reach a committed fixture.
func RedactUsage(raw []byte) ([]byte, error) {
	u, err := decodeAllowlisted(raw)
	if err != nil {
		return nil, err
	}
	return json.Marshal(u)
}
