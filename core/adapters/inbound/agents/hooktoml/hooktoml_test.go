package hooktoml

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/pkg/atomicfile"
)

const testSentinel = "hook-post mistral-vibe "

func testEntry(command string) []byte {
	return []byte("[[hooks]]\n" +
		"name = " + Quote("irrlicht-turn-done") + "\n" +
		"type = " + Quote("post_agent_turn") + "\n" +
		"command = " + Quote(command) + "\n")
}

func testCanonical(want string) func([]byte) bool {
	return func(section []byte) bool {
		return strings.Contains(string(section), Quote(want))
	}
}

func testConfig(t *testing.T, path, command string) HookConfig {
	t.Helper()
	return HookConfig{
		Path:        path,
		Sentinel:    testSentinel,
		Entry:       func() []byte { return testEntry(command) },
		IsCanonical: testCanonical(command),
		WriteFile:   atomicfile.WriteFile,
	}
}

// --- EnsureInstalled / Uninstall round trip ---

func TestEnsureInstalled_CreatesFileWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	cfg := testConfig(t, path, "irrlichd hook-post mistral-vibe >/dev/null || true")

	modified, err := EnsureInstalled(cfg)
	if err != nil || !modified {
		t.Fatalf("EnsureInstalled: modified=%v err=%v, want true/nil", modified, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "[[hooks]]") || !strings.Contains(string(data), testSentinel) {
		t.Errorf("installed file missing our block:\n%s", data)
	}
}

func TestEnsureInstalled_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	cfg := testConfig(t, path, "irrlichd hook-post mistral-vibe >/dev/null || true")

	if _, err := EnsureInstalled(cfg); err != nil {
		t.Fatalf("first install: %v", err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	modified, err := EnsureInstalled(cfg)
	if err != nil || modified {
		t.Fatalf("second install: modified=%v err=%v, want false/nil", modified, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Errorf("second install changed bytes:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestEnsureInstalled_PreservesUserContentAndComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	seed := `# my personal lint hook
[[hooks]]
name = "lint"                       # Required: unique within the file.
type = "post_agent_turn"            # comment on its own
command = "eslint --quiet ."
timeout = 60.0

[[hooks]]
name = "deny-rm-rf"
type = "before_tool"
match = "bash"
command = "uv run python /path/to/guard-bash"
`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, path, "irrlichd hook-post mistral-vibe >/dev/null || true")

	modified, err := EnsureInstalled(cfg)
	if err != nil || !modified {
		t.Fatalf("EnsureInstalled: modified=%v err=%v", modified, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), seed) {
		t.Errorf("user's original content was not preserved byte-for-byte at the head of the file:\n%s", got)
	}
	if !strings.Contains(string(got), "[[hooks]]\nname = \"irrlicht-turn-done\"") {
		t.Errorf("our block was not appended:\n%s", got)
	}
	// Every comment from the seed must survive verbatim.
	for _, c := range []string{
		"# my personal lint hook",
		"# Required: unique within the file.",
		"# comment on its own",
	} {
		if !strings.Contains(string(got), c) {
			t.Errorf("comment %q did not survive:\n%s", c, got)
		}
	}
}

func TestEnsureInstalled_RewritesStaleEntryInPlace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	cfg := testConfig(t, path, "irrlichd hook-post mistral-vibe >/dev/null || true")
	if _, err := EnsureInstalled(cfg); err != nil {
		t.Fatalf("first install: %v", err)
	}

	// Simulate a moved binary: the sentinel is the same, the command differs.
	staleCfg := testConfig(t, path, "/other/irrlichd hook-post mistral-vibe >/dev/null || true")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureInstalled(cfg) // canonical command, should now say "stale" vs the file on disk? No — file already has staleCfg's command from the FIRST install above using cfg (canonical). Re-run against cfg detects it's already canonical.
	_ = before
	if err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	if modified {
		t.Fatalf("re-running the SAME canonical config reported a modification")
	}

	// Now install the "stale" shape directly (simulating an old install),
	// then reconcile with the canonical config and confirm it rewrites in
	// place rather than duplicating.
	if _, err := EnsureInstalled(staleCfg); err != nil {
		t.Fatalf("seeding a stale install: %v", err)
	}
	modified, err = EnsureInstalled(cfg)
	if err != nil || !modified {
		t.Fatalf("reconciling stale->canonical: modified=%v err=%v, want true/nil", modified, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(got), "[[hooks]]") != 1 {
		t.Errorf("reconciling created a duplicate block instead of rewriting in place:\n%s", got)
	}
	if !strings.Contains(string(got), "irrlicht hook-post mistral-vibe") && !strings.Contains(string(got), "irrlichd hook-post mistral-vibe") {
		t.Errorf("rewritten block does not carry the canonical command:\n%s", got)
	}
}

func TestUninstall_RemovesOnlyOurBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	seed := `[[hooks]]
name = "lint"
type = "post_agent_turn"
command = "eslint --quiet ."
`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, path, "irrlichd hook-post mistral-vibe >/dev/null || true")
	if _, err := EnsureInstalled(cfg); err != nil {
		t.Fatalf("install: %v", err)
	}

	modified, err := Uninstall(cfg)
	if err != nil || !modified {
		t.Fatalf("Uninstall: modified=%v err=%v, want true/nil", modified, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), testSentinel) {
		t.Errorf("our entry survived uninstall:\n%s", got)
	}
	if !strings.Contains(string(got), `name = "lint"`) {
		t.Errorf("the user's own hook was removed too:\n%s", got)
	}
}

func TestUninstall_AbsentFileIsANoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	cfg := testConfig(t, path, "irrlichd hook-post mistral-vibe >/dev/null || true")

	modified, err := Uninstall(cfg)
	if err != nil || modified {
		t.Fatalf("Uninstall on absent file: modified=%v err=%v, want false/nil", modified, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("Uninstall created the file")
	}
}

func TestInspect_ReportsPresenceAndCanonicity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	cfg := testConfig(t, path, "irrlichd hook-post mistral-vibe >/dev/null || true")

	present, canonical, err := Inspect(cfg)
	if err != nil {
		t.Fatalf("Inspect before install: %v", err)
	}
	if present || canonical {
		t.Errorf("Inspect before install: present=%v canonical=%v, want false/false", present, canonical)
	}

	if _, err := EnsureInstalled(cfg); err != nil {
		t.Fatal(err)
	}
	present, canonical, err = Inspect(cfg)
	if err != nil || !present || !canonical {
		t.Errorf("Inspect after install: present=%v canonical=%v err=%v, want true/true/nil", present, canonical, err)
	}

	staleCfg := testConfig(t, path, "/other/irrlichd hook-post mistral-vibe >/dev/null || true")
	present, canonical, err = Inspect(staleCfg)
	if err != nil || !present || canonical {
		t.Errorf("Inspect against a differently-commanded config: present=%v canonical=%v err=%v, want true/false/nil", present, canonical, err)
	}
}

func TestInspect_DoesNotCreateTheFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	cfg := testConfig(t, path, "irrlichd hook-post mistral-vibe >/dev/null || true")

	if _, _, err := Inspect(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("Inspect must not create the file")
	}
}

// --- top-level scalar operations ---

func TestEnsureBoolTrue_CreatesFileWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	modified, err := EnsureBoolTrue(path, "enable_experimental_hooks", atomicfile.WriteFile)
	if err != nil || !modified {
		t.Fatalf("EnsureBoolTrue: modified=%v err=%v, want true/nil", modified, err)
	}
	value, found, err := TopLevelBool(path, "enable_experimental_hooks")
	if err != nil || !found || !value {
		t.Errorf("value=%v found=%v err=%v, want true/true/nil", value, found, err)
	}
}

func TestEnsureBoolTrue_IsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if _, err := EnsureBoolTrue(path, "enable_experimental_hooks", atomicfile.WriteFile); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)

	modified, err := EnsureBoolTrue(path, "enable_experimental_hooks", atomicfile.WriteFile)
	if err != nil || modified {
		t.Fatalf("second call: modified=%v err=%v, want false/nil", modified, err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Errorf("idempotent call changed bytes")
	}
}

func TestEnsureBoolTrue_FlipsAnExistingFalseAndPreservesEverythingElse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	seed := `active_model = "mistral-medium-3.5"  # my favourite
enable_experimental_hooks = false
bypass_tool_permissions = false

[[providers]]
name = "mistral"
`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureBoolTrue(path, "enable_experimental_hooks", atomicfile.WriteFile)
	if err != nil || !modified {
		t.Fatalf("EnsureBoolTrue: modified=%v err=%v", modified, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `active_model = "mistral-medium-3.5"  # my favourite`) {
		t.Errorf("unrelated key/comment lost:\n%s", got)
	}
	if !strings.Contains(string(got), "bypass_tool_permissions = false") {
		t.Errorf("unrelated key lost:\n%s", got)
	}
	if !strings.Contains(string(got), "[[providers]]\nname = \"mistral\"") {
		t.Errorf("table content lost:\n%s", got)
	}
	if strings.Contains(string(got), "enable_experimental_hooks = false") {
		t.Errorf("flag was not flipped to true:\n%s", got)
	}
	if !strings.Contains(string(got), "enable_experimental_hooks = true") {
		t.Errorf("flag not present as true:\n%s", got)
	}
}

func TestEnsureBoolTrue_DoesNotMatchTheKeyInsideATable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	// A key of the same name, but inside a table, must never be treated as
	// the top-level flag — TOML itself requires bare keys before any table.
	seed := "[some_table]\nenable_experimental_hooks = false\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureBoolTrue(path, "enable_experimental_hooks", atomicfile.WriteFile)
	if err != nil || !modified {
		t.Fatalf("EnsureBoolTrue: modified=%v err=%v", modified, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "[some_table]\nenable_experimental_hooks = false") {
		t.Errorf("the table's own key was rewritten, not left alone:\n%s", got)
	}
	if !strings.HasPrefix(string(got), "enable_experimental_hooks = true\n") {
		t.Errorf("a fresh top-level assignment was not inserted before the table:\n%s", got)
	}
}

func TestClearBoolIfPresent_LeavesAnAbsentKeyAbsent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	seed := "active_model = \"x\"\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	modified, err := ClearBoolIfPresent(path, "enable_experimental_hooks", atomicfile.WriteFile)
	if err != nil || modified {
		t.Fatalf("ClearBoolIfPresent on an absent key: modified=%v err=%v, want false/nil", modified, err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != seed {
		t.Errorf("file changed despite nothing to clear:\n%s", got)
	}
}

func TestClearBoolIfPresent_SetsAnExistingKeyFalse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if _, err := EnsureBoolTrue(path, "enable_experimental_hooks", atomicfile.WriteFile); err != nil {
		t.Fatal(err)
	}

	modified, err := ClearBoolIfPresent(path, "enable_experimental_hooks", atomicfile.WriteFile)
	if err != nil || !modified {
		t.Fatalf("ClearBoolIfPresent: modified=%v err=%v, want true/nil", modified, err)
	}
	value, found, err := TopLevelBool(path, "enable_experimental_hooks")
	if err != nil || !found || value {
		t.Errorf("value=%v found=%v err=%v, want false/true/nil", value, found, err)
	}
}

// --- HasAnyHooksBlock ---

func TestHasAnyHooksBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")

	has, err := HasAnyHooksBlock(path)
	if err != nil || has {
		t.Fatalf("absent file: has=%v err=%v, want false/nil", has, err)
	}

	seed := "[[hooks]]\nname = \"lint\"\ntype = \"post_agent_turn\"\ncommand = \"eslint\"\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	has, err = HasAnyHooksBlock(path)
	if err != nil || !has {
		t.Fatalf("file with a user hook: has=%v err=%v, want true/nil", has, err)
	}
}

// --- HookBlocks / FieldValue (the contracttesting #1178 seam) ---

func TestHookBlocks_AbsentFileIsZeroBlocksNotAnError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")

	blocks, err := HookBlocks(path)
	if err != nil {
		t.Fatalf("HookBlocks on an absent file: %v, want nil error", err)
	}
	if len(blocks) != 0 {
		t.Fatalf("HookBlocks on an absent file: got %d blocks, want 0", len(blocks))
	}
}

func TestHookBlocks_ReturnsEveryBlockInFileOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	seed := "[[hooks]]\nname = \"a\"\ntype = \"post_agent_turn\"\ncommand = \"cmd-a\"\n\n" +
		"[[hooks]]\nname = \"b\"\ntype = \"before_tool\"\ncommand = \"cmd-b\"\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	blocks, err := HookBlocks(path)
	if err != nil {
		t.Fatalf("HookBlocks: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("HookBlocks returned %d blocks, want 2:\n%v", len(blocks), blocks)
	}
	if !strings.Contains(string(blocks[0]), `name = "a"`) {
		t.Errorf("block 0 = %q, want the \"a\" block", blocks[0])
	}
	if !strings.Contains(string(blocks[1]), `name = "b"`) {
		t.Errorf("block 1 = %q, want the \"b\" block", blocks[1])
	}
}

// TestHookBlocks_RefusesOnUnterminatedCommand is the mutation evidence for
// HookBlocks' own doc comment: a block whose command value is opened and
// never closed — the state a truncated write leaves behind — must be
// refused, not silently read as a block with no command field at all. The
// same fixture shape contracttesting's
// TestReferenceTOMLReadEntries_RejectsUnterminatedCommand pins for its
// scanner; this is the identical property pinned directly against
// HookBlocks, which is what the real #1178 wiring actually calls.
func TestHookBlocks_RefusesOnUnterminatedCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	malformed := "[[hooks]]\nname = \"broken\"\ntype = \"post_agent_turn\"\ncommand = \"curl -sf http://127.0.0.1:7837/api/v1/hooks\n"
	if err := os.WriteFile(path, []byte(malformed), 0o600); err != nil {
		t.Fatal(err)
	}

	blocks, err := HookBlocks(path)
	if err == nil {
		t.Fatalf("HookBlocks on an unterminated command string: got %d blocks and no error, want a refusal", len(blocks))
	}
}

func TestFieldValue_ExtractsAndDecodesAQuotedField(t *testing.T) {
	block := []byte("[[hooks]]\nname = \"irrlicht-turn-done\"\ntype = \"post_agent_turn\"\n" +
		"command = " + Quote(`/opt/irrlicht/bin/irrlichd --version hook-post mistral-vibe >/dev/null || true`) + "\n")

	got, ok := FieldValue(block, "command")
	if !ok {
		t.Fatal("FieldValue did not find the command field")
	}
	want := `/opt/irrlicht/bin/irrlichd --version hook-post mistral-vibe >/dev/null || true`
	if got != want {
		t.Errorf("FieldValue(command) = %q, want %q", got, want)
	}
}

func TestFieldValue_DecodesEscapedQuotesAndBackslashes(t *testing.T) {
	raw := `/path with "quotes" and \backslash`
	block := []byte("command = " + Quote(raw) + "\n")

	got, ok := FieldValue(block, "command")
	if !ok || got != raw {
		t.Errorf("FieldValue(command) = %q, ok=%v, want %q, true", got, ok, raw)
	}
}

func TestFieldValue_AbsentKeyReportsNotFound(t *testing.T) {
	block := []byte("[[hooks]]\nname = \"a\"\n")
	if _, ok := FieldValue(block, "command"); ok {
		t.Error("FieldValue found a command field that is not there")
	}
}

// --- refusal on constructs the scanner does not model ---

func TestEnsureInstalled_RefusesOnTripleQuotedString(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	seed := "description = \"\"\"\nmulti\nline\n\"\"\"\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, path, "irrlichd hook-post mistral-vibe >/dev/null || true")

	_, err := EnsureInstalled(cfg)
	if err == nil {
		t.Fatal("EnsureInstalled did not refuse a document containing a triple-quoted string")
	}
	got, _ := os.ReadFile(path)
	if string(got) != seed {
		t.Errorf("file was modified despite the refusal:\n%s", got)
	}
}

// TestEnsureInstalled_ModelsAMultiLineArray replaces
// TestEnsureInstalled_RefusesOnMultiLineArray, which pinned the OPPOSITE
// verdict on this exact seed until #1753. The behaviour it locked was wrong:
// mistral-vibe's own writer emits multi-line arrays, so refusing them refused
// every realistic user config. The seed is kept byte-for-byte from the
// predecessor so the change of verdict is visible as a change of verdict
// rather than as a test that quietly stopped existing.
func TestEnsureInstalled_ModelsAMultiLineArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	seed := "tags = [\n  \"a\",\n  \"b\",\n]\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, path, "irrlichd hook-post mistral-vibe >/dev/null || true")

	modified, err := EnsureInstalled(cfg)
	if err != nil {
		t.Fatalf("EnsureInstalled refused a document containing a multi-line array: %v", err)
	}
	if !modified {
		t.Fatal("EnsureInstalled reported no modification")
	}
	got, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(got), seed) {
		t.Errorf("the pre-existing multi-line array did not survive verbatim:\n%s", got)
	}
	present, canonical, err := Inspect(cfg)
	if err != nil || !present || !canonical {
		t.Errorf("Inspect after install: present=%v canonical=%v err=%v", present, canonical, err)
	}
}

// TestScannerRefusals_Corpus is the committed mutation evidence for every
// construct the scanner still refuses after #1753 widened it, and — the half
// that carries the value — for the documents it must NOT refuse. A scanner
// that refused everything and one that refuses correctly are
// indistinguishable without the must-accept rows, and that is not a
// hypothetical here: refusing too much is precisely the defect this ticket
// exists to fix.
//
// Both entry points are driven for every row, because they are two separate
// scanners over the same bytes (scanDocument for the [[hooks]] operations,
// topLevelKeyLine for the top-level scalar) and #1753 is the ticket where one
// of them refusing while the other did not would have been invisible.
func TestScannerRefusals_Corpus(t *testing.T) {
	cases := []struct {
		name string
		doc  string
		// refuseNaming is the fragment the refusal must name; "" means the
		// document must be ACCEPTED by both entry points.
		refuseNaming string
		// line, when non-zero, is the 1-based line the refusal must point at.
		line int
	}{
		// --- still refused ---
		{"triple-quoted basic string", "a = \"\"\"\nx\n\"\"\"\n", "triple-quoted", 1},
		{"triple-quoted literal string", "k = 1\nb = '''\nx\n'''\n", "triple-quoted", 2},
		{"unterminated basic quote", "k = 1\nname = \"oops\nother = 2\n", "unterminated quote", 2},
		{"unterminated literal quote", "name = 'oops\n", "unterminated quote", 1},
		{"multi-line inline table", "k = 1\npoint = { x = 1,\n  y = 2 }\n", "inline table spanning", 2},
		{"stray closing bracket", "k = 1\n]\n", "unbalanced ']'", 2},
		{"array never closed (reported at its OPENING line)", "k = 1\ntags = [\n  \"a\",\n", "never closed", 2},

		// --- must be accepted (the vacuity guard) ---
		{"empty document", "", "", 0},
		{"single-line array", "tags = [\"a\", \"b\"]\n", "", 0},
		{"multi-line array at the top level", "tags = [\n  \"a\",\n]\n", "", 0},
		{"multi-line array inside a table", "[t]\ntags = [\n  \"a\",\n]\n", "", 0},
		{"multi-line array of single-line inline tables", "t = [\n  { a = 1 },\n  { b = 2 },\n]\n", "", 0},
		{"nested multi-line array", "m = [\n  [1, 2],\n  [\n    3,\n  ],\n]\n", "", 0},
		{"brackets and braces inside strings", "s = \"[ { ] }\"\nl = '] } ['\n", "", 0},
		{"array opener carrying a comment", "tags = [  # why\n  \"a\",\n]\n", "", 0},
		{"a header-shaped string inside an array", "tags = [\n  \"[[hooks]]\",\n]\n[[hooks]]\nname = \"x\"\n", "", 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "doc.toml")
			if err := os.WriteFile(path, []byte(tc.doc), 0o600); err != nil {
				t.Fatal(err)
			}

			// Entry point 1: the [[hooks]] scanner, reached read-only so an
			// accepted document is not rewritten under the next assertion.
			_, blocksErr := HookBlocks(path)
			// Entry point 2: the top-level scalar scanner.
			_, _, scalarErr := TopLevelBool(path, "enable_experimental_hooks")

			for label, err := range map[string]error{"HookBlocks": blocksErr, "TopLevelBool": scalarErr} {
				if tc.refuseNaming == "" {
					if err != nil {
						t.Errorf("%s refused a document it must accept: %v", label, err)
					}
					continue
				}
				if err == nil {
					t.Errorf("%s accepted a document it must refuse", label)
					continue
				}
				if !errors.Is(err, ErrUnsafe) {
					t.Errorf("%s: error %v does not wrap ErrUnsafe", label, err)
				}
				var unsafe *UnsafeConstructError
				if !errors.As(err, &unsafe) {
					t.Fatalf("%s: error %v is not an *UnsafeConstructError", label, err)
				}
				if !strings.Contains(unsafe.Construct, tc.refuseNaming) {
					t.Errorf("%s: refusal names %q, want it to mention %q", label, unsafe.Construct, tc.refuseNaming)
				}
				if unsafe.Path != path {
					t.Errorf("%s: refusal names path %q, want %q", label, unsafe.Path, path)
				}
				if tc.line != 0 && unsafe.Line != tc.line {
					t.Errorf("%s: refusal points at line %d, want %d", label, unsafe.Line, tc.line)
				}
				if !strings.Contains(err.Error(), path) {
					t.Errorf("%s: rendered message %q does not name the file — it is what the wizard shows", label, err.Error())
				}
			}
		})
	}
}

// --- continuation lines are classified as NOTHING ---
//
// The two tests below are the committed mutation evidence for the single
// rule that makes #1753's widening safe rather than merely permissive:
// inside a multi-line array, no line is a header, a comment or a key.
//
// They exist because the property test did NOT catch it. Deleting
// scanDocument's `if depth == 0` guard left the entire suite — 4000 generated
// documents included — green, because every adversarial array element the
// generator emitted was a QUOTED string, and `"[[hooks]]"` starts with a
// quote and is header-shaped to nobody. The shape that bites is a bare nested
// array written as the last element without a trailing comma: its line reads
// `[1, 2]`, which is precisely what isTableHeaderLine matches. The generator
// now emits that too (property_test.go), and these two pin it deterministically
// at the two places a spurious header actually changes bytes.

// TestHookBlocks_HeaderShapedArrayElementDoesNotSplitABlock: a spurious
// header inside an array truncates the block it sits in, so the block's own
// later fields silently stop being part of it.
func TestHookBlocks_HeaderShapedArrayElementDoesNotSplitABlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	seed := "[[hooks]]\nname = \"a\"\nmatrix = [\n  [1, 2]\n]\ncommand = \"eslint .\"\n\n[[hooks]]\nname = \"b\"\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	blocks, err := HookBlocks(path)
	if err != nil {
		t.Fatalf("HookBlocks: %v", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("HookBlocks returned %d blocks, want 2:\n%s", len(blocks), blocks)
	}
	if !strings.Contains(string(blocks[0]), `command = "eslint ."`) {
		t.Errorf("block 0 lost the field below its multi-line array — a line inside the array "+
			"was classified as a section header:\n%s", blocks[0])
	}
}

// TestEnsureBoolTrue_InsertsAfterAPreambleArrayNotInsideIt: a spurious header
// inside a PREAMBLE array is what firstHeaderOffset returns, so the new
// assignment is spliced into the middle of the user's array.
func TestEnsureBoolTrue_InsertsAfterAPreambleArrayNotInsideIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	seed := "matrix = [\n  [1, 2]\n]\n\n[session_logging]\nenabled = true\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureBoolTrue(path, "enable_experimental_hooks", atomicfile.WriteFile); err != nil {
		t.Fatalf("EnsureBoolTrue: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !strings.HasPrefix(string(got), "matrix = [\n  [1, 2]\n]\n") {
		t.Errorf("the flag was spliced INSIDE the user's preamble array:\n%s", got)
	}
	if !strings.Contains(string(got), "enable_experimental_hooks = true\n[session_logging]") {
		t.Errorf("the flag did not land immediately before the first header:\n%s", got)
	}
}

// TestEnsureBoolTrue_ReplacesTheWholeOfAMultiLineValue is the evidence for
// the second half of topLevelKeyLine's #1753 change — that it returns the
// LOGICAL line's range, through the closing bracket, not just the physical
// opening line.
//
// The property test cannot reach this: its generator always renders the flag
// as a bool, which is the only shape vibe or this daemon ever writes. So the
// state is constructed by hand — a user (or a botched hand-edit) who left the
// gate key holding an array. Replacing only the opening line would leave
// `enable_experimental_hooks = true` followed by two lines of orphaned array
// syntax, which is a corrupted config.toml: the outcome hooktoml's whole
// refuse-rather-than-guess posture exists to prevent.
func TestEnsureBoolTrue_ReplacesTheWholeOfAMultiLineValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	seed := "a = 1\nenable_experimental_hooks = [\n  \"nonsense\",\n]\nb = 2\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureBoolTrue(path, "enable_experimental_hooks", atomicfile.WriteFile)
	if err != nil {
		t.Fatalf("EnsureBoolTrue: %v", err)
	}
	if !modified {
		t.Fatal("EnsureBoolTrue reported no modification")
	}
	got, _ := os.ReadFile(path)
	want := "a = 1\nenable_experimental_hooks = true\nb = 2\n"
	if string(got) != want {
		t.Errorf("EnsureBoolTrue left orphaned array syntax behind:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

// TestEnsureBoolTrue_RefusalNamesTheFileAndLine is #1753's route-3 half. The
// SURFACING already existed (#1362 records the effect error verbatim and both
// wizards render "granted but NOT applied, because <reason>"), so nothing new
// is built for it; what did not exist was a reason a user could act on. This
// pins the three facts that make it actionable.
func TestEnsureBoolTrue_RefusalNamesTheFileAndLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	// The construct is on line 3, after two perfectly ordinary lines.
	seed := "a = 1\nb = 2\nbanner = \"\"\"\nhi\n\"\"\"\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := EnsureBoolTrue(path, "enable_experimental_hooks", atomicfile.WriteFile)
	if err == nil {
		t.Fatal("EnsureBoolTrue did not refuse a document containing a triple-quoted string")
	}
	msg := err.Error()
	for _, want := range []string{path, ":3", "triple-quoted", "grant again", "`enable_experimental_hooks = true`"} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not mention %q", msg, want)
		}
	}
	if got, _ := os.ReadFile(path); string(got) != seed {
		t.Errorf("file was modified despite the refusal:\n%s", got)
	}
}

func TestEnsureBoolTrue_RefusesOnTripleQuotedStringInThePreamble(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	seed := "banner = '''\nhi\n'''\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := EnsureBoolTrue(path, "enable_experimental_hooks", atomicfile.WriteFile)
	if err == nil {
		t.Fatal("EnsureBoolTrue did not refuse a document containing a triple-quoted string")
	}
}

// --- Quote ---

func TestQuote_LeavesShellMetacharactersUnescaped(t *testing.T) {
	// The whole point of SetEscapeHTML(false): a bare '>' and '&' render
	// literally, the same as hookjson's own encodeValue for the identical
	// reason (the beacon command contains both).
	if q := Quote(`>/dev/null 2>&1`); !strings.Contains(q, ">") || !strings.Contains(q, "&") {
		t.Errorf(`Quote(">/dev/null 2>&1") = %q, HTML-escaped '>' or '&' instead of leaving them literal`, q)
	}
	if q := Quote(`a && b || true`); !strings.Contains(q, "&&") {
		t.Errorf(`Quote("a && b || true") = %q, HTML-escaped '&' instead of leaving it literal`, q)
	}
}

func TestQuote_EscapesQuotesAndBackslashes(t *testing.T) {
	cases := map[string]string{
		`plain`:         `"plain"`,
		`has "quotes"`:  `"has \"quotes\""`,
		`has\backslash`: `"has\\backslash"`,
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %q, want %q", in, got, want)
		}
	}
}
