package desktopdriver

// The configuration guard exists to prove a Desktop run left the operator's
// config alone. It can only do that for config the run could plausibly move.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfigFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

// pluginsRoot builds the shape ~/.claude/plugins actually has: the plugin set
// beside the CLI's own derived cache.
func pluginsRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), ".claude", "plugins")
	writeConfigFile(t, filepath.Join(root, "config.json"), `{"v":1}`)
	writeConfigFile(t, filepath.Join(root, "installed_plugins.json"), `{"plugins":[]}`)
	writeConfigFile(t, filepath.Join(root, "marketplaces", "official", "manifest.json"), `{}`)
	writeConfigFile(t, filepath.Join(root, ".last_inuse_sweep"), "2026-09-06T21:54:00Z")
	writeConfigFile(t, filepath.Join(root, "plugin-catalog-cache.json"), `{"catalog":[]}`)
	writeConfigFile(t, filepath.Join(root, "cache", "official", "frontend-design", "abc123", "plugin.json"), `{}`)
	return root
}

// RED-FIRST: this is cell 2-17 on 2026-09-06. The run drove its turn and
// captured every piece of evidence, then failed in cleanup on
// `unexpected path ".../.claude/plugins/cache/claude-plugins-official/
// frontend-design/85cce0381e78/.orphaned_at"` — a marker the Claude Code CLI's
// own in-use sweep drops on a cache entry it has orphaned, while the driver was
// driving Claude Desktop.
func TestConfigGuardIgnoresTheCLIsOwnDerivedCache(t *testing.T) {
	root := pluginsRoot(t)
	baseline, err := CaptureTreeSnapshot([]string{root})
	if err != nil {
		t.Fatalf("CaptureTreeSnapshot() error = %v", err)
	}
	if len(baseline) == 0 {
		t.Fatal("the baseline is empty; this check cannot compare a snapshot it never took")
	}

	// Everything below happens while a Desktop run is in flight, and none of it
	// is the run's doing.
	writeConfigFile(t,
		filepath.Join(root, "cache", "official", "frontend-design", "abc123", ".orphaned_at"),
		"2026-09-06T21:42:00Z")
	writeConfigFile(t, filepath.Join(root, ".last_inuse_sweep"), "2026-09-06T21:58:00Z")
	writeConfigFile(t, filepath.Join(root, "plugin-catalog-cache.json"), `{"catalog":["refetched"]}`)

	if err := VerifyTreeSnapshot(baseline); err != nil {
		t.Fatalf("VerifyTreeSnapshot() error = %v; the CLI's cache is not the driver's doing", err)
	}
}

// The other half. Narrowing the guard must not blind it to the plugin SET,
// which is what a run installing or removing a plugin would actually move.
func TestConfigGuardStillCatchesAChangeToThePluginSet(t *testing.T) {
	tests := map[string]func(root string){
		"installed plugins rewritten": func(root string) {
			writeConfigFile(t, filepath.Join(root, "installed_plugins.json"), `{"plugins":["new"]}`)
		},
		"a marketplace definition changed": func(root string) {
			writeConfigFile(t, filepath.Join(root, "marketplaces", "official", "manifest.json"), `{"changed":true}`)
		},
		"a new plugin file appeared": func(root string) {
			writeConfigFile(t, filepath.Join(root, "repos", "someone", "plugin.json"), `{}`)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			root := pluginsRoot(t)
			baseline, err := CaptureTreeSnapshot([]string{root})
			if err != nil {
				t.Fatal(err)
			}
			mutate(root)
			err = VerifyTreeSnapshot(baseline)
			if err == nil {
				t.Fatal("VerifyTreeSnapshot() returned nil; the plugin set moved and the guard missed it")
			}
			if !strings.Contains(err.Error(), "configuration changed") {
				t.Fatalf("VerifyTreeSnapshot() error = %v", err)
			}
		})
	}
}

// A directory called "cache" that is not the CLI's plugin cache stays guarded.
// The exemption matches a full path suffix for exactly this reason.
func TestConfigGuardExemptsOnlyTheNamedCachePaths(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Claude")
	writeConfigFile(t, filepath.Join(root, "cache", "settings.json"), `{}`)
	baseline, err := CaptureTreeSnapshot([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, filepath.Join(root, "cache", "settings.json"), `{"changed":true}`)
	if err := VerifyTreeSnapshot(baseline); err == nil {
		t.Fatal("a directory merely NAMED cache was exempted; the exemption must match a full path")
	}
}
