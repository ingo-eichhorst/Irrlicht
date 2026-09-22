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
// MEASURED, not assumed: issue #2007's live probe on 2026-09-20 first ran
// this constant at 3s (matching accountquota.keychainTimeout's own value for
// a DIFFERENT, never-prompted use) and the real `ai.meta.dev.credentials`
// item's `-w` read consistently timed out — `TestLiveProbe` logged
// "security did not answer (killed or timed out)". Raising it to 25s for one
// diagnostic re-run (never committed at that value) let the SAME read
// succeed in ~9.5s (12.72s total probe time minus the 2869ms HTTP fetch the
// same run measured, minus `muse --version`'s own sub-second cost).
//
// UNVERIFIED which of two causes that ~9.5s was: securityd/ACL negotiation
// latency for an already-trusted caller, or a Keychain authorization dialog
// that was answered (by whoever was at this keyboard) within the 25s window
// — this run cannot distinguish the two, and no screenshot or prompt log was
// captured either way. That distinction matters: if it was a one-time Allow
// rather than an already-trusted ACL, every UNATTENDED production poll after
// this one could re-prompt and simply time out again at 15s, silently
// degrading to "credential not resolvable" rather than ever completing. The
// pinned herdr-agent-quota source treats this as a first-class risk its own
// way: it refuses a background keychain read outright without a recorded
// `--keychain-approve` marker, so an unattended process NEVER triggers a
// prompt. This package implements no equivalent ceremony — 15s (roughly 50%
// headroom over the one successful measurement) is a stopgap the maintainer
// should treat as an open question, not a settled fix; see the PR body's
// risks section.
const keychainTimeout = 15 * time.Second

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
