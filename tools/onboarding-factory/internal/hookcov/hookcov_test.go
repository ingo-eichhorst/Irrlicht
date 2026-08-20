package hookcov

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"irrlicht/core/adapters/inbound/agents"
)

// TestDeclaredMatchesRegistry pins the code→catalog join. This is the part of
// the report that can rot without anything else noticing: rename the
// permission key, or change an adapter's Identity.Name without updating Slug,
// and every adapter silently reports "declares no hooks" — which turns every
// real gap into a benign-looking StatusNone.
func TestDeclaredMatchesRegistry(t *testing.T) {
	got := Declared()

	// Exhaustive as of this commit: claudecode, codex, copilot and gemini-cli
	// are the adapters declaring a hooks permission. If a fifth gains one,
	// this fails and the new adapter joins the list — that is the intended
	// workflow, not an obstacle.
	//
	// copilot joined in #1378 and gemini-cli in #1717, and both are expected
	// to report StatusGap for a while: each declares hooks, but no recording
	// for either carries a hook_received event yet, because landing the
	// permission deliberately re-recorded nothing (the frozen sidecars are
	// hook-free and the replay goldens had to stay byte-identical — #1717's
	// own audit measured 0 cells re-recorded). A GAP here is the honest
	// reading of that, not a defect.
	want := map[string]bool{
		"aider": false, "antigravity": false, "claudecode": true, "codex": true,
		"copilot": true, "gemini-cli": true, "hermes": false, "kiro-cli": false,
		"mistral-vibe": false, "opencode": false, "pi": false,
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
// recording — the minimum shape Coverage reads.
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
		`{"seq":1,"ts":"2026-05-01T00:00:00Z","kind":"hook_received","session_id":"s","hook_name":"PostToolUse"}`+"\n")
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
