//go:build darwin

package museaccountapi

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
// Not a measured probe cost — mirrors
// accountquota.keychainTimeout's own reasoning verbatim: a local, offline
// keychain lookup should return in well under a second, with headroom for a
// loaded machine. A Keychain ACL PROMPT is a distinct, much slower case
// (herdr-agent-quota budgets 5s for exactly that) — this package's own live
// probe (museaccountapi_live_probe_test.go) wraps its one real invocation in
// an outer `timeout` at the shell level rather than raising this constant,
// so an unattended prompt fails the probe rather than hanging it.
const keychainTimeout = 3 * time.Second

// securityPath is resolved once from a fixed, unwriteable directory set
// rather than trusted PATH (core/pkg/pathutil's own rationale, go:S4036) —
// mirrors accountquota.securityPath.
var securityPath = pathutil.MustResolve("security")

// platformKeychainRawLookup shells out to `security find-generic-password -w
// -s <service> -a <account>`, returning the RAW stdout (Muse's own item is a
// JSON blob, not a bare secret — see keychainBlob's doc comment; unwrapping
// happens in credential.go, never here). bounded is derived from the
// caller's ctx, never a bare context.Background()/TODO()
// (core/architecture_shellout_test.go).
func platformKeychainRawLookup(ctx context.Context, service, account string) (string, error) {
	bounded, cancel := context.WithTimeout(ctx, keychainTimeout)
	defer cancel()
	cmd := exec.CommandContext(bounded, securityPath, "find-generic-password", "-w", "-s", service, "-a", account)
	out, err := cmd.Output()
	if err != nil {
		if !shellout.Answered(err) {
			return "", fmt.Errorf("museaccountapi: security did not answer (killed or timed out) looking up %s/%s", service, account)
		}
		return "", fmt.Errorf("museaccountapi: no keychain item found for %s/%s", service, account)
	}
	return strings.TrimRight(string(out), "\n"), nil
}
