// verify.go is the read-only half of the install (issue #1372).
//
// EnsureInstalled answers "make it so" and, by design, tells you almost nothing
// afterwards: a bool saying whether it wrote. That was enough while the install
// was treated as a one-shot fact — grant once, write once, assume forever. It
// is not enough once the file can change underneath us.
//
// It can. gemini-cli's settings writer is sync-by-omission: applyKeyDiff
// deletes every key present on disk but absent from the in-memory snapshot the
// CLI took at startup, recursively, at every nesting level. Live-verified on
// #1355's Phase C audit — with gemini running, an externally added top-level
// key AND an externally added hooks.PreCompress entry were both silently
// deleted by a single /theme change, and one of the write paths that can do it
// (saveModelChange) is not user-initiated at all. Claude Code and Codex have
// the same exposure to a hand-edit or a future in-app settings surface; it has
// simply never been checked.
//
// So the question "are our entries still there" needs an answer that does not
// require writing to find out. Calling EnsureInstalled on a schedule would
// answer it, but only by performing the write — behind the user's back, on a
// file whose consent they may have withdrawn since. Verify separates the
// question from the answer: it reads, reports, and returns. The repair runs
// through PermissionService, which owns the consent gate.
package hookjson

import (
	"irrlicht/core/domain/agent"
)

// Verify reports which of cfg's events are not installed the way
// EnsureInstalled would install them right now. It never writes.
//
// The verdict is deliberately built from the SAME predicates EnsureInstalled
// repairs with — entryIsStale, matcherIsStale, HasOurHook — rather than from a
// second reading of what "installed" means. Two definitions would drift, and
// both directions of drift are live bugs: stricter than the repair is a loop
// that writes on every pass and never converges, laxer is a clobbered install
// that reads healthy in the diagnostics bundle. TestVerifyAgreesWithEnsureInstalled
// pins the equivalence over a corpus of awkward files.
//
// An unreadable or malformed file is an error, not damage and not health.
// Reporting damage would aim the repair at a file EnsureInstalled also refuses
// to touch (it errors rather than overwrite content the user meant to keep), so
// the loop would spin; reporting health would hide a genuinely broken config.
// A file that does not EXIST is neither — ReadSettings treats it as an empty
// document, so every event reads as missing, which is exactly right: that is
// what deleting the settings file looks like, and installing is the fix.
func Verify(cfg Config) (agent.HookEntryStatus, error) {
	settings, err := ReadSettings(cfg.Path)
	if err != nil {
		return agent.HookEntryStatus{}, err
	}

	hooksMap, _ := settings["hooks"].(map[string]interface{})
	if hooksMap == nil {
		// No hooks object at all — the shape a sync-by-omission clobber leaves
		// behind. Every event is missing; nothing can be stale.
		return agent.HookEntryStatus{Missing: append([]string(nil), cfg.Events...)}, nil
	}

	var status agent.HookEntryStatus
	for _, event := range cfg.Events {
		if !HasOurHook(hooksMap, event, cfg.Sentinel) {
			status.Missing = append(status.Missing, event)
			continue
		}
		// Present but possibly wrong. Missing and stale are exclusive per event
		// on purpose: an event with no entry of ours cannot also have one in the
		// wrong shape, and reporting it in both buckets would double every count
		// the diagnostics bundle publishes.
		if eventHasStaleEntry(hooksMap, event, cfg) || eventHasStaleMatcher(hooksMap, event, cfg) {
			status.Stale = append(status.Stale, event)
		}
	}
	return status, nil
}

// eventHasStaleEntry reports whether any sentinel-bearing inner hook of the
// event is no longer canonical — the read-only twin of upgradeStaleEntries.
func eventHasStaleEntry(hooksMap map[string]interface{}, event string, cfg Config) bool {
	arr, ok := eventGroups(hooksMap, event)
	if !ok {
		return false
	}
	for _, g := range arr {
		entries, ok := groupEntries(g)
		if !ok {
			continue
		}
		for _, h := range entries {
			hook, ok := h.(map[string]interface{})
			if !ok {
				continue
			}
			if entryIsStale(hook, cfg) {
				return true
			}
		}
	}
	return false
}

// eventHasStaleMatcher reports whether any sentinel-bearing group of the event
// carries a matcher other than the one cfg now expects — the read-only twin of
// upgradeStaleMatchers.
func eventHasStaleMatcher(hooksMap map[string]interface{}, event string, cfg Config) bool {
	arr, ok := eventGroups(hooksMap, event)
	if !ok {
		return false
	}
	expected := cfg.MatcherFor(event)
	for _, g := range arr {
		group, ok := g.(map[string]interface{})
		if !ok || !containsSentinel(g, cfg.Sentinel) {
			continue
		}
		if matcherIsStale(group, expected) {
			return true
		}
	}
	return false
}
