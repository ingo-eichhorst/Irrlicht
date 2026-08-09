package hookcov

import (
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

	// Exhaustive as of this commit: claudecode and codex are the only two
	// adapters declaring a hooks permission. If a third gains one, this fails
	// and the new adapter joins the list — that is the intended workflow, not
	// an obstacle.
	want := map[string]bool{
		"aider": false, "antigravity": false, "claudecode": true, "codex": true,
		"copilot": false, "gemini-cli": false, "hermes": false, "kiro-cli": false,
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

// TestSlugMapsOnlyClaudeCode locks the name mapping. Over-normalising is the
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
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
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
