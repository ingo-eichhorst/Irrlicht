package hooktoml

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		WriteFile:   AtomicWriteFile,
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

	modified, err := EnsureBoolTrue(path, "enable_experimental_hooks", AtomicWriteFile)
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
	if _, err := EnsureBoolTrue(path, "enable_experimental_hooks", AtomicWriteFile); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)

	modified, err := EnsureBoolTrue(path, "enable_experimental_hooks", AtomicWriteFile)
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

	modified, err := EnsureBoolTrue(path, "enable_experimental_hooks", AtomicWriteFile)
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

	modified, err := EnsureBoolTrue(path, "enable_experimental_hooks", AtomicWriteFile)
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

	modified, err := ClearBoolIfPresent(path, "enable_experimental_hooks", AtomicWriteFile)
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
	if _, err := EnsureBoolTrue(path, "enable_experimental_hooks", AtomicWriteFile); err != nil {
		t.Fatal(err)
	}

	modified, err := ClearBoolIfPresent(path, "enable_experimental_hooks", AtomicWriteFile)
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

func TestEnsureInstalled_RefusesOnMultiLineArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hooks.toml")
	seed := "tags = [\n  \"a\",\n  \"b\",\n]\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, path, "irrlichd hook-post mistral-vibe >/dev/null || true")

	_, err := EnsureInstalled(cfg)
	if err == nil {
		t.Fatal("EnsureInstalled did not refuse a document containing a multi-line array")
	}
}

func TestEnsureBoolTrue_RefusesOnTripleQuotedStringInThePreamble(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	seed := "banner = '''\nhi\n'''\n"
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := EnsureBoolTrue(path, "enable_experimental_hooks", AtomicWriteFile)
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
