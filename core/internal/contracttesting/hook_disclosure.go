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
	"sort"
	"strconv"
	"strings"
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
//
// Anchored because it is applied to whole words, not scanned across prose.
var eventShapedToken = regexp.MustCompile(`^[A-Z][a-z]+(?:[A-Z][a-z]+)+$`)

// wordPattern splits consent copy into identifier-shaped words. Deliberately
// includes digits and underscores so "cli_version" is one word rather than two,
// which keeps a fragment from being mistaken for an event name.
var wordPattern = regexp.MustCompile(`[A-Za-z][A-Za-z0-9_]*`)

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
	// One tokenization serves both the "is this name present" and the "what
	// event-shaped names are present" questions. Whole-word matching is what
	// makes the first safe: "PostToolUse" occurs inside "PostToolUseFailure",
	// so a substring test on copy naming only the latter would read as
	// disclosing both.
	named := wordsIn(disclosure)

	t.Run("names_every_installed_event", func(t *testing.T) {
		assertNamesEveryInstalledEvent(t, d.Installed, named)
	})
	t.Run("states_the_installed_count", func(t *testing.T) {
		assertStatesInstalledCount(t, d.Installed, disclosure)
	})
	t.Run("names_no_uninstalled_event", func(t *testing.T) {
		assertNamesNoUninstalledEvent(t, d, named)
	})
	t.Run("states_the_version_floor", func(t *testing.T) {
		assertStatesVersionFloor(t, perm, disclosure)
	})
}

// assertStatesVersionFloor extends the #1356 contract along the axis #1365
// added. The other three arms bind the copy to WHICH entries get written; a
// declared minimum upstream version makes it conditional whether ANY of them
// do, and copy that describes an install without naming its precondition
// describes something that may not happen on the user's machine.
//
// This arm reads the floor off the declaration rather than taking it as a
// field, so every adapter that declares one is covered without changing its
// call site, and an adapter that declares none is unaffected.
//
// Note what does NOT need saying here: the disclosed event set is the same at
// every version the adapter installs at. The gate is whole-install — below the
// floor nothing is written, at or above it everything is — precisely so that
// Touches/Detail can keep stating one exact count and one exact list. A gate
// that filtered events per version would make the count a range, which is the
// hand-waving #1356 abolished.
func assertStatesVersionFloor(t *testing.T, perm agent.Permission, disclosure string) {
	t.Helper()
	if perm.Hooks == nil || perm.Hooks.Version == nil || perm.Hooks.Version.Min == "" {
		return
	}
	if !strings.Contains(disclosure, perm.Hooks.Version.Min) {
		t.Errorf("consent copy never states the %s minimum CLI version the install is gated "+
			"on — the user reads this text and grants, then nothing is written and the copy "+
			"gave no hint why (#1365); render it with hookjson.RequiresVersion so it cannot "+
			"drift from the gate", perm.Hooks.Version.Min)
	}
}

// assertNamesEveryInstalledEvent is the consent-critical arm: an installed
// event the copy never names is a write the user was not told about.
func assertNamesEveryInstalledEvent(t *testing.T, installed []string, named map[string]bool) {
	t.Helper()
	for _, event := range installed {
		if !named[event] {
			t.Errorf("consent copy never names the %q hook, but the installer writes it — "+
				"an undisclosed write to the user's config (#570)", event)
		}
	}
}

// assertStatesInstalledCount checks every count the copy states, not just the
// first: a permission that mentions its entry count twice must not have them
// disagree.
func assertStatesInstalledCount(t *testing.T, installed []string, disclosure string) {
	t.Helper()
	matches := entryCountPattern.FindAllStringSubmatch(disclosure, -1)
	if len(matches) == 0 {
		t.Fatalf("consent copy states no entry count; want %q",
			strconv.Itoa(len(installed))+" hook entries")
	}
	for _, m := range matches {
		stated, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("unparseable entry count %q: %v", m[1], err)
		}
		if stated != len(installed) {
			t.Errorf("consent copy declares %d hook entries, installer writes %d",
				stated, len(installed))
		}
	}
}

// assertNamesNoUninstalledEvent is the over-promise arm. Its two candidate
// sources overlap (every multi-word modelled name is in both), so they are
// merged before reporting: one wrong name should produce one failure line, not
// two. The contract's whole value is the message someone reads at 2am.
func assertNamesNoUninstalledEvent(t *testing.T, d HookDisclosure, named map[string]bool) {
	t.Helper()
	exempt := make(map[string]bool, len(d.Installed)+len(d.NonEventTerms))
	for _, event := range d.Installed {
		exempt[event] = true
	}
	for _, term := range d.NonEventTerms {
		exempt[term] = true
	}
	for _, name := range append(uninstalledNamesIn(named), eventShapedNamesIn(named)...) {
		if exempt[name] {
			continue
		}
		exempt[name] = true // reported once
		t.Errorf("consent copy names %q, which reads as a hook event but the installer "+
			"does not write it — the disclosure promises more than it does; install it, "+
			"reword the copy, or list it in NonEventTerms", name)
	}
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

// wordsIn returns the set of whole words in text. Hook event names are single
// identifiers, so set membership is exactly whole-word matching — and one scan
// serves every arm instead of a regexp compiled per candidate name.
func wordsIn(text string) map[string]bool {
	words := map[string]bool{}
	for _, w := range wordPattern.FindAllString(text, -1) {
		words[w] = true
	}
	return words
}

// uninstalledNamesIn returns the modelled hook event names present in words, in
// the domain's declaration order so failures are deterministic.
func uninstalledNamesIn(words map[string]bool) []string {
	var found []string
	for _, event := range session.AllHookEvents {
		if words[event] {
			found = append(found, event)
		}
	}
	return found
}

// eventShapedNamesIn returns the words that read as hook event names without
// the domain necessarily modelling them, sorted so failures are deterministic.
func eventShapedNamesIn(words map[string]bool) []string {
	var found []string
	for word := range words {
		if eventShapedToken.MatchString(word) {
			found = append(found, word)
		}
	}
	sort.Strings(found)
	return found
}
