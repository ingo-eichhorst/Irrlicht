// hook_disclosure.go holds the issue #1356 contract every hook-installing
// agent adapter must satisfy: the consent copy the user reads before granting
// the hooks permission names exactly the hook events the installer writes, and
// states how many of them there are.
//
// Like AssertPermissionGated and AssertHookEndpointFollowsBindAddr it exists
// because the obligation is one an adapter opts into rather than one the
// compiler can see. Claude Code's copy said "6 hook entries" and named six
// events for the whole of #1173's seven-event install: the Notification hook
// was written to the user's settings.json without ever being disclosed. That is
// a #570 consent violation, not a docs typo — the wizard's Touches/Detail text
// *is* what the user consents to — and nothing bound the prose to the list, so
// it drifted silently at N=2 adapters with one added event.
package contracttesting

import (
	"regexp"
	"strconv"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
	"irrlicht/core/domain/session"
)

// entryCountPattern extracts the entry count an adapter's consent copy states
// ("Writes 7 hook entries to ~/.claude/settings.json"). Both singular and
// plural are accepted so a one-event adapter is not forced into bad grammar.
var entryCountPattern = regexp.MustCompile(`(\d+) hook entr(?:y|ies)`)

// eventShapedToken matches a CamelCase identifier of two or more words —
// "SessionStart", "UserPromptSubmit", "PostToolUseFailure". Every agent CLI
// names its hook events this way, and an agent fires far more of them than
// irrlicht models, so session.AllHookEvents alone cannot see copy that promises
// a SessionStart hook nobody installs. Matching the *shape* rather than a
// second hand-kept roster of upstream names is deliberate: a roster is the kind
// of list #1356 is about.
//
// Single-word event names ("Stop", "Notification") are outside this shape and
// are covered by the session.AllHookEvents arm instead. The two overlap, and
// between them they cover every name either source knows.
var eventShapedToken = regexp.MustCompile(`\b[A-Z][a-z]+(?:[A-Z][a-z]+)+\b`)

// HookDisclosure wires one adapter's hooks permission into
// AssertHookDisclosureMatchesInstalled. Installed is the same slice the
// adapter hands to hookjson.Config.Events — pass the variable, never a
// hand-written copy of it, or the contract checks the declaration against
// another declaration instead of against the install.
//
// That instruction is not enforced here, which bounds what the count
// obligation proves: it is len(the slice the test passed), not a count read
// back off a written config file. For today's two adapters the gap is closed
// elsewhere — both pass the same installedHookEvents variable to
// AssertHookEndpointFollowsBindAddr, which does run a real install into a temp
// home and reads every event's entry back, and hookjson.EnsureInstalled writes
// exactly one matcher group per event. A third adapter that wires this contract
// with a literal slice and skips that one would get a weaker check than it
// looks like.
type HookDisclosure struct {
	// Agent is the adapter's registration, exactly as the daemon consumes it.
	Agent agent.Agent
	// PermissionKey is the hooks permission's key within that registration.
	PermissionKey string
	// Installed is the installer's event list.
	Installed []string
	// NonEventTerms are CamelCase terms the copy legitimately mentions that
	// are not hook events — a tool name in a matcher, a type name. Usually
	// empty. Every entry is a name the over-promise arm stops policing, so
	// keep the list short and say in a comment why each one is there.
	NonEventTerms []string
}

// AssertHookDisclosureMatchesInstalled runs the issue #1356 contract against d:
// the declared permission is modify-kind, its Touches/Detail text names every
// installed event, states the right number of entries, and names no hook event
// the adapter does not install.
//
// All three obligations read Touches and Detail together, because which of the
// two carries a given fact is a presentation choice — the wizard row shows
// Touches and the (i) expander shows Detail, and the user sees both before
// granting.
//
// The third obligation draws candidate names from two overlapping sources:
// session.AllHookEvents (the universe the domain models, kept complete by
// TestAllHookEvents_CoversEveryConstant) and every event-shaped CamelCase token
// in the copy itself, which reaches upstream events irrlicht has no constant
// for. Neither direction is the consent-critical one — an over-promise doesn't
// hide a write from the user — but a disclosure that names hooks the daemon
// never installs is still not the contract the user agreed to.
func AssertHookDisclosureMatchesInstalled(t *testing.T, d HookDisclosure) {
	t.Helper()

	perm, ok := findPermission(d.Agent, d.PermissionKey)
	if !ok {
		t.Fatalf("agent %q declares no permission with key %q", d.Agent.Identity.Name, d.PermissionKey)
	}
	if perm.Kind != permission.KindModify {
		t.Errorf("hooks permission %q is %v, want %v — installing hooks writes to the user's config",
			d.PermissionKey, perm.Kind, permission.KindModify)
	}
	if len(d.Installed) == 0 {
		t.Fatal("HookDisclosure.Installed is empty — pass the installer's event list")
	}
	disclosure := perm.Touches + "\n" + perm.Detail

	t.Run("names_every_installed_event", func(t *testing.T) {
		for _, event := range d.Installed {
			if !mentions(disclosure, event) {
				t.Errorf("consent copy never names the %q hook, but the installer writes it — "+
					"an undisclosed write to the user's config (#570)", event)
			}
		}
	})

	t.Run("states_the_installed_count", func(t *testing.T) {
		matches := entryCountPattern.FindAllStringSubmatch(disclosure, -1)
		if len(matches) == 0 {
			t.Fatalf("consent copy states no entry count; want %q", strconv.Itoa(len(d.Installed))+" hook entries")
		}
		for _, m := range matches {
			stated, err := strconv.Atoi(m[1])
			if err != nil {
				t.Fatalf("unparseable entry count %q: %v", m[1], err)
			}
			if stated != len(d.Installed) {
				t.Errorf("consent copy declares %d hook entries, installer writes %d",
					stated, len(d.Installed))
			}
		}
	})

	t.Run("names_no_uninstalled_event", func(t *testing.T) {
		installed := make(map[string]bool, len(d.Installed))
		for _, event := range d.Installed {
			installed[event] = true
		}
		allowed := make(map[string]bool, len(d.NonEventTerms))
		for _, term := range d.NonEventTerms {
			allowed[term] = true
		}
		// Two candidate sources, deliberately overlapping: the names the
		// domain models (which includes single-word ones), and every
		// event-shaped token actually present in the copy (which reaches
		// upstream events irrlicht has no constant for).
		for _, event := range session.AllHookEvents {
			if installed[event] || allowed[event] || !mentions(disclosure, event) {
				continue
			}
			t.Errorf("consent copy names the %q hook, which the installer does not write — "+
				"the disclosure promises more than it does", event)
		}
		for _, token := range eventShapedToken.FindAllString(disclosure, -1) {
			if installed[token] || allowed[token] {
				continue
			}
			t.Errorf("consent copy names %q, which reads as a hook event but the installer "+
				"does not write it — install it, reword the copy, or list it in NonEventTerms", token)
		}
	})
}

// findPermission returns the declared permission with the given key.
func findPermission(a agent.Agent, key string) (agent.Permission, bool) {
	for _, p := range a.Permissions {
		if p.Key == key {
			return p, true
		}
	}
	return agent.Permission{}, false
}

// mentions reports whether text names event as a whole word. Substring
// matching would not do: "PostToolUse" occurs inside "PostToolUseFailure", so
// copy that named only the latter would read as disclosing both.
func mentions(text, event string) bool {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(event) + `\b`).MatchString(text)
}
