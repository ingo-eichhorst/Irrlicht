package push

// Pairing codes are the one-time bridge that carries a workspace from an
// authenticated client token into the device token minted at redeem
// (docs/mobile-notifications-arc42.md §8.1, §6.1). They live in RAM only:
// a relay restart mid-pairing just means regenerating the code (§8.6).
//
// The generation/redeem algorithm itself — alphabet, TTL, outstanding cap,
// failure window, constant-time match, uniform failure — is lifted into
// core/pkg/onetimecode (#1963), shared with desktop enrollment
// (core/cmd/irrlichtrelay's /api/v1/enroll routes: the CLI-mintable case
// that RAM storage cannot serve). This file wires that leaf over an
// in-memory onetimecode.Store, so the RAM-only shape above is unchanged.
// MintCode/Redeem stay thin wrappers so no caller or test in this package
// moves. The re-exported constants and errors below exist because
// codes_test.go — this package's predecessor test, kept as the lock on this
// lift (AGENTS.md: a rewritten guard replays what it replaced) — references
// them by these exact bare names.

import (
	"time"

	"irrlicht/core/pkg/onetimecode"
)

const (
	codeAlphabet        = onetimecode.Alphabet
	CodeTTL             = onetimecode.CodeTTL
	maxOutstandingCodes = onetimecode.MaxOutstanding
	redeemFailureLimit  = onetimecode.FailureLimit
	redeemFailureWindow = onetimecode.FailureWindow
)

var (
	// ErrTooManyCodes, ErrRateLimited and ErrCodeInvalid alias the leaf's
	// sentinel error values (not merely equal messages) — codes_test.go's
	// TestRedeemFailureIsUniform compares one of these with `!=`, which only
	// holds because it is the identical value Redeem below returns.
	ErrTooManyCodes = onetimecode.ErrTooManyCodes
	ErrRateLimited  = onetimecode.ErrRateLimited
	ErrCodeInvalid  = onetimecode.ErrCodeInvalid
)

// MintCode mints a single-use pairing code bound to workspace, presented as
// XXXX-XXXX. A thin wrapper over the shared leaf's Manager.Mint; pairing
// codes carry no label.
func (s *Service) MintCode(workspace string) (code string, ttl time.Duration, err error) {
	return s.codes.Mint(workspace, "")
}

// Redeem exchanges a code for the workspace it was minted in, consuming it.
// A thin wrapper over the shared leaf's Manager.Redeem; the label return is
// discarded because pairing codes never carry one.
func (s *Service) Redeem(code string) (workspace string, err error) {
	workspace, _, err = s.codes.Redeem(code)
	return workspace, err
}
