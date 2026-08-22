package hookcov

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"irrlicht/core/adapters/inbound/agents"
)

// repoRoot resolves the repository root from this test file's own path, the
// same pattern replay.repoRoot and righome.repoRoot use — hookcov sits at the
// same depth (tools/onboarding-factory/internal/<pkg>), so it is also four
// directories up.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")
}

// TestDeclaredMatchesRegistry pins the code→catalog join. This is the part of
// the report that can rot without anything else noticing: rename the
// permission key, or change an adapter's Identity.Name without updating Slug,
// and every adapter silently reports "declares no hooks" — which turns every
// real gap into a benign-looking StatusNone.
func TestDeclaredMatchesRegistry(t *testing.T) {
	got := Declared()

	// Exhaustive as of this commit: every registry adapter EXCEPT aider
	// declares a hooks permission. If aider gains one, this fails and the map
	// is updated — that is the intended workflow, not an obstacle.
	//
	// copilot joined in #1378, gemini-cli in #1717, kiro-cli in #1716,
	// mistral-vibe in #1718, pi in #1721, opencode in #1719, hermes in #1722
	// and antigravity in #1723 — all of them are expected to report
	// StatusGap for a while: each declares hooks, but no recording carries a hook_received
	// event yet, because landing the permission deliberately re-recorded
	// nothing (the frozen sidecars are hook-free and the replay goldens had
	// to stay byte-identical — #1717's own audit measured 0 cells
	// re-recorded, and #1716's/#1718's Phase 2 is plan-only for the same
	// reason; #1721 re-recorded nothing either, and its own audit reports the
	// rig gaps that would have to close first; #1722 re-recorded nothing
	// either, and hermes' rig row is opt-in because a bare HERMES_HOME cannot
	// authenticate; #1723 re-recorded nothing either, and antigravity has no
	// rig row at all — righome.Unisolatable records the two measurements that
	// make one impossible: no home override, and a login-keychain credential
	// keyed off $HOME). A GAP here is the honest reading of that, not a defect.
	want := map[string]bool{
		"aider": false, "antigravity": true, "claudecode": true, "codex": true,
		"copilot": true, "gemini-cli": true, "hermes": true, "kiro-cli": true,
		"mistral-vibe": true, "opencode": true, "pi": true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Declared() = %v\nwant %v", got, want)
	}

	// Every registry adapter must be represented, so a false is an answer
	// rather than a missing key.
	if len(got) != len(agents.All()) {
		t.Errorf("Declared() has %d entries for %d registry adapters", len(got), len(agents.All()))
	}
}

// TestSlugMapsOnlyClaudeCode locks the shared shard.SlugForAdapter mapping as
// hookcov consumes it. Over-normalising is the
// hazard: the viewer's near-identical helper also maps "" to claudecode, and
// copying that here would invent an adapter for an unattributed name.
func TestSlugMapsOnlyClaudeCode(t *testing.T) {
	cases := map[string]string{
		"claude-code":  "claudecode",
		"claudecode":   "claudecode",
		"gemini-cli":   "gemini-cli",
		"kiro-cli":     "kiro-cli",
		"mistral-vibe": "mistral-vibe",
		"":             "",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestStatusOf is the report's central distinction, tabulated. Rows 2 and 4
// are the pair the whole command exists for: identical zero, opposite meaning.
func TestStatusOf(t *testing.T) {
	cases := []struct {
		declares  bool
		withHooks int
		want      Status
	}{
		{true, 3, StatusOK},
		{true, 0, StatusGap},
		{false, 2, StatusIncidental},
		{false, 0, StatusNone},
	}
	for _, c := range cases {
		if got := statusOf(c.declares, c.withHooks); got != c.want {
			t.Errorf("statusOf(declares=%v, withHooks=%d) = %q, want %q", c.declares, c.withHooks, got, c.want)
		}
	}
}

// TestAdapterSetIncludesUnrepresentedDeclarer covers the union rule: an
// adapter that declares hooks but has no catalog column still gets a row, so
// "declares hooks, has nothing at all" cannot vanish from the report.
func TestAdapterSetIncludesUnrepresentedDeclarer(t *testing.T) {
	got := adapterSet(
		[]string{"opencode", "aider"},
		map[string]bool{"codex": true, "claudecode": true, "pi": false},
	)
	want := []string{"aider", "claudecode", "codex", "opencode"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("adapterSet = %v, want %v (sorted union; pi declares nothing so it stays out)", got, want)
	}
}

// TestCoverageRelativeRepoRoot is what keeps the filepath.Abs call in
// Coverage alive. validate.RecordingDirs and replay.LoadEvents both refuse any
// path containing "..", so without the Abs a repo root reached via ".." counts
// zero recordings and zero hook events for every adapter — rendering every
// hooks-declaring adapter as a GAP. That is the loudest possible wrong answer
// from this report, and before this test it was defended by three lines no
// test exercised: deleting them left the whole suite green.
// A RELATIVE root is required, and it must be spelled ".." rather than
// filepath.Join(root, "x", ".."): Join runs Clean, which cancels an interior
// "x/.." and leaves an absolute path with no ".." in it at all — a version of
// this test written that way passes with the Abs guard deleted, proving
// nothing. Only a LEADING ".." survives Clean, which is why this chdirs.
func TestCoverageRelativeRepoRoot(t *testing.T) {
	root := t.TempDir()
	writeHookFixture(t, root)
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	declares := map[string]bool{"claudecode": true}
	direct := Coverage(root, []string{"claudecode"}, declares)
	if len(direct.Adapters) != 1 || direct.Adapters[0].WithHooks != 1 {
		t.Fatalf("fixture precondition: direct read should find 1 hook-bearing recording, got %+v", direct.Adapters)
	}

	// From root/sub, ".." IS root — but spelled so that every path derived
	// from it keeps a leading "..".
	t.Chdir(filepath.Join(root, "sub"))
	relative := Coverage("..", []string{"claudecode"}, declares)

	if !reflect.DeepEqual(direct, relative) {
		t.Errorf("a repo root spelled \"..\" must give the same report\nabsolute: %+v\nrelative: %+v",
			direct.Adapters, relative.Adapters)
	}
	if relative.Adapters[0].Status == StatusGap {
		t.Errorf("relative repo root invented a GAP for an adapter with %d hook-bearing recordings",
			direct.Adapters[0].WithHooks)
	}
}

// writeHookFixture lays down one claudecode cell with one hook-bearing
// recording — the minimum shape Coverage reads. The hook_received line is
// preceded by a transcript_new for the same session_id carrying
// adapter:"claude-code" so the event is genuinely attributable to claudecode
// (see the Attribution doc on hasOwnHookEvent) — without it, this fixture
// would trip the same unattributable-event exclusion TestUnattributableHookEventDoesNotCount
// covers, for a reason unrelated to what this test actually checks (repo-root
// relativity).
func writeHookFixture(t *testing.T, root string) {
	t.Helper()
	mk := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cell := filepath.Join(root, "replaydata", "agents", "claudecode", "scenarios", "1-1_one")
	mk(filepath.Join(cell, "metadata.json"), `{"scenario_id":"one"}`)
	mk(filepath.Join(cell, "recordings", "r1", "events.jsonl"),
		`{"seq":1,"ts":"2026-05-01T00:00:00Z","kind":"transcript_new","session_id":"s","adapter":"claude-code"}`+"\n"+
			`{"seq":2,"ts":"2026-05-01T00:00:01Z","kind":"hook_received","session_id":"s","hook_name":"PostToolUse"}`+"\n")
}

// TestHermesCoexistHookNotCredited is the defect test for #1768, run against
// the already-committed
// replaydata/agents/hermes/scenarios/4-2_multiple-agents-same-workspace
// recording — no new fixture needed. That recording is the WHOLE of hermes's
// hook_received corpus (`grep -c hook_received` on its events.jsonl is 1),
// and the one event's session_id (8b9025f8-5346-484e-89a6-41e991602e47) is
// tagged adapter:"claude-code" by every sibling transcript_new /
// transcript_activity event for that session_id in the SAME sidecar — a
// co-resident Claude Code session's Stop hook, landed in hermes's cell
// directory because this is a coexistence scenario, not a hermes hook. hermes's
// own session in this recording (20260803_011105_d40f63) carries zero
// hook_received events of its own.
//
// Before the fix, hasHookEvent counts any hook_received regardless of whose
// session it names, so this recording alone flips hermes's row to StatusOK.
// The fix must read it as StatusGap: hermes declares hooks (#1722) and this
// corpus supplies none of its own.
func TestHermesCoexistHookNotCredited(t *testing.T) {
	rep := Coverage(repoRoot(t), []string{"hermes"}, map[string]bool{"hermes": true})
	if len(rep.Adapters) != 1 {
		t.Fatalf("Coverage(hermes) = %d rows, want 1: %+v", len(rep.Adapters), rep.Adapters)
	}
	row := rep.Adapters[0]
	if row.WithHooks != 0 {
		t.Errorf("hermes WithHooks = %d, want 0 — the one hook_received event in "+
			"4-2_multiple-agents-same-workspace belongs to session "+
			"8b9025f8-5346-484e-89a6-41e991602e47, tagged adapter:\"claude-code\" by "+
			"every sibling event for that session — not hermes's own", row.WithHooks)
	}
	if row.Status != StatusGap {
		t.Errorf("hermes Status = %q, want %q (declares hooks, corpus supplies none of its own)",
			row.Status, StatusGap)
	}
}

// TestUnattributableHookEventDoesNotCount pins the other half of the
// Attribution rule directly: a hook_received event whose session_id has NO
// sibling event carrying an Adapter anywhere in the sidecar must not count
// for anybody — the spec calls this out explicitly ("an unattributable event
// must NOT count as coverage") precisely so the fix cannot default an
// unresolvable session to the cell's own adapter, which would silently
// launder the exact bug #1768 is about.
func TestUnattributableHookEventDoesNotCount(t *testing.T) {
	root := t.TempDir()
	mk := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cell := filepath.Join(root, "replaydata", "agents", "hermes", "scenarios", "1-1_one")
	mk(filepath.Join(cell, "metadata.json"), `{"scenario_id":"one"}`)
	mk(filepath.Join(cell, "recordings", "r1", "events.jsonl"),
		`{"seq":1,"ts":"2026-05-01T00:00:00Z","kind":"hook_received","session_id":"orphan","hook_name":"Stop"}`+"\n")

	rep := Coverage(root, []string{"hermes"}, map[string]bool{"hermes": true})
	if got := rep.Adapters[0].WithHooks; got != 0 {
		t.Errorf("WithHooks = %d, want 0 — session %q has no sibling event naming an adapter, so it must not be credited to hermes (or anyone)", got, "orphan")
	}
	if rep.Adapters[0].Status != StatusGap {
		t.Errorf("Status = %q, want %q", rep.Adapters[0].Status, StatusGap)
	}
}

// TestOwnHookEventStillCounts is the vacuity guard: attribution-by-session
// must not degenerate into "no hook_received event ever counts" — a
// hook_received event whose session_id IS tagged as the adapter's own by a
// sibling event in the same sidecar must still count. Without this, the fix
// for #1768 would make hermes's StatusGap unfalsifiable — permanently GAP no
// matter what the corpus contains — which is a check with no genuine content,
// not a repaired one.
func TestOwnHookEventStillCounts(t *testing.T) {
	root := t.TempDir()
	mk := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cell := filepath.Join(root, "replaydata", "agents", "hermes", "scenarios", "1-1_one")
	mk(filepath.Join(cell, "metadata.json"), `{"scenario_id":"one"}`)
	mk(filepath.Join(cell, "recordings", "r1", "events.jsonl"),
		`{"seq":1,"ts":"2026-05-01T00:00:00Z","kind":"transcript_new","session_id":"s1","adapter":"hermes"}`+"\n"+
			`{"seq":2,"ts":"2026-05-01T00:00:01Z","kind":"hook_received","session_id":"s1","hook_name":"Stop"}`+"\n")

	rep := Coverage(root, []string{"hermes"}, map[string]bool{"hermes": true})
	if got := rep.Adapters[0].WithHooks; got != 1 {
		t.Fatalf("WithHooks = %d, want 1 — session %q is tagged adapter:\"hermes\" by its own transcript_new, so its hook_received event is hermes's own", got, "s1")
	}
	if rep.Adapters[0].Status != StatusOK {
		t.Errorf("Status = %q, want %q", rep.Adapters[0].Status, StatusOK)
	}
}

// TestGapsListsOnlyGaps confirms the convenience accessor the CLI shouts with.
func TestGapsListsOnlyGaps(t *testing.T) {
	r := Report{Adapters: []AdapterCoverage{
		{Adapter: "aider", Status: StatusNone},
		{Adapter: "claudecode", Status: StatusOK},
		{Adapter: "codex", Status: StatusGap},
		{Adapter: "copilot", Status: StatusIncidental},
	}}
	if got := r.Gaps(); !reflect.DeepEqual(got, []string{"codex"}) {
		t.Errorf("Gaps() = %v, want [codex]", got)
	}
	if got := (Report{}).Gaps(); got != nil {
		t.Errorf("Gaps() on an empty report = %v, want nil", got)
	}
}
