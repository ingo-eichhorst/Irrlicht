package claudecode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// memoryPathFor returns the CLAUDE.md path inside the temp HOME.
func memoryPathFor(home string) string {
	return filepath.Join(home, ".claude", "CLAUDE.md")
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestEnsureTaskEtaBlock_CreatesFileIfAbsent(t *testing.T) {
	home := withTempHome(t)
	modified, err := EnsureTaskEtaBlockInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("expected modified=true on first install")
	}
	content := readFileString(t, memoryPathFor(home))
	for _, want := range []string{taskEtaBeginSentinel, taskEtaEndSentinel, `"marker":"irrlicht-eta"`} {
		if !strings.Contains(content, want) {
			t.Errorf("installed file missing %q", want)
		}
	}
}

func TestEnsureTaskEtaBlock_Idempotent(t *testing.T) {
	home := withTempHome(t)
	if _, err := EnsureTaskEtaBlockInstalled(); err != nil {
		t.Fatal(err)
	}
	first := readFileString(t, memoryPathFor(home))

	modified, err := EnsureTaskEtaBlockInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if modified {
		t.Error("second install should be a no-op")
	}
	if got := readFileString(t, memoryPathFor(home)); got != first {
		t.Error("second install changed bytes")
	}
}

func TestEnsureTaskEtaBlock_PreservesSurroundingContent(t *testing.T) {
	home := withTempHome(t)
	path := memoryPathFor(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	userContent := "# My setup\n\nAlways use tabs.\n"
	if err := os.WriteFile(path, []byte(userContent), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureTaskEtaBlockInstalled(); err != nil {
		t.Fatal(err)
	}
	content := readFileString(t, path)
	if !strings.HasPrefix(content, "# My setup\n\nAlways use tabs.\n\n"+taskEtaBeginSentinel) {
		t.Errorf("user content not preserved with single blank-line separator:\n%s", content)
	}
}

func TestEnsureTaskEtaBlock_UpgradesStaleBlock(t *testing.T) {
	home := withTempHome(t)
	path := memoryPathFor(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	stale := "before\n\n" + taskEtaBeginSentinel + "\nold rule text\n" + taskEtaEndSentinel + "\n\nafter\n"
	if err := os.WriteFile(path, []byte(stale), 0o644); err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureTaskEtaBlockInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("stale block should be upgraded")
	}
	content := readFileString(t, path)
	if strings.Contains(content, "old rule text") {
		t.Error("stale block content survived the upgrade")
	}
	if !strings.HasPrefix(content, "before\n\n") || !strings.HasSuffix(content, "\n\nafter\n") {
		t.Errorf("surrounding bytes changed:\n%s", content)
	}
	if !strings.Contains(content, managedTaskEtaBlock) {
		t.Error("upgraded block is not the canonical content")
	}

	// And the upgrade itself is idempotent.
	if again, err := EnsureTaskEtaBlockInstalled(); err != nil || again {
		t.Errorf("re-install after upgrade: modified=%v err=%v, want false,nil", again, err)
	}
}

// assertCurrentEtaContract checks the phrases every shipped-block upgrade
// must land on: first marker on the Bash description carrier
// (anthropics/claude-code#65620 — pre-tool-call prose is lossy upstream),
// the Bash description carrier as the mandatory per-phase update channel
// (#617), and the command-field prohibition (permission matching).
func assertCurrentEtaContract(t *testing.T, content string) {
	t.Helper()
	if !strings.Contains(content, "`description` of your first Bash call") {
		t.Error("block must put the first marker on the Bash description carrier")
	}
	if !strings.Contains(content, "After each phase you complete") {
		t.Error("block must make the per-phase update mandatory")
	}
	if !strings.Contains(content, "`description` of the next Bash call") {
		t.Error("block must name the Bash description carrier as the update channel")
	}
	if !strings.Contains(content, "never to the command itself") {
		t.Error("block must forbid the command field (permission matching)")
	}
}

func TestEnsureTaskEtaBlock_V1BlockUpgrades(t *testing.T) {
	// The exact v1 block (shipped with #558) must upgrade in place to the
	// current contract.
	home := withTempHome(t)
	path := memoryPathFor(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	v1 := taskEtaBeginSentinel + `
## Task progress markers (managed by Irrlicht)

As you work on a multi-step task, periodically emit a hidden progress marker
so tools can show a task-completion estimate. Emit it as an HTML comment in
your response text, and update it as you make progress:

` + "```" + `
<!-- {"marker":"irrlicht-eta","total_rounds":N,"completed_rounds":M} -->
` + "```" + `

` + "`total_rounds`" + ` is your estimate of the task's phases; ` + "`completed_rounds`" + `
is how many you've finished. Update every few steps.
` + taskEtaEndSentinel
	if err := os.WriteFile(path, []byte(v1+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureTaskEtaBlockInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("v1 block should upgrade in place")
	}
	assertCurrentEtaContract(t, readFileString(t, path))
}

func TestEnsureTaskEtaBlock_V2BlockUpgradesToV3(t *testing.T) {
	// The exact v2 block (shipped with #604/#602) must upgrade in place to
	// the v3 contract (#617): the description carrier becomes the mandatory
	// per-phase update channel instead of an optional one — v2's "you may
	// also" under-bound in prose-less sessions, pinning the chip at
	// "estimating…" (session ad880389).
	home := withTempHome(t)
	path := memoryPathFor(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	v2 := taskEtaBeginSentinel + `
## Task progress markers (managed by Irrlicht)

As you work on a multi-step task, periodically emit a hidden progress marker
so tools can show a task-completion estimate. Emit it as an HTML comment in
your response text, and update it as you make progress:

` + "```" + `
<!-- {"marker":"irrlicht-eta","total_rounds":N,"completed_rounds":M} -->
` + "```" + `

` + "`total_rounds`" + ` is your estimate of the task's phases; ` + "`completed_rounds`" + `
is how many you've finished. Emit the first marker in your first response,
right before your first tool call. Update every few steps; you may also
append the marker to the ` + "`description`" + ` of a Bash call you are
already making (never to the command itself).
` + taskEtaEndSentinel
	if err := os.WriteFile(path, []byte(v2+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureTaskEtaBlockInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("v2 block should upgrade to v3")
	}
	content := readFileString(t, path)
	if strings.Contains(content, "you may also") {
		t.Error("v2's permissive carrier phrasing survived the upgrade")
	}
	assertCurrentEtaContract(t, content)
}

func TestEnsureTaskEtaBlock_V3BlockUpgradesToV4(t *testing.T) {
	// The exact v3 block (shipped with #617) must upgrade in place to the v4
	// contract (anthropics/claude-code#65620): the first marker moves from
	// pre-tool-call response text — the shape upstream loses since
	// ~2026-06-04 — onto the Bash description carrier, which reaches the
	// daemon via the PreToolUse hook regardless of text-block fate.
	home := withTempHome(t)
	path := memoryPathFor(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	v3 := taskEtaBeginSentinel + `
## Task progress markers (managed by Irrlicht)

As you work on a multi-step task, periodically emit a hidden progress marker
so tools can show a task-completion estimate. Emit it as an HTML comment in
your response text, and update it as you make progress:

` + "```" + `
<!-- {"marker":"irrlicht-eta","total_rounds":N,"completed_rounds":M} -->
` + "```" + `

` + "`total_rounds`" + ` is your estimate of the task's phases; ` + "`completed_rounds`" + `
is how many you've finished. Emit the first marker in your first response,
right before your first tool call. After each phase you complete, emit
the updated marker: append it to the ` + "`description`" + ` of the next Bash call
you make (never to the command itself), or include it in your response
text when no Bash call is coming.
` + taskEtaEndSentinel
	if err := os.WriteFile(path, []byte(v3+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	modified, err := EnsureTaskEtaBlockInstalled()
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("v3 block should upgrade to v4")
	}
	content := readFileString(t, path)
	if strings.Contains(content, "first marker in your first response") {
		t.Error("v3's pre-tool-call first-marker phrasing survived the upgrade")
	}
	assertCurrentEtaContract(t, content)
}

func TestPatchManagedBlock_AppendAddsSingleBlankLine(t *testing.T) {
	for _, existing := range []string{"content", "content\n", "content\n\n\n"} {
		got, changed := patchManagedBlock(existing, managedTaskEtaBlock)
		if !changed {
			t.Fatalf("append on %q should report changed", existing)
		}
		want := "content\n\n" + managedTaskEtaBlock + "\n"
		if got != want {
			t.Errorf("patch(%q) =\n%q\nwant\n%q", existing, got, want)
		}
	}
}

func TestPatchManagedBlock_NestedMarkerCommentNotMisparsed(t *testing.T) {
	// The block contains the marker example — itself an HTML comment. A
	// second patch must key on the full sentinels and not mistake the inner
	// comment for a block boundary.
	once, _ := patchManagedBlock("", managedTaskEtaBlock)
	twice, changed := patchManagedBlock(once, managedTaskEtaBlock)
	if changed {
		t.Error("re-patch should be a no-op")
	}
	if twice != once {
		t.Errorf("re-patch changed bytes:\n%q\nvs\n%q", twice, once)
	}
}

func TestUninstallTaskEtaBlock_RoundTripsToOriginal(t *testing.T) {
	home := withTempHome(t)
	path := memoryPathFor(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	original := "# Mine\n\nkeep this\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureTaskEtaBlockInstalled(); err != nil {
		t.Fatal(err)
	}
	modified, err := UninstallTaskEtaBlock()
	if err != nil {
		t.Fatal(err)
	}
	if !modified {
		t.Fatal("uninstall should report modified")
	}
	if got := readFileString(t, path); got != original {
		t.Errorf("round-trip mismatch:\n%q\nwant\n%q", got, original)
	}
}

func TestUninstallTaskEtaBlock_PreservesUserContentAroundBlock(t *testing.T) {
	home := withTempHome(t)
	path := memoryPathFor(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	seeded := "before\n\n" + managedTaskEtaBlock + "\n\nafter\n"
	if err := os.WriteFile(path, []byte(seeded), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := UninstallTaskEtaBlock(); err != nil {
		t.Fatal(err)
	}
	got := readFileString(t, path)
	if got != "before\n\nafter\n" {
		t.Errorf("got %q, want user content joined by one blank line", got)
	}
}

func TestUninstallTaskEtaBlock_Noops(t *testing.T) {
	home := withTempHome(t)

	// No file at all.
	modified, err := UninstallTaskEtaBlock()
	if err != nil || modified {
		t.Errorf("no file: modified=%v err=%v, want false,nil", modified, err)
	}
	if _, err := os.Stat(memoryPathFor(home)); !os.IsNotExist(err) {
		t.Error("uninstall must not create the file")
	}

	// File without our block.
	path := memoryPathFor(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("just user stuff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	modified, err = UninstallTaskEtaBlock()
	if err != nil || modified {
		t.Errorf("no block: modified=%v err=%v, want false,nil", modified, err)
	}
	if got := readFileString(t, path); got != "just user stuff\n" {
		t.Errorf("file changed: %q", got)
	}
}

func TestRemoveManagedBlock_HalfBlockIsNoop(t *testing.T) {
	for name, content := range map[string]string{
		"begin only":   "x\n" + taskEtaBeginSentinel + "\ny\n",
		"end only":     "x\n" + taskEtaEndSentinel + "\ny\n",
		"out of order": taskEtaEndSentinel + "\n" + taskEtaBeginSentinel + "\n",
	} {
		got, changed := removeManagedBlock(content)
		if changed || got != content {
			t.Errorf("%s: removeManagedBlock should never touch a half-block (changed=%v)", name, changed)
		}
	}
}

func TestPatchManagedBlock_BeginWithoutEndAppendsFresh(t *testing.T) {
	damaged := "x\n" + taskEtaBeginSentinel + "\ndangling\n"
	got, changed := patchManagedBlock(damaged, managedTaskEtaBlock)
	if !changed {
		t.Fatal("expected a fresh block appended")
	}
	if !strings.Contains(got, damaged[:len(damaged)-1]) {
		t.Error("damaged remnant must be preserved, not guessed at")
	}
	if !strings.HasSuffix(got, managedTaskEtaBlock+"\n") {
		t.Error("fresh well-formed block should be appended at the end")
	}
}
