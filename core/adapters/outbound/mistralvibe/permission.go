package mistralvibe

import (
	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// Name is the pseudo-adapter this permission is declared on. It is distinct
// from the "mistral-vibe" coding-agent adapter, whose own observe permission
// covers its transcripts; revoking this one must not stop those. It is also
// distinct from any account-API permission: a browser grant is its own
// consent class (epic #1977 §4.3).
const Name = "mistral-vibe-browser-session"

// PermissionKey is this permission's key. It is not
// agent.AccountAPIPermissionKey: that key covers reading a credential file
// or Keychain item the provider's own CLI keeps. This one covers reading a
// browser's cookies, which is a different grant (#1977 §4.3, #2012 §1.3).
const PermissionKey = "browser-session"

// Permission text. Title, Touches and Detail name the browser, the profile,
// the cookies and the one destination (#2012 §1.3), and Detail states the
// account assertion the grant makes (#2012's binding decision). Each claim
// is backed by code: the profile rule by chromecookies.LastUsedProfile, the
// cookies by CookieSelector, the destination by Destination, the immediate
// stop by the daemon's Stop effect (AccountPoller.Revoke plus a key-cache
// reset), and the chip removal by the sweep's not-granted branch. The
// "Always Allow" warning is the review of #2012's finding 2: the Keychain is
// read by /usr/bin/security (core/pkg/keychainlookup), so the access an
// "Always Allow" grants belongs to that tool, not to irrlichd, and the item
// is the key to every Chrome cookie, not only Mistral's.
const (
	permissionTitle   = "Read Mistral Vibe usage from your Chrome session"
	permissionFeature = "Mistral Vibe plan usage in the quota chip on Mistral Vibe sessions"
	permissionTouches = "Google Chrome on macOS only. Reads the Chrome profile Chrome last used " +
		"(named in ~/Library/Application Support/Google/Chrome/Local State), its Cookies database, " +
		"and the \"Chrome Safe Storage\" item in your login Keychain. macOS may ask you to allow that read: " +
		"choose Allow, not Always Allow, because the read goes through macOS's security tool and Always Allow " +
		"would let any program that runs it read the key to all of Chrome's cookies without asking. " +
		"Only two kinds of cookie are decrypted: csrftoken and ory_session_*, for console.mistral.ai " +
		"and .mistral.ai. Those cookies, and an X-CSRFToken header repeating the csrftoken value, are " +
		"sent once per poll to GET https://console.mistral.ai/api-ui/trpc/billing.vibeUsage and nowhere else."
	permissionDetail = "Granting this confirms that the Mistral account signed in to this Chrome profile " +
		"is the account your vibe CLI uses. Mistral's answer names no account, so Irrlicht cannot check " +
		"that itself: the link is recorded as user-asserted, never as provider-issued, and every Mistral " +
		"Vibe session shows the one reading. Turning this off stops every further cookie and Keychain read " +
		"and every further request to console.mistral.ai at once, cancels a request in flight, and removes " +
		"the reading from your sessions within a minute."
)

// PermissionDeclaration returns the consent declaration: a daemon-wide entry
// with no Source/Process axes, KindObserve (it reads and sends, and writes no
// file), with Apply/Remove built by the daemon's wiring
// (core/cmd/irrlichd/mistralvibe_effects.go).
func PermissionDeclaration(apply, remove func() error) agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{Name: Name, DisplayName: "Mistral Vibe usage (Chrome session)"},
		Permissions: []agent.Permission{{
			Key:             PermissionKey,
			Kind:            permission.KindObserve,
			Title:           permissionTitle,
			FeatureUnlocked: permissionFeature,
			Touches:         permissionTouches,
			Detail:          permissionDetail,
			Apply:           apply,
			Remove:          remove,
		}},
	}
}
