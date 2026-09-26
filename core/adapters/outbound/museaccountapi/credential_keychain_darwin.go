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
// SETTLED by issue #2062 (observed by its reporter on 2026-09-26, with the
// dev daemon from main @ 927d80cf4; not re-measured by this change): that
// ~9.5s was an ANSWERED Keychain dialog, not securityd latency. `security`
// is not on the item's ACL, so every read raises the dialog ("security
// möchte deine vertraulichen Informationen verwenden …"), and "Allow"
// covers that one read only. A hand-run read with the dialog answered took
// 6s; an unanswered one is killed here after 15s — events.log recorded those
// timeouts at 16:03, 16:58 and 17:17. Because each read is a prompt, callers
// must not read on every poll: the daemon reads through
// services.GrantCredentialCache — once per grant, plus one re-read after Meta
// rejects the token (re-armed only by an accepted fetch) — and keeps a failed
// read (ErrKeychainRead) cached until the permission is revoked and granted
// again, so an unanswered dialog is not raised again on the poller's
// backoff. 15s stays the bound on one read;
// the pinned herdr-agent-quota source's `--keychain-approve` ceremony (it
// never reads in the background without a recorded approval) is still not
// implemented here.
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
