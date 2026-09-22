//go:build darwin

// Package keychainlookup is the shared `security find-generic-password`
// shellout mechanics behind every macOS Keychain credential route in this
// repo. Extracted (review finding on #2007) from
// core/adapters/outbound/accountquota's own platformKeychainLookup: that
// package's #2003-authored version and #2007's museaccountapi package had
// independently grown byte-identical exec.CommandContext + shellout.Answered
// + TrimRight mechanics, differing only in their timeout constant and what
// the caller does with the returned string afterward. core/pkg/shellout's
// own doc comment deliberately holds no shared TIMEOUT ("each package names
// its own... merging them would be picking one number by proximity") — this
// package keeps that: Raw takes timeout as a parameter, so
// core/adapters/outbound/accountquota (3s) and
// core/adapters/outbound/museaccountapi (15s, measured — see that package's
// own doc comment) each keep their own named constant, only the shellout
// PLUMBING is shared.
package keychainlookup

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"irrlicht/core/pkg/pathutil"
	"irrlicht/core/pkg/shellout"
)

// SecurityPath is resolved once from a fixed, unwriteable directory set
// rather than trusted PATH (core/pkg/pathutil's own rationale, go:S4036).
var SecurityPath = pathutil.MustResolve("security")

// Raw shells out to `security find-generic-password -w -s <service> -a
// <account>`, which prints just the stored password (or, for an item like
// Muse's whose value is itself a JSON blob, that raw blob — unwrapping it is
// the CALLER's job, never this package's) to stdout on success. bounded is
// derived from ctx (never a bare context.Background()/TODO() —
// core/architecture_shellout_test.go's rule, satisfied at every call site
// that builds ctx from its own caller).
func Raw(ctx context.Context, service, account string, timeout time.Duration) (string, error) {
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(bounded, SecurityPath, "find-generic-password", "-w", "-s", service, "-a", account)
	out, err := cmd.Output()
	if err != nil {
		// shellout.Answered with no explicit codes: ANY normal exit of
		// `security` is an answer (it exits non-zero for "item not found",
		// which is a real, reportable verdict) — only a killed/never-started
		// child is a non-answer. See core/pkg/shellout's own doc for the
		// plutil precedent this mirrors.
		if !shellout.Answered(err) {
			return "", fmt.Errorf("keychainlookup: security did not answer (killed or timed out) looking up %s/%s", service, account)
		}
		// security's own stderr can echo the service/account it was asked
		// for but never a secret; it is still left out here because this
		// function has no need to widen what an error crossing it can carry.
		return "", fmt.Errorf("keychainlookup: no keychain item found for %s/%s", service, account)
	}
	return strings.TrimRight(string(out), "\n"), nil
}
