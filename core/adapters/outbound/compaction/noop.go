// Package compaction implements the outbound.TextCompactor strategy seam
// (issue #759): turning bloated session text into a terse one-line sidebar
// headline. NoopCompactor is the identity (no compaction); DeterministicCompactor
// is the default pure-heuristic strategy. A future LLMCompactor can join here
// without touching callers.
package compaction

import "irrlicht/core/ports/outbound"

// NoopCompactor returns the text unchanged. It is the explicit identity used
// where compaction is disabled, and the behaviour a nil compactor falls back to
// in the converter seam.
type NoopCompactor struct{}

var _ outbound.TextCompactor = NoopCompactor{}

// Compact returns text unchanged regardless of kind.
func (NoopCompactor) Compact(text string, _ outbound.CompactKind) string {
	return text
}
