//go:build darwin

package accountquota

import (
	"context"
	"time"

	"irrlicht/core/pkg/keychainlookup"
)

// keychainTimeout bounds one `security find-generic-password` invocation.
// Not a measured probe cost (docs/testing-contracts.md's convention for the
// figures in core/internal/costreport) — a deliberate ceiling for a local,
// offline keychain lookup that should return in well under a second, with
// generous headroom for a loaded machine. Kept as this package's OWN
// constant (never merged with museaccountapi's — that package's own
// keychainTimeout is 15s, MEASURED against a real prompt/latency scenario
// #2003's own never-prompted use never exercised) per core/pkg/shellout's
// own stated philosophy: "each package names its own [timeout]... merging
// them would be picking one number by proximity."
const keychainTimeout = 3 * time.Second

// platformKeychainLookup shells out to `security find-generic-password -w -s
// <service> -a <account>` via the shared core/pkg/keychainlookup primitive
// (extracted by review, issue #2007: this function and
// museaccountapi.platformKeychainRawLookup had grown byte-identical
// exec.CommandContext + shellout.Answered + TrimRight mechanics,
// independently), which prints just the stored password to stdout on
// success. bounded is derived from the caller's ctx (never a bare
// context.Background()/TODO() — core/architecture_shellout_test.go).
func platformKeychainLookup(ctx context.Context, service, account string) (string, error) {
	return keychainlookup.Raw(ctx, service, account, keychainTimeout)
}
