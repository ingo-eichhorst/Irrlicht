// Package hookcov derives per-adapter hook coverage over the onboarding
// catalog: how many of each adapter's recordings carry at least one
// hook_received lifecycle event, against how many could — i.e. whether the
// adapter declares a "hooks" permission in the daemon's adapter registry.
//
// Two facts live in two different places and this package is the join:
//
//   - "declares hooks" is code, not catalog. Nothing under replaydata/ records
//     it; the only machine-readable source is agent.Agent.Permissions in the
//     daemon's adapter registry.
//   - "has a hook-bearing recording" is disk, reached through the catalog
//     loaders (shard.LoadAdapterCells → shard.AgentCellDir →
//     validate.RecordingDirs → replay.LoadEvents) rather than a bespoke walk
//     of replaydata/, so this report reads the same tree the rest of the
//     factory tooling reads.
//
// One caveat on that second bullet: a cell here is a metadata.json folder on
// disk, whereas `of status` counts a cell only once its scenario_id resolves
// to a catalog row. The two therefore agree only while the FK holds — which
// `of validate` gates in CI, and which does hold today (482 == 482). They are
// not equal by construction.
//
// Every number is derived at call time. Nothing is cached and nothing is
// hardcoded — a count committed to source would be stale the day it landed.
//
// # Attribution
//
// A hook_received event is credited to the adapter its OWN session belongs
// to, not to the cell directory the recording happens to live under (#1768).
// The two agree everywhere except a co-resident-agent scenario, where they
// can disagree: replaydata/agents/hermes/scenarios/4-2_multiple-agents-same-workspace
// is a hermes/claude-code coexistence cell whose one hook_received event is
// Claude Code's own Stop hook, not hermes's — hermes has none of its own in
// that recording.
//
// hook_name cannot make that distinction: every adapter's turn-done hook
// dispatches through the same handful of canonical wire names
// (session.HookStop = "Stop" chief among them; see
// core/application/services/session_detector_lifecycle.go), so an adapter's
// OWN hook_received event and a co-resident adapter's carry the identical
// hook_name. hook_received also never carries its own `adapter` field — every
// hook_received event actually present in the corpus leaves it empty. But
// every OTHER event kind for that same session_id (transcript_new,
// transcript_activity, ...) does carry `adapter`, in the SAME sidecar — so
// hasOwnHookEvent cross-references a hook_received event's session_id against
// its sibling events to find whose session it actually is. A session_id with
// no sibling carrying an adapter at all is unattributable and counts for
// nobody: never defaulted to the cell's own adapter, which would silently
// launder the exact bug this join exists to catch.
//
// # Scope
//
// Cells on disk under replaydata/agents/<adapter>/scenarios. The sibling
// regressions/ tree is not a catalog cell and no catalog loader enumerates it,
// so it is excluded and the CLI says so in its output rather than leaving a
// reader to discover the discrepancy by grep.
package hookcov

import (
	"path/filepath"
	"sort"

	"irrlicht/core/adapters/inbound/agents"
	"irrlicht/core/domain/lifecycle"
	"irrlicht/tools/onboarding-factory/internal/replay"
	"irrlicht/tools/onboarding-factory/internal/shard"
	"irrlicht/tools/onboarding-factory/internal/validate"
)

// permissionKeyHooks is the Permission.Key adapters use to declare that they
// install agent hooks. It mirrors claudecode.PermissionKeyHooks and
// codex.PermissionKeyHooks, which are independent consts of the same value;
// TestDeclaredMatchesRegistry pins the join so a rename on either side fails a
// test instead of silently reporting every adapter as declaring nothing.
const permissionKeyHooks = "hooks"

// Status is the per-adapter verdict. The distinction that matters is
// StatusGap vs StatusNone: both have zero hook-bearing recordings, but only
// one of them is a problem.
type Status string

const (
	// StatusOK — declares hooks and has at least one hook-bearing recording.
	StatusOK Status = "ok"
	// StatusGap — declares hooks and has NONE. The loud failure this report
	// exists to surface: the regression net does not exercise the channel.
	StatusGap Status = "gap"
	// StatusIncidental — declares no hooks, yet a recording carries hook
	// events. Real in this corpus: multi-agent scenarios where a co-resident
	// agent emitted them. Not a problem, but not "no hooks here" either.
	StatusIncidental Status = "incidental"
	// StatusNone — declares no hooks and has none. Nothing to see.
	StatusNone Status = "none"
)

// AdapterCoverage is one adapter's row.
type AdapterCoverage struct {
	Adapter       string `json:"adapter"`
	DeclaresHooks bool   `json:"declares_hooks"`
	Cells         int    `json:"cells"`
	Recordings    int    `json:"recordings"`
	WithHooks     int    `json:"recordings_with_hooks"`
	Status        Status `json:"status"`
}

// Totals is the corpus-wide rollup of the rows.
type Totals struct {
	Cells      int `json:"cells"`
	Recordings int `json:"recordings"`
	WithHooks  int `json:"recordings_with_hooks"`
}

// Report is the whole derived view, adapters sorted by name.
type Report struct {
	Adapters []AdapterCoverage `json:"adapters"`
	Totals   Totals            `json:"totals"`
}

// Declaring counts the adapters that declare a hooks permission — the
// denominator a gap count is meaningful against.
func (r Report) Declaring() int {
	n := 0
	for _, a := range r.Adapters {
		if a.DeclaresHooks {
			n++
		}
	}
	return n
}

// Gaps names every adapter that declares hooks but has no hook-bearing
// recording, in report order. Nil when there are none.
func (r Report) Gaps() []string {
	var out []string
	for _, a := range r.Adapters {
		if a.Status == StatusGap {
			out = append(out, a.Adapter)
		}
	}
	return out
}

// Declared reports, per on-disk adapter slug, whether that adapter declares a
// hooks permission in the daemon's adapter registry. Every registry adapter
// gets an entry, so a false is "asked and answered" rather than "absent".
func Declared() map[string]bool {
	out := make(map[string]bool)
	for _, a := range agents.All() {
		slug := slug(a.Identity.Name)
		declares := false
		for _, p := range a.Permissions {
			if p.Key == permissionKeyHooks {
				declares = true
				break
			}
		}
		out[slug] = declares
	}
	return out
}

// slug is shard.SlugForAdapter, which owns the registry-name → on-disk-slug
// map for every caller that indexes the catalog by adapter.
//
// Note it is NOT viewer.normalizeAdapter, which additionally maps "" to
// claudecode so pre-#319 recordings with no adapter field still render a
// branded icon. That fallback is right on the recording path and wrong here —
// it would invent an adapter for an unattributed name — so the viewer keeps it
// as a local wrapper around the shared map rather than the two forking.
func slug(identityName string) string { return shard.SlugForAdapter(identityName) }

// Coverage builds the report. catalogAdapters is the onboarded column set
// (shard.Agents); declares is the registry join (Declared). Both are
// parameters rather than read inside so tests can pin the arithmetic without
// depending on the compiled-in registry.
//
// The adapter set is the union of the catalog columns and any hooks-declaring
// registry adapter missing from them — an adapter that declares hooks and has
// no catalog presence at all is the loudest gap there is, and dropping it
// because it has no cells would hide exactly the case worth shouting about.
func Coverage(repoRoot string, catalogAdapters []string, declares map[string]bool) Report {
	// Resolve to an absolute path up front. validate.RecordingDirs and
	// replay.LoadEvents both refuse any path containing "..", so a relative
	// --repo-root like "../.." would otherwise report zero recordings and
	// zero hook-bearing recordings for every adapter — which in THIS report
	// renders as every hooks-declaring adapter being a GAP. A silent
	// all-clear is bad; a silent all-alarm is worse. TestCoverageRelativeRepoRoot
	// is what keeps this from being deleted as dead code.
	if abs, err := filepath.Abs(repoRoot); err == nil {
		repoRoot = abs
	}

	rep := Report{}
	for _, adapter := range adapterSet(catalogAdapters, declares) {
		row := coverageFor(repoRoot, adapter, declares[adapter])
		rep.Adapters = append(rep.Adapters, row)
		rep.Totals.Cells += row.Cells
		rep.Totals.Recordings += row.Recordings
		rep.Totals.WithHooks += row.WithHooks
	}
	return rep
}

// coverageFor walks one adapter's cells and their recordings. repoRoot must
// already be absolute — Coverage resolves it once for every adapter.
func coverageFor(repoRoot, adapter string, declaresHooks bool) AdapterCoverage {
	row := AdapterCoverage{Adapter: adapter, DeclaresHooks: declaresHooks}
	for _, cell := range shard.LoadAdapterCells(repoRoot, adapter) {
		if cell == nil {
			continue // LoadAdapterCells never stores one; cheap panic guard
		}
		row.Cells++
		cellDir := shard.AgentCellDir(repoRoot, adapter, cell.Folder)
		withHooks, total := hookBearingRecordings(cellDir, adapter)
		row.Recordings += total
		row.WithHooks += withHooks
	}
	row.Status = statusOf(row.DeclaresHooks, row.WithHooks)
	return row
}

// hookBearingRecordings counts one cell's recordings and how many of them
// carry at least one hook_received event attributable to adapter.
func hookBearingRecordings(cellDir, adapter string) (withHooks, total int) {
	for _, recDir := range validate.RecordingDirs(cellDir) {
		total++
		if hasOwnHookEvent(filepath.Join(recDir, "events.jsonl"), adapter) {
			withHooks++
		}
	}
	return withHooks, total
}

// adapterSet is the sorted union described on Coverage.
func adapterSet(catalogAdapters []string, declares map[string]bool) []string {
	seen := make(map[string]bool, len(catalogAdapters))
	for _, a := range catalogAdapters {
		seen[a] = true
	}
	for a, d := range declares {
		if d {
			seen[a] = true
		}
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// statusOf is the whole point of the report in one function: zero
// hook-bearing recordings means opposite things depending on whether the
// adapter claims to install hooks.
func statusOf(declares bool, withHooks int) Status {
	switch {
	case declares && withHooks > 0:
		return StatusOK
	case declares:
		return StatusGap
	case withHooks > 0:
		return StatusIncidental
	default:
		return StatusNone
	}
}

// hasOwnHookEvent reports whether a recording's events sidecar carries at
// least one hook_received event attributable to adapterSlug — see the
// package's Attribution section for why hook_name cannot answer this and
// session_id cross-referencing does. It goes through replay.LoadEvents — the
// sanctioned reader — rather than scanning bytes, so it cannot disagree with
// replay about line-length caps or malformed-line handling. A missing or
// unreadable sidecar counts as "no hooks", the same as an empty one.
func hasOwnHookEvent(eventsPath, adapterSlug string) bool {
	events, err := replay.LoadEvents(eventsPath)
	if err != nil {
		return false
	}
	return anyHookEventOwnedBy(events, sessionAdapters(events), adapterSlug)
}

// HasOwnHookEvent is hasOwnHookEvent, exported for a caller asking the same
// question about ONE staged candidate recording rather than the whole
// committed corpus hookBearingRecordings walks. `of hookcheck` is that caller
// (#1754): promote-recording.sh's promote-time prompt needs exactly this
// attribution logic — not a cruder "does events.jsonl contain the string
// hook_received anywhere" check, which would misfire on a co-resident
// adapter's hook events the same way hookBearingRecordings would without it.
func HasOwnHookEvent(eventsPath, adapterSlug string) bool {
	return hasOwnHookEvent(eventsPath, adapterSlug)
}

// sessionAdapters maps session_id -> the on-disk slug of the adapter a
// SIBLING event (never hook_received itself, which carries no Adapter)
// tagged that session with. One pass over the whole recording, built once
// regardless of how many hook_received events it holds.
func sessionAdapters(events []lifecycle.Event) map[string]string {
	owners := make(map[string]string, len(events))
	for _, e := range events {
		if e.Adapter != "" {
			owners[e.SessionID] = slug(e.Adapter)
		}
	}
	return owners
}

// anyHookEventOwnedBy reports whether any hook_received event in events
// belongs to adapterSlug, per owners (session_id -> adapter slug, from
// sessionAdapters). Comma-ok on the lookup, not a plain map-read: an
// unattributed session_id must read as "no match" via absence, never
// coincide with adapterSlug="" (which never occurs here — callers always
// pass a real on-disk slug — but the explicit check keeps that an invariant
// rather than a map zero-value accident).
func anyHookEventOwnedBy(events []lifecycle.Event, owners map[string]string, adapterSlug string) bool {
	for _, e := range events {
		if e.Kind != lifecycle.KindHookReceived {
			continue
		}
		if owner, ok := owners[e.SessionID]; ok && owner == adapterSlug {
			return true
		}
	}
	return false
}
