package tailer

import (
	"os"
	"path/filepath"
	"testing"
)

// writeClaudeSettingsRaw seeds a hermetic HOME with the exact bytes given as
// ~/.claude/settings.json. Unlike writeClaudeSettingsFixture it does not build
// the document, so a case can hand it JSONC — or deliberate garbage — and
// still be sure it never touches the developer's real settings file.
func writeClaudeSettingsRaw(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// TestGetClaudeModel_ReadsCommentedSettings pins issue #1391.
//
// PR #1381 (issue #1371) taught hookjson.ReadSettings to blank JSONC comments
// before decoding, so irrlicht installs hooks into a commented
// ~/.claude/settings.json without complaint. getClaudeModel kept its plain
// json.Unmarshal, so the very same file that accepted our hooks yields no
// model at all — and no cost estimate derived from it. The failure is silent:
// the error path of that Unmarshal returns "".
//
// The two readers must agree on what a valid settings.json is.
func TestGetClaudeModel_ReadsCommentedSettings(t *testing.T) {
	cases := map[string]string{
		"line comment above the key": "{\n" +
			"  // pinned so cost estimates stay stable\n" +
			"  \"model\": \"claude-sonnet-4-20250514\"\n" +
			"}\n",
		"trailing line comment": "{\n" +
			"  \"model\": \"claude-sonnet-4-20250514\" // pinned\n" +
			"}\n",
		"block comment": "{\n" +
			"  /* team default, see docs/models.md */\n" +
			"  \"model\": \"claude-sonnet-4-20250514\"\n" +
			"}\n",
		"block comment spanning lines": "{\n" +
			"  /* why this model:\n" +
			"     it is the cheapest that passes our evals */\n" +
			"  \"model\": \"claude-sonnet-4-20250514\"\n" +
			"}\n",
	}

	want := NormalizeModelName("claude-sonnet-4-20250514")

	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			home := writeClaudeSettingsRaw(t, body)
			if got := getClaudeModel(home); got != want {
				t.Errorf("getClaudeModel on a commented settings.json = %q, want %q\nsettings.json was:\n%s", got, want, body)
			}
		})
	}
}

// TestGetClaudeModel_CommentDelimitersInsideStringsAreNotComments guards the
// obvious wrong fix — a naive strings.Index("//") strip would eat the value.
// A URL in a settings value is the everyday shape that breaks it.
func TestGetClaudeModel_CommentDelimitersInsideStringsAreNotComments(t *testing.T) {
	body := "{\n" +
		"  \"apiBaseUrl\": \"https://example.invalid/v1\", // not a comment above\n" +
		"  \"model\": \"claude-sonnet-4-20250514\"\n" +
		"}\n"
	home := writeClaudeSettingsRaw(t, body)
	want := NormalizeModelName("claude-sonnet-4-20250514")
	if got := getClaudeModel(home); got != want {
		t.Errorf("getClaudeModel = %q, want %q (a // inside a string value is data, not a comment)", got, want)
	}
}

// TestGetClaudeModel_MalformedStaysEmpty is a LOCK, not a defect test: it
// passes on main by construction and must keep passing.
//
// hookjson.ReadSettings deliberately errors on genuinely malformed input —
// overwriting such a file would destroy content the user meant to keep
// (hookjson_test.go TestReadSettings_MalformedJSONErrors, jsonc_test.go
// TestMalformedInput_StillErrors). Blanking comments must not make this reader
// any more permissive than that one: the shapes below are exactly the JSONC-
// adjacent ones hookjson still rejects, and getClaudeModel must keep yielding
// "" for each. If the two readers ever disagree about whether a file is valid,
// irrlicht will install hooks into a config it cannot read, or refuse one it
// can — which is the class of bug #1391 itself is.
func TestGetClaudeModel_MalformedStaysEmpty(t *testing.T) {
	cases := map[string]string{
		"trailing comma":   "{\n  \"model\": \"claude-sonnet-4-20250514\",\n}\n",
		"single quotes":    `{'model': 'claude-sonnet-4-20250514'}`,
		"unterminated str": `{"model": "claude-sonnet-4-20250514}`,
		"unclosed comment": "{\n  /* never closed\n  \"model\": \"claude-sonnet-4-20250514\"\n}\n",
		"bare word":        `{model: 1}`,
		"truncated":        `{"model":`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			home := writeClaudeSettingsRaw(t, body)
			if got := getClaudeModel(home); got != "" {
				t.Errorf("getClaudeModel on malformed settings.json = %q, want \"\" (must stay as strict as hookjson.ReadSettings)\nsettings.json was:\n%s", got, body)
			}
		})
	}
}
