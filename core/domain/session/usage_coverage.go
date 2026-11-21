package session

import "strings"

// TokenType names one kind of token a usage record can count (#2061). The
// set is closed; TokenTypes returns it in its canonical order.
type TokenType string

const (
	// TokenInput is fresh (non-cached) prompt input.
	TokenInput TokenType = "input"
	// TokenOutput is generated output.
	TokenOutput TokenType = "output"
	// TokenCacheRead is prompt input served from a provider cache.
	TokenCacheRead TokenType = "cache_read"
	// TokenCacheWrite is prompt input written to a provider cache —
	// SessionMetrics.CumCacheCreationTokens.
	TokenCacheWrite TokenType = "cache_write"
)

// TokenTypes returns the closed token-type vocabulary in canonical order.
func TokenTypes() []TokenType {
	return []TokenType{TokenInput, TokenOutput, TokenCacheRead, TokenCacheWrite}
}

// IsTokenType reports whether t is a member of TokenTypes.
func IsTokenType(t TokenType) bool {
	for _, known := range TokenTypes() {
		if t == known {
			return true
		}
	}
	return false
}

// UsageCoverage is an adapter's own declaration of what its usage accounting
// covers (#2061). It is declared beside the adapter's parser, carried on
// agent.Agent.Usage, and stamped onto each session's metrics by the metrics
// adapter, so a consumer can tell a counted zero from a count nobody took.
//
// Nil means the adapter has declared nothing, and a consumer must then treat
// every token count and every token-priced cost as unknown. That default is
// deliberate: an adapter is covered only once someone has read its parser and
// written down what it reads.
type UsageCoverage struct {
	// TokenTypes are the token kinds the adapter's parser reads from the
	// agent's own usage record and accumulates into the Cum*Tokens fields. A
	// type missing here is one Irrlicht never counts for this adapter, so its
	// Cum*Tokens value is 0 by omission, not by measurement.
	TokenTypes []TokenType `json:"token_types"`

	// ExcludesSubagents is true when the parent session's token and cost
	// counts are known to exclude the usage of its child sessions, so a
	// consumer that lists children beside their parent does not count the
	// same tokens twice. False means "not established", never "includes".
	ExcludesSubagents bool `json:"excludes_subagents,omitempty"`
}

// Observes reports whether the coverage declares t. A nil coverage observes
// nothing.
func (c *UsageCoverage) Observes(t TokenType) bool {
	if c == nil {
		return false
	}
	for _, have := range c.TokenTypes {
		if have == t {
			return true
		}
	}
	return false
}

// ObservesAll reports whether the coverage declares every token type.
func (c *UsageCoverage) ObservesAll() bool {
	for _, t := range TokenTypes() {
		if !c.Observes(t) {
			return false
		}
	}
	return true
}

// PricingSourceProviderReported is the SessionMetrics.PricingSource part for
// a cost the agent reported itself rather than one priced from Irrlicht's
// price table (#2061).
const PricingSourceProviderReported = "provider-reported"

// PricingSourceSeparator joins the parts of a PricingSource that names more
// than one source.
const PricingSourceSeparator = "+"

// PricedFromTable reports whether a PricingSource includes a part priced from
// Irrlicht's price table — any part other than PricingSourceProviderReported.
// Such a cost is only as complete as the token types the adapter counts.
func PricedFromTable(source string) bool {
	for _, part := range strings.Split(source, PricingSourceSeparator) {
		if part != "" && part != PricingSourceProviderReported {
			return true
		}
	}
	return false
}
