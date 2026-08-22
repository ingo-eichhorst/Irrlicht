package hookyaml

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testOwner = "irrlicht"
	testBlock = "hooks"
)

func testConfig(path string) Config {
	return Config{
		Path:     path,
		BlockKey: testBlock,
		Owner:    testOwner,
		Entries: []Entry{
			{Event: "on_session_end", Command: "/bin/sh -c 'beacon hook-post x'", TimeoutSeconds: 2},
			{Event: "pre_approval_request", Command: "/bin/sh -c 'beacon hook-post x'", TimeoutSeconds: 2},
		},
	}
}

func writeTemp(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if body != "" {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) // #nosec G304 -- the temp path this test created
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return string(b)
}

// TestEnsureInstalled_AppendsWhenTheBlockIsAbsent is the common case: the user
// has no hooks at all, so the whole original document must survive as a
// byte-identical prefix.
func TestEnsureInstalled_AppendsWhenTheBlockIsAbsent(t *testing.T) {
	const src = "# a comment\nmodel:\n  default: \"x\"   # trailing\n"
	path := writeTemp(t, src)
	cfg := testConfig(path)

	changed, err := EnsureInstalled(cfg)
	if err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	if !changed {
		t.Fatal("reported no change on a first install")
	}
	got := read(t, path)
	if !strings.HasPrefix(got, src) {
		t.Fatalf("the original is not a byte-identical prefix:\n%s", got)
	}
	if !strings.Contains(got, "hooks:\n") {
		t.Errorf("no hooks block was written:\n%s", got)
	}
	if !strings.Contains(got, BeginMarker(testOwner)) || !strings.Contains(got, EndMarker(testOwner)) {
		t.Errorf("the region is not marker-delimited:\n%s", got)
	}
}

// TestEnsureInstalled_MergesIntoAnExistingBlock pins that a user's own hooks on
// OTHER events are untouched and that our region adopts their indent.
func TestEnsureInstalled_MergesIntoAnExistingBlock(t *testing.T) {
	const src = `hooks:
    post_tool_call:
        - command: "/usr/local/bin/fmt"
model:
  default: "x"
`
	path := writeTemp(t, src)
	if _, err := EnsureInstalled(testConfig(path)); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	got := read(t, path)

	for _, line := range strings.Split(strings.TrimRight(src, "\n"), "\n") {
		if !strings.Contains(got, line) {
			t.Errorf("original line %q is gone:\n%s", line, got)
		}
	}
	if !strings.Contains(got, "    "+BeginMarker(testOwner)) {
		t.Errorf("the region did not adopt the block's own 4-space indent:\n%s", got)
	}
}

// TestEnsureInstalled_RewritesAStaleRegionInPlace is the self-healing property:
// a region written by a differently-situated daemon is replaced, not
// duplicated.
func TestEnsureInstalled_RewritesAStaleRegionInPlace(t *testing.T) {
	path := writeTemp(t, "model:\n  default: \"x\"\n")
	old := testConfig(path)
	old.Entries[0].Command = "/bin/sh -c 'OLD hook-post x'"
	if _, err := EnsureInstalled(old); err != nil {
		t.Fatalf("first install: %v", err)
	}

	if _, err := EnsureInstalled(testConfig(path)); err != nil {
		t.Fatalf("second install: %v", err)
	}
	got := read(t, path)
	if n := strings.Count(got, BeginMarker(testOwner)); n != 1 {
		t.Errorf("found %d regions after a rewrite, want 1:\n%s", n, got)
	}
	if strings.Contains(got, "OLD hook-post") {
		t.Errorf("the stale command survived:\n%s", got)
	}
}

// TestUninstall_RemovesTheBlockWhenItEmpties pins the tidy uninstall, and that
// an unrelated user hook keeps the block alive.
func TestUninstall_RemovesTheBlockWhenItEmpties(t *testing.T) {
	t.Run("only ours", func(t *testing.T) {
		const src = "model:\n  default: \"x\"\n"
		path := writeTemp(t, src)
		cfg := testConfig(path)
		if _, err := EnsureInstalled(cfg); err != nil {
			t.Fatalf("install: %v", err)
		}
		if _, err := Uninstall(cfg); err != nil {
			t.Fatalf("uninstall: %v", err)
		}
		if got := read(t, path); got != src {
			t.Errorf("round trip is not the original:\n--- got ---\n%s--- want ---\n%s", got, src)
		}
	})

	t.Run("a user hook remains", func(t *testing.T) {
		const src = "hooks:\n  post_tool_call:\n    - command: \"/x\"\n"
		path := writeTemp(t, src)
		cfg := testConfig(path)
		if _, err := EnsureInstalled(cfg); err != nil {
			t.Fatalf("install: %v", err)
		}
		if _, err := Uninstall(cfg); err != nil {
			t.Fatalf("uninstall: %v", err)
		}
		if got := read(t, path); got != src {
			t.Errorf("round trip is not the original:\n--- got ---\n%s--- want ---\n%s", got, src)
		}
	})
}

// TestUninstall_LeavesAForeignFileAlone: a config we never wrote to must not be
// touched, and a file that does not exist must not be created.
func TestUninstall_LeavesAForeignFileAlone(t *testing.T) {
	const src = "hooks:\n  post_tool_call:\n    - command: \"/x\"\n"
	path := writeTemp(t, src)
	changed, err := Uninstall(testConfig(path))
	if err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if changed {
		t.Error("reported a change against a config with no region of ours")
	}
	if got := read(t, path); got != src {
		t.Errorf("the config was modified:\n%s", got)
	}

	missing := filepath.Join(t.TempDir(), "nope.yaml")
	if changed, err := Uninstall(testConfig(missing)); err != nil || changed {
		t.Errorf("Uninstall on a missing file = (%v, %v), want (false, nil)", changed, err)
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Error("Uninstall created the file it was asked to clean")
	}
}

// TestEnsureInstalled_HandlesTheEmptyFlowMapping pins the one inline value that
// is modelled — the spelling hermes' own DEFAULT_CONFIG carries — including
// that a trailing comment on that line rides along.
func TestEnsureInstalled_HandlesTheEmptyFlowMapping(t *testing.T) {
	const src = "model:\n  default: \"x\"\nhooks: {}   # none yet\nother: 1\n"
	path := writeTemp(t, src)
	if _, err := EnsureInstalled(testConfig(path)); err != nil {
		t.Fatalf("EnsureInstalled: %v", err)
	}
	got := read(t, path)
	if strings.Contains(got, "hooks: {}") {
		t.Errorf("the flow mapping was not replaced:\n%s", got)
	}
	if !strings.Contains(got, "hooks:   # none yet\n") {
		t.Errorf("the trailing comment was dropped:\n%s", got)
	}
	if !strings.Contains(got, "other: 1\n") {
		t.Errorf("the following key was lost:\n%s", got)
	}
}

// TestInspect_ReportsPresenceAndCanonicality is the read-only half every
// adapter's Verify sits on.
func TestInspect_ReportsPresenceAndCanonicality(t *testing.T) {
	path := writeTemp(t, "model:\n  default: \"x\"\n")
	cfg := testConfig(path)

	present, canonical, err := Inspect(cfg)
	if err != nil || present || canonical {
		t.Fatalf("before install: (%v, %v, %v), want (false, false, nil)", present, canonical, err)
	}
	if _, err := EnsureInstalled(cfg); err != nil {
		t.Fatalf("install: %v", err)
	}
	present, canonical, err = Inspect(cfg)
	if err != nil || !present || !canonical {
		t.Fatalf("after install: (%v, %v, %v), want (true, true, nil)", present, canonical, err)
	}

	stale := cfg
	stale.Entries = append([]Entry(nil), cfg.Entries...)
	stale.Entries[0].Command = "/bin/sh -c 'different hook-post x'"
	present, canonical, err = Inspect(stale)
	if err != nil || !present || canonical {
		t.Fatalf("against a changed command: (%v, %v, %v), want (true, false, nil)", present, canonical, err)
	}
}

// TestRefusals is the corpus of documents this package will not edit. Each row
// is a shape whose correct edit is not obvious, so guessing at one is how a
// user's config gets corrupted — and each must name its line, because #1362
// renders the message to the user.
//
// The `want` half of this table is the mutation evidence for the scanner: a
// scanner that stopped refusing would silently start editing one of these.
func TestRefusals(t *testing.T) {
	cases := []struct {
		name     string
		src      string
		wantLine int
		wantWord string
	}{
		{
			name:     "a tab in the indentation",
			src:      "model:\n\tdefault: x\n",
			wantLine: 2, wantWord: "tab",
		},
		{
			name:     "a document marker",
			src:      "---\nmodel: x\n",
			wantLine: 1, wantWord: "document marker",
		},
		{
			name:     "a second hooks key",
			src:      "hooks:\n  a:\n    - command: \"x\"\nmodel: y\nhooks:\n  b:\n    - command: \"z\"\n",
			wantLine: 5, wantWord: "second top-level",
		},
		{
			name:     "an inline value that is not {}",
			src:      "hooks: {on_session_end: []}\n",
			wantLine: 1, wantWord: "inline value",
		},
		{
			name:     "hooks is an alias",
			src:      "hooks: *shared\n",
			wantLine: 1, wantWord: "inline value",
		},
		{
			name:     "a root-level sequence",
			src:      "- one\n- two\n",
			wantLine: 1, wantWord: "not a key",
		},
		{
			name:     "the hooks block is a sequence",
			src:      "hooks:\n  - command: \"x\"\n",
			wantLine: 2, wantWord: "sequence, not a mapping",
		},
		{
			name:     "a merge key inside the block",
			src:      "hooks:\n  <<: *defaults\n",
			wantLine: 2, wantWord: "merge key",
		},
		{
			name:     "an event whose value is an anchor",
			src:      "hooks:\n  post_tool_call: &shared\n    - command: \"x\"\n",
			wantLine: 2, wantWord: "anchor or alias",
		},
		{
			name:     "an event whose value is a block scalar",
			src:      "hooks:\n  post_tool_call: |\n    not entries\n",
			wantLine: 2, wantWord: "block scalar",
		},
		{
			name:     "an unterminated region",
			src:      "hooks:\n  " + BeginMarker(testOwner) + "\n  a:\n    - command: \"x\"\n",
			wantLine: 2, wantWord: "no matching END",
		},
		{
			name:     "an END with no BEGIN",
			src:      "hooks:\n  " + EndMarker(testOwner) + "\n",
			wantLine: 2, wantWord: "no BEGIN",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeTemp(t, tc.src)
			_, err := EnsureInstalled(testConfig(path))
			if err == nil {
				t.Fatalf("EnsureInstalled accepted a document it cannot model:\n%s", tc.src)
			}
			var unsafe *UnsafeConstructError
			if !errors.As(err, &unsafe) {
				t.Fatalf("error is %T (%v), want *UnsafeConstructError", err, err)
			}
			if unsafe.Line != tc.wantLine {
				t.Errorf("reported line %d, want %d (%v)", unsafe.Line, tc.wantLine, err)
			}
			if !strings.Contains(unsafe.Message, tc.wantWord) {
				t.Errorf("message %q does not mention %q", unsafe.Message, tc.wantWord)
			}
			if got := read(t, path); got != tc.src {
				t.Errorf("a refused document was still modified:\n%s", got)
			}
		})
	}
}

// TestCollisionIsRefusedByName is separate from TestRefusals because the
// document is perfectly well formed: the conflict is semantic. A YAML mapping
// cannot hold the key twice and yaml.safe_load resolves a duplicate silently to
// the LAST one, so writing ours beside theirs would delete their hook with no
// error anywhere.
func TestCollisionIsRefusedByName(t *testing.T) {
	src := "hooks:\n  on_session_end:\n    - command: \"/theirs\"\n"
	path := writeTemp(t, src)

	_, err := EnsureInstalled(testConfig(path))
	var collision *CollisionError
	if !errors.As(err, &collision) {
		t.Fatalf("error is %T (%v), want *CollisionError", err, err)
	}
	if collision.Event != "on_session_end" {
		t.Errorf("Event = %q, want on_session_end", collision.Event)
	}
	if collision.Line != 2 {
		t.Errorf("Line = %d, want 2", collision.Line)
	}
	if got := read(t, path); got != src {
		t.Errorf("a refused install still modified the config:\n%s", got)
	}
}

// TestQuoteIsAYAMLDoubleQuotedScalar pins the escaping. The command embeds an
// absolute path the user chose, so a quote, a backslash, a leading `-` or a
// `#` must not be able to end the scalar or turn the line into a comment.
func TestQuoteIsAYAMLDoubleQuotedScalar(t *testing.T) {
	cases := map[string]string{
		`plain`:            `"plain"`,
		`has "quote"`:      `"has \"quote\""`,
		`back\slash`:       `"back\\slash"`,
		"tab\there":        `"tab\there"`,
		"nl\nhere":         `"nl\nhere"`,
		`# not a comment`:  `"# not a comment"`,
		"-leading":         `"-leading"`,
		"ctrl\x01char":     `"ctrl\x01char"`,
		"unicode ünïcødé":  `"unicode ünïcødé"`,
		`both "\ together`: `"both \"\\ together"`,
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

// TestRenderRegion_RefusesAnEmptyOrUnnamedRegion is the loud-failure rule: a
// caller that supplied nothing must not produce an empty marker pair that later
// reads as a healthy install.
func TestRenderRegion_RefusesAnEmptyOrUnnamedRegion(t *testing.T) {
	if _, err := renderRegion(Config{Owner: testOwner}, 2); err == nil {
		t.Error("rendered a region with no entries")
	}
	if _, err := renderRegion(Config{Entries: []Entry{{Event: "e", Command: "c"}}}, 2); err == nil {
		t.Error("rendered a region with no owner")
	}
	if _, err := renderRegion(Config{Owner: testOwner, Entries: []Entry{{Event: "not a key", Command: "c"}}}, 2); err == nil {
		t.Error("rendered a region with an event name that is not a plain YAML key")
	}
	if _, err := renderRegion(Config{Owner: testOwner, Entries: []Entry{{Event: "e"}}}, 2); err == nil {
		t.Error("rendered a region with an empty command")
	}
}
