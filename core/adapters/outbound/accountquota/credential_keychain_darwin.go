//go:build darwin

package accountquota

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"irrlicht/core/pkg/pathutil"
	"irrlicht/core/pkg/shellout"
)

// keychainTimeout bounds one `security find-generic-password` invocation.
// Not a measured probe cost (docs/testing-contracts.md's convention for the
// figures in core/internal/costreport) — a deliberate ceiling for a local,
// offline keychain lookup that should return in well under a second, with
// generous headroom for a loaded machine.
const keychainTimeout = 3 * time.Second

// securityPath is resolved once from a fixed, unwriteable directory set
// rather than trusted PATH (core/pkg/pathutil's own rationale, go:S4036).
var securityPath = pathutil.MustResolve("security")

// platformKeychainLookup shells out to `security find-generic-password -w -s
// <service> -a <account>`, which prints just the stored password to stdout on
// success. bounded is derived from the caller's ctx (never a bare
// context.Background()/TODO() — core/architecture_shellout_test.go).
func platformKeychainLookup(ctx context.Context, service, account string) (string, error) {
	bounded, cancel := context.WithTimeout(ctx, keychainTimeout)
	defer cancel()
	cmd := exec.CommandContext(bounded, securityPath, "find-generic-password", "-w", "-s", service, "-a", account)
	out, err := cmd.Output()
	if err != nil {
		// shellout.Answered with no explicit codes: ANY normal exit of
		// `security` is an answer (it exits non-zero for "item not found",
		// which is a real, reportable verdict) — only a killed/never-started
		// child is a non-answer. See core/pkg/shellout's own doc for the
		// plutil precedent this mirrors.
		if !shellout.Answered(err) {
			return "", fmt.Errorf("accountquota: security did not answer (killed or timed out) looking up %s/%s", service, account)
		}
		// security's own stderr can echo the service/account it was asked
		// for but never a secret; it is still left out here because this
		// function has no need to widen what an error crossing it can carry.
		return "", fmt.Errorf("accountquota: no keychain item found for %s/%s", service, account)
	}
	return strings.TrimRight(string(out), "\n"), nil
}
