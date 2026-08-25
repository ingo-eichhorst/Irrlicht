package tailer

import "time"

// Optional readers for JSON fields whose ABSENCE is meaningful.
//
// They live in their own file rather than beside the parser types because
// they are generic field accessors, not part of ParsedEvent's vocabulary —
// and parser.go is already one of this package's largest files.

// OptInt reads a JSON number as *int, returning nil when m is nil, the key is
// absent, or the value is not a number.
//
// The OPTIONAL counterpart to the zero-defaulting reads adapters use for
// ordinary fields, and it lives beside the other shared parser primitives
// rather than in each adapter because absence and zero are different facts
// wherever it is used. #1799 needed it in two adapters at once — claudecode's
// `error.status` and copilot's `statusCode` — and one of copilot's two recorded
// shapes omits the key entirely. With a plain int that absence reads as HTTP 0,
// which is a fabricated status code rather than a missing one. See
// session.SessionError for why every numeric field on it is a pointer.
//
// encoding/json decodes every JSON number into float64, so that is the only
// type asserted; an int cannot occur from a decoded transcript line.
func OptInt(m map[string]interface{}, key string) *int {
	if m == nil {
		return nil
	}
	f, ok := m[key].(float64)
	if !ok {
		return nil
	}
	v := int(f)
	return &v
}

// OptDurationFromMillis reads a fractional-millisecond JSON number as a
// *time.Duration, returning nil under the same conditions as OptInt.
//
// Fractional on purpose: claudecode writes retryInMs as a float
// (616.4520045919932 in the recordings), so an integer-millisecond read would
// truncate it. Paired with OptInt here so the "an absent number is not zero"
// argument is stated once for both rather than re-derived per adapter.
func OptDurationFromMillis(m map[string]interface{}, key string) *time.Duration {
	if m == nil {
		return nil
	}
	f, ok := m[key].(float64)
	if !ok {
		return nil
	}
	d := time.Duration(f * float64(time.Millisecond))
	return &d
}
