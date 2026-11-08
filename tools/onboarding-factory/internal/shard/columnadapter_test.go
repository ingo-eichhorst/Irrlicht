package shard

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDaemonAdapterForSlug pins the column → adapter map, including the
// many-to-one entry #2109 adds: opencode-v2 is the opencode adapter's second
// column, so opencode has two slugs and SlugForAdapter cannot invert it.
func TestDaemonAdapterForSlug(t *testing.T) {
	cases := map[string]string{
		"claudecode":       "claude-code",
		"deepseek-harness": "dsh",
		"opencode-v2":      "opencode",
		"opencode":         "opencode",
		"codex":            "codex",
		"mistral-vibe":     "mistral-vibe",
		"":                 "",
	}
	for slug, want := range cases {
		if got := DaemonAdapterForSlug(slug); got != want {
			t.Errorf("DaemonAdapterForSlug(%q) = %q, want %q", slug, got, want)
		}
	}
	if got := SlugForAdapter("opencode"); got != "opencode" {
		t.Errorf("SlugForAdapter(\"opencode\") = %q, want the V1 column \"opencode\" — the "+
			"adapter's primary column is unchanged by its second one", got)
	}
}

// TestShellSlugMapMatchesTheGoMap keeps the rig's shell copy of the map
// (scripts/lib/adapter-slug.sh) equal to DaemonAdapterForSlug for every
// onboarded column and every mapped slug. The shell copy is the one the
// recording daemon's IRRLICHT_RECORD_ADAPTERS and the unapplied-grants filter
// read, and both fail silently on a miss: before #2109 it lacked
// deepseek-harness, so a dsh recording scoped dsh's own grants out.
func TestShellSlugMapMatchesTheGoMap(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	lib := filepath.Join(repoRoot, "tools", "onboarding-factory", "scripts", "lib", "adapter-slug.sh")

	slugs := map[string]bool{}
	for _, s := range Agents(repoRoot) {
		slugs[s] = true
	}
	for _, s := range AliasedColumns() {
		slugs[s] = true
	}
	// Vacuity guard: an unreadable catalog would leave only the aliases, and
	// the comparison below would grade three slugs while claiming the corpus.
	if len(slugs) <= len(AliasedColumns()) {
		t.Fatalf("Agents(%s) returned no onboarded column — the catalog was not read, so "+
			"this check graded nothing", repoRoot)
	}

	for slug := range slugs {
		// #nosec G204 -- bash runs a repo-owned library with a slug read from the catalog.
		out, err := exec.Command("bash", "-c",
			`set -euo pipefail; source "$1"; adapter_slug_to_daemon_name "$2"`, "_", lib, slug).CombinedOutput()
		if err != nil {
			t.Fatalf("running adapter_slug_to_daemon_name %q from %s: %v: %s", slug, lib, err, out)
		}
		if got, want := strings.TrimSpace(string(out)), DaemonAdapterForSlug(slug); got != want {
			t.Errorf("column %q: the shell rig names adapter %q, shard.DaemonAdapterForSlug names %q — "+
				"a recording daemon given the shell's answer scopes the recorded adapter's own "+
				"grants out on an exact-string miss", slug, got, want)
		}
	}
}
