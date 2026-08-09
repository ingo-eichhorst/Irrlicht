// Package hookjson implements the JSON hook-config machinery shared by agent
// adapters that install the daemon's hooks into a JSON settings file and
// receive the resulting callbacks over the daemon's HTTP endpoint —
// ~/.claude/settings.json for Claude Code (issues #108, #1161),
// ~/.codex/hooks.json for Codex (issue #1171).
//
// Both files carry the same shape: a top-level "hooks" object mapping an event
// name to an array of matcher groups, each holding an array of inner hook
// entries. So the merge, idempotency, in-place upgrade and removal logic is
// identical, and only four things genuinely differ per adapter — the settings
// path, the sentinel marking our entries, the event list plus per-event
// matcher, and the shape of the inner entry itself (Claude Code uses native
// `type: http` delivery, Codex a `type: command` curl). Config parameterizes
// exactly those; nothing is force-merged.
//
// It sits beside the adapters rather than inside one of them for the same
// reason agentpaths does: the rule is identical in every case, so it lives here
// once instead of being reimplemented per adapter — and forgotten, or silently
// drifted from, by the next one (issue #1179).
package hookjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

// Config describes one adapter's hook install to the machinery below. Every
// field is required; EnsureInstalled and Uninstall call the funcs directly.
type Config struct {
	// Path is the JSON settings file to merge into. The adapter resolves it
	// (and reports any home-directory error) before building the Config.
	Path string

	// Sentinel is the substring that identifies irrlicht-managed entries in an
	// inner hook object's `command` or `url` field. It must be a substring of
	// every shape we have ever installed for this adapter, so a legacy install
	// is still recognized as ours and upgraded in place rather than duplicated.
	Sentinel string

	// Events is the ordered list of hook event names to install handlers for.
	Events []string

	// MatcherFor returns the matcher to install for an event. An empty string
	// means the group carries no `matcher` key at all — the shape a Stop hook
	// requires in both Claude Code and Codex.
	MatcherFor func(event string) string

	// Entry builds the canonical inner hook object to install. Called fresh per
	// entry so no two groups share a map.
	Entry func() map[string]interface{}

	// IsCanonical reports whether an existing inner hook object is already in
	// the current form. A sentinel-bearing entry that is not canonical is
	// rewritten in place by Entry(), which is how a delivery-shape change
	// (Claude Code's curl→http migration, a Codex curl-flag change) reaches an
	// existing install without appending a duplicate group.
	IsCanonical func(hook map[string]interface{}) bool

	// WriteFile persists the re-serialized settings. It is supplied by the
	// adapter rather than fixed here because the two adapters' atomic writers
	// differ deliberately: claudecode's is shared with its CLAUDE.md writer, so
	// hardening it here would silently split that pair.
	WriteFile func(path string, data []byte) error
}

// EnsureInstalled merges cfg's hook entries into the settings file at cfg.Path,
// creating the file if it does not exist. Returns true if the file was
// modified, false if the entries were already present and current.
//
// Three reconciliations run per event, in order: any sentinel-bearing entry
// that is not canonical is rewritten in place; the matcher of any
// sentinel-bearing group is brought in line with the expected value; and a
// fresh group is appended only if no sentinel-bearing group exists at all. The
// first two migrate an existing install without ever appending a duplicate.
func EnsureInstalled(cfg Config) (bool, error) {
	settings, err := ReadSettings(cfg.Path)
	if err != nil {
		return false, err
	}

	hooksMap := ensureHooksMap(settings)

	modified := false
	for _, event := range cfg.Events {
		if upgradeStaleEntries(hooksMap, event, cfg) {
			modified = true
		}
		if upgradeStaleMatchers(hooksMap, event, cfg) {
			modified = true
		}
		if !HasOurHook(hooksMap, event, cfg.Sentinel) {
			addOurHook(hooksMap, event, cfg.MatcherFor(event), cfg.Entry())
			modified = true
		}
	}

	if !modified {
		return false, nil
	}

	return true, WriteSettings(cfg.Path, settings, cfg.WriteFile)
}

// Uninstall removes cfg's hook entries from the settings file at cfg.Path,
// leaving every user-authored group untouched. Returns true if the file was
// modified, false if none of our entries were found.
func Uninstall(cfg Config) (bool, error) {
	settings, err := ReadSettings(cfg.Path)
	if err != nil {
		return false, err
	}

	hooksMap, ok := settings["hooks"].(map[string]interface{})
	if !ok {
		return false, nil
	}

	modified := false
	for _, event := range cfg.Events {
		if removeOurHook(hooksMap, event, cfg) {
			modified = true
		}
	}

	if !modified {
		return false, nil
	}

	// Our removals emptied the "hooks" object, so drop it: on a config that had
	// no hooks before we installed, leaving `"hooks": {}` behind means revoking
	// consent does not give the user their file back (issue #1371). Gated on
	// having actually removed something, so a user's own pre-existing empty
	// object — which never reaches here, because modified stays false — is not
	// touched. A user hook in any event keeps the object non-empty and it stays.
	if len(hooksMap) == 0 {
		delete(settings, "hooks")
	}

	return true, WriteSettings(cfg.Path, settings, cfg.WriteFile)
}

// ReadSettings decodes a hook-settings JSON file into a generic map. A missing
// or empty file is not an error — there is simply nothing to merge onto yet, so
// both yield an empty map and the caller goes on to create the file. Malformed
// JSON does error: overwriting it would destroy content the user meant to keep.
// Exported because claudecode's statusline installer merges into the same
// settings.json and must read it exactly the same way.
//
// Comments are tolerated (issue #1371), for every caller rather than per
// adapter. The file this is for is gemini-cli's ~/.gemini/settings.json, which
// is JSONC by design — no caller reads it yet, and this is the prerequisite for
// the one that will (#1355 Phase D). Tolerating them everywhere rather than
// behind a Config flag is deliberate: the two files read today are strict JSON,
// so a comment in one means the user's own agent cannot load it either, and
// erroring here neither tells them that nor fixes it. Comments are blanked to
// spaces rather than stripped, so any error the decoder does report still
// points at the byte the user has to go fix.
//
// Not established: whether Claude Code and Codex themselves accept comments in
// those files. If they do not, an install into a commented one now reports
// success where it used to report a parse error — for a config their agent was
// already failing to load.
//
// What has NOT changed is the treatment of genuinely broken JSON: it still
// errors, and the file is still never overwritten. Issue #1362 is making that
// error visible rather than swallowed by the callers; it is the correct
// behavior, and TestReadSettings_MalformedJSONErrors plus
// TestMalformedInput_IsNeverOverwritten pin it.
//
// A file holding a bare `null` decodes to a nil map with no error, which every
// later write would panic on ("assignment to entry in nil map"), so it is
// normalized to an empty map here — the one place that can catch it for all
// callers.
func ReadSettings(path string) (map[string]interface{}, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]interface{}{}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]interface{}{}, nil
	}
	var settings map[string]interface{}
	if err := json.Unmarshal(blankComments(data), &settings); err != nil {
		return nil, err
	}
	if settings == nil {
		return map[string]interface{}{}, nil
	}
	return settings, nil
}

// WriteSettings persists settings to path and hands the bytes to writeFile.
// Exported alongside ReadSettings for the same statusline caller.
//
// It re-reads the file it is about to replace so it can change as little of it
// as possible: the wanted document is compared against the one on disk and only
// the byte ranges that actually differ are rewritten (issue #1371). That is what
// keeps a hand-maintained config intact — comments, key order, blank lines and
// an unescaped `&&` all survive because those bytes are copied, not
// re-serialized. See jsonc.go for how.
//
// Only when there is nothing to preserve — no file, or an empty one — is the
// document encoded from scratch, 2-space indented with a trailing newline,
// which is the shape a hand-edited config expects.
//
// The re-read cannot resurrect content the caller already dropped: callers
// obtain settings from ReadSettings on the same path moments earlier, so the
// bytes read here are the bytes that decoded into what they mutated.
func WriteSettings(path string, settings map[string]interface{}, writeFile func(path string, data []byte) error) error {
	original, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	data, err := renderSettings(original, settings)
	if err != nil {
		return err
	}
	return writeFile(path, data)
}

// renderSettings produces the bytes to write: a minimal splice of the original
// when there is one, a fresh encode when there is not.
func renderSettings(original []byte, settings map[string]interface{}) ([]byte, error) {
	if len(bytes.TrimSpace(original)) == 0 {
		return encodeDocument(settings)
	}
	data, err := spliceSettings(original, settings)
	if errors.Is(err, errNoSafeSplice) {
		return encodeDocument(settings)
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

// HasOurHook reports whether any matcher group carrying sentinel already exists
// in the event's hook array. Matcher-agnostic: a group with our sentinel is
// "ours" regardless of which matcher string it happens to carry.
func HasOurHook(hooksMap map[string]interface{}, event, sentinel string) bool {
	arr, ok := eventGroups(hooksMap, event)
	if !ok {
		return false
	}
	for _, g := range arr {
		if containsSentinel(g, sentinel) {
			return true
		}
	}
	return false
}

// --- helpers ---

// ensureHooksMap returns (or creates) the top-level "hooks" map in settings.
func ensureHooksMap(settings map[string]interface{}) map[string]interface{} {
	if h, ok := settings["hooks"]; ok {
		if m, ok := h.(map[string]interface{}); ok {
			return m
		}
	}
	m := map[string]interface{}{}
	settings["hooks"] = m
	return m
}

// upgradeStaleEntries rewrites every sentinel-bearing inner hook of the event
// that is not cfg.IsCanonical to a fresh cfg.Entry(). Returns true if any entry
// was rewritten.
func upgradeStaleEntries(hooksMap map[string]interface{}, event string, cfg Config) bool {
	arr, ok := eventGroups(hooksMap, event)
	if !ok {
		return false
	}
	upgraded := false
	for _, g := range arr {
		if upgradeGroupEntries(g, cfg) {
			upgraded = true
		}
	}
	return upgraded
}

// upgradeGroupEntries rewrites, within one matcher group, every inner hook that
// carries our sentinel but isn't canonical. Returns true if any was rewritten.
func upgradeGroupEntries(g interface{}, cfg Config) bool {
	entries, ok := groupEntries(g)
	if !ok {
		return false
	}
	upgraded := false
	for i, h := range entries {
		hook, ok := h.(map[string]interface{})
		if !ok {
			continue
		}
		if entryIsSentinel(hook, cfg.Sentinel) && !cfg.IsCanonical(hook) {
			entries[i] = cfg.Entry()
			upgraded = true
		}
	}
	return upgraded
}

// upgradeStaleMatchers reconciles the matcher of every sentinel-bearing group of
// the event with the one cfg now expects. Used to migrate existing installs
// when an event's matcher is widened or changed; for an expected of "" it
// strips the key entirely, since a Stop hook must carry no matcher.
func upgradeStaleMatchers(hooksMap map[string]interface{}, event string, cfg Config) bool {
	arr, ok := eventGroups(hooksMap, event)
	if !ok {
		return false
	}
	expected := cfg.MatcherFor(event)
	upgraded := false
	for _, g := range arr {
		group, ok := g.(map[string]interface{})
		if !ok || !containsSentinel(g, cfg.Sentinel) {
			continue
		}
		if reconcileGroupMatcher(group, expected) {
			upgraded = true
		}
	}
	return upgraded
}

// reconcileGroupMatcher brings one sentinel-bearing group's matcher into line
// with expected: sets it when it differs, or deletes the key when expected is
// empty. Returns true if it changed the group.
//
// The delete branch keys off PRESENCE, not "is a string". A hand-edited file
// can hold `"matcher": null` or `"matcher": 5`, and a type assertion would miss
// those and leave a turn-end group carrying a matcher key that both upstreams
// reject — permanently, since EnsureInstalled would then report no change and
// never revisit it.
func reconcileGroupMatcher(group map[string]interface{}, expected string) bool {
	if expected == "" {
		if _, present := group["matcher"]; present {
			delete(group, "matcher")
			return true
		}
		return false
	}
	if m, _ := group["matcher"].(string); m != expected {
		group["matcher"] = expected
		return true
	}
	return false
}

// addOurHook appends a matcher group holding entry to the event's array. The
// matcher key is omitted entirely for an empty matcher.
func addOurHook(hooksMap map[string]interface{}, event, matcher string, entry map[string]interface{}) {
	group := map[string]interface{}{
		"hooks": []interface{}{entry},
	}
	if matcher != "" {
		group["matcher"] = matcher
	}

	if existing, ok := hooksMap[event].([]interface{}); ok {
		hooksMap[event] = append(existing, group)
		return
	}
	hooksMap[event] = []interface{}{group}
}

// removeOurHook drops every sentinel-bearing matcher group from the event's
// array, deleting the event key outright once nothing is left. Returns true if
// any group was removed.
func removeOurHook(hooksMap map[string]interface{}, event string, cfg Config) bool {
	arr, ok := eventGroups(hooksMap, event)
	if !ok {
		return false
	}

	var kept []interface{}
	removed := false
	for _, g := range arr {
		if containsSentinel(g, cfg.Sentinel) {
			removed = true
			continue
		}
		kept = append(kept, g)
	}
	if !removed {
		return false
	}

	if len(kept) == 0 {
		delete(hooksMap, event)
	} else {
		hooksMap[event] = kept
	}
	return true
}

// containsSentinel reports whether a matcher group holds an inner hook entry
// carrying our sentinel.
func containsSentinel(g interface{}, sentinel string) bool {
	entries, ok := groupEntries(g)
	if !ok {
		return false
	}
	for _, h := range entries {
		hook, ok := h.(map[string]interface{})
		if !ok {
			continue
		}
		if entryIsSentinel(hook, sentinel) {
			return true
		}
	}
	return false
}

// eventGroups returns an event's matcher-group array, or ok=false when the
// event is absent or holds something that isn't an array.
//
// It and groupEntries are the two places that tolerate a settings file holding
// a shape we don't expect. These are hand-editable configs, so every traversal
// goes through one of them rather than asserting inline and repeating the
// judgement about what a malformed value means.
func eventGroups(hooksMap map[string]interface{}, event string) ([]interface{}, bool) {
	arr, ok := hooksMap[event].([]interface{})
	return arr, ok
}

// groupEntries returns a matcher group's inner hook array, or ok=false when the
// value isn't a group in the expected shape.
func groupEntries(g interface{}) ([]interface{}, bool) {
	group, ok := g.(map[string]interface{})
	if !ok {
		return nil, false
	}
	entries, ok := group["hooks"].([]interface{})
	if !ok {
		return nil, false
	}
	return entries, true
}

// entryIsSentinel reports whether an inner hook object is one of ours, by our
// sentinel appearing in either delivery field: `command` (a shell hook, which
// is Codex's only form and Claude Code's legacy curl form) or `url` (Claude
// Code's native http delivery). Checking both is what lets one predicate serve
// an adapter that has migrated between the two.
func entryIsSentinel(hook map[string]interface{}, sentinel string) bool {
	for _, field := range []string{"command", "url"} {
		if v, ok := hook[field].(string); ok && strings.Contains(v, sentinel) {
			return true
		}
	}
	return false
}
