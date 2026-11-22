// hookvariant_test.go covers the per-version hook artifact (#2171). Vibe
// changed its hook schema at 2.21.0. From 2.21.0 on, the hook type users
// write is `post_agent`, not `post_agent_turn`, and enable_experimental_hooks
// is a setting Vibe no longer reads. Source for both: the hook reference
// Vibe bundles in vibe/core/skills/builtins/vibe.py, read in the 2.20.0 and
// 2.21.0 wheels during triage. The 3.1.4 and 2.21.0 TUIs rejected the old
// entry when started in this ticket's manual check.
//
// Every test here drives the declaration the daemon consumes, Agent(), the
// way PermissionService does: the variant SelectVariant picks for an
// installed version, else the permission's own Apply/Verify. That is what
// let these tests run red against the single-artifact declaration this
// ticket replaced.
package vibe

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"irrlicht/core/domain/agent"
	"irrlicht/core/internal/contracttesting"
)

// hooksDeclaration returns the real hooks permission.
func hooksDeclaration(t *testing.T) agent.Permission {
	t.Helper()
	for _, p := range Agent().Permissions {
		if p.Key == PermissionKeyHooks {
			if p.Writes == nil {
				t.Fatal("hooks permission declares no Writes")
			}
			return p
		}
	}
	t.Fatal("no hooks permission")
	return agent.Permission{}
}

// applyAt runs what PermissionService runs on a grant at the installed
// version: the selected variant's Apply, or the permission's own Apply for a
// single-artifact declaration.
func applyAt(t *testing.T, version string) {
	t.Helper()
	p := hooksDeclaration(t)
	v, bound, why := p.Writes.SelectVariant(version)
	var err error
	switch {
	case v != nil:
		err = v.Apply()
	case bound == agent.WithinBounds:
		err = p.Apply()
	default:
		t.Fatalf("Vibe %s: install refused: %s", version, why)
	}
	if err != nil {
		t.Fatalf("Vibe %s: apply: %v", version, err)
	}
}

// verifyAt runs what the #1372 re-verification runs at the installed
// version: the selected variant's Verify, or Writes.Verify.
func verifyAt(t *testing.T, version string) agent.HookEntryStatus {
	t.Helper()
	p := hooksDeclaration(t)
	v, bound, why := p.Writes.SelectVariant(version)
	verify := p.Writes.Verify
	switch {
	case v != nil:
		verify = v.Verify
	case bound != agent.WithinBounds:
		t.Fatalf("Vibe %s: refused: %s", version, why)
	}
	status, err := verify()
	if err != nil {
		t.Fatalf("Vibe %s: verify: %v", version, err)
	}
	return status
}

func readFileOrEmpty(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func vibeFiles(t *testing.T) (hooksPath, configPath string) {
	t.Helper()
	hp, err := hooksTomlPath()
	if err != nil {
		t.Fatal(err)
	}
	cp, err := vibeConfigTomlPath()
	if err != nil {
		t.Fatal(err)
	}
	return hp, cp
}

// assertCurrentHooks fails unless hooks.toml holds the current artifact's
// type line and no post_agent_turn.
func assertCurrentHooks(t *testing.T, hooksPath string) {
	t.Helper()
	if hooks := readFileOrEmpty(t, hooksPath); !strings.Contains(hooks, typeLineCurrent) || strings.Contains(hooks, "post_agent_turn") {
		t.Errorf("hooks.toml = %q, want an entry with %q and no post_agent_turn", hooks, typeLineCurrent)
	}
}

func isolatedVibeHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv(vibeHomeEnvVar, "")
}

const (
	flagKey         = "enable_experimental_hooks"
	typeLineCurrent = `type = "post_agent"` + "\n"
	typeLineLegacy  = `type = "post_agent_turn"` + "\n"
)

// TestCurrentVibe_InstallWritesPostAgentAndNoFlag is a defect test (#2171):
// on Vibe 2.21.0 and newer the install must write the type Vibe accepts and
// no enable_experimental_hooks key. Seen red before the fix: the
// single-artifact install wrote `type = "post_agent_turn"` and the flag at
// every version.
func TestCurrentVibe_InstallWritesPostAgentAndNoFlag(t *testing.T) {
	for _, version := range []string{"2.21.0", "3.1.4"} {
		t.Run(version, func(t *testing.T) {
			isolatedVibeHome(t)
			applyAt(t, version)
			hooksPath, configPath := vibeFiles(t)

			assertCurrentHooks(t, hooksPath)
			if cfg := readFileOrEmpty(t, configPath); strings.Contains(cfg, flagKey) {
				t.Errorf("config.toml = %q, want no %s key: Vibe %s does not read it", cfg, flagKey, version)
			}
			if status := verifyAt(t, version); !status.Intact() {
				t.Errorf("Verify right after install: %s", status.Damage())
			}
		})
	}
}

// TestLegacyVibe_InstallIsUnchanged is a LOCK: below 2.21.0 the install
// still writes today's artifact, post_agent_turn plus the flag. It passed
// before the change by construction.
func TestLegacyVibe_InstallIsUnchanged(t *testing.T) {
	for _, version := range []string{"2.19.1", "2.20.0"} {
		t.Run(version, func(t *testing.T) {
			isolatedVibeHome(t)
			applyAt(t, version)
			hooksPath, configPath := vibeFiles(t)
			if hooks := readFileOrEmpty(t, hooksPath); !strings.Contains(hooks, typeLineLegacy) {
				t.Errorf("hooks.toml = %q, want %q", hooks, typeLineLegacy)
			}
			if cfg := readFileOrEmpty(t, configPath); cfg != flagKey+" = true\n" {
				t.Errorf("config.toml = %q, want exactly the flag set true", cfg)
			}
			if status := verifyAt(t, version); !status.Intact() {
				t.Errorf("Verify right after install: %s", status.Damage())
			}
		})
	}
}

// TestUpgrade_LegacyInstallOnARealConfigIsRewritten is a defect test
// (#2171): an install a 2.19.1 Vibe got, in a config.toml Vibe itself wrote,
// must read Stale once Vibe is 2.21.0 or newer, and the repair must rewrite
// the entry's type and delete the flag line while leaving every other byte of
// config.toml unchanged. Seen red before the fix: Verify read the old install
// as intact at every version, so nothing would ever have rewritten it.
func TestUpgrade_LegacyInstallOnARealConfigIsRewritten(t *testing.T) {
	_, configPath := seedRealConfig(t)
	hooksPath, _ := vibeFiles(t)
	original := readFileOrEmpty(t, configPath)
	if strings.Contains(original, flagKey) {
		t.Fatal("fixture already carries the flag; this test needs it absent before the legacy install")
	}

	applyAt(t, "2.19.1")
	if status := verifyAt(t, "2.19.1"); !status.Intact() {
		t.Fatalf("legacy install not intact under 2.19.1: %s", status.Damage())
	}

	status := verifyAt(t, "3.1.4")
	if len(status.Stale) == 0 {
		t.Fatalf("a post_agent_turn install under Vibe 3.1.4: %+v, want Stale so #1372 rewrites it", status)
	}

	applyAt(t, "3.1.4")
	if got := readFileOrEmpty(t, configPath); got != original {
		t.Errorf("config.toml after the upgrade differs from the file Vibe wrote; want only the flag line removed.\n--- got\n%s", got)
	}
	assertCurrentHooks(t, hooksPath)
	if n := strings.Count(readFileOrEmpty(t, hooksPath), "[[hooks]]"); n != 1 {
		t.Errorf("hooks.toml holds %d [[hooks]] blocks after the upgrade, want 1 (rewritten in place)", n)
	}
	if status := verifyAt(t, "3.1.4"); !status.Intact() {
		t.Errorf("after the upgrade repair: %s", status.Damage())
	}
	if status := verifyAt(t, "2.19.1"); status.Intact() {
		t.Error("the current artifact also reads intact under 2.19.1; the two variants' Verify must never both read intact")
	}

	// A downgrade is the same repair in the other direction.
	applyAt(t, "2.20.0")
	if status := verifyAt(t, "2.20.0"); !status.Intact() {
		t.Errorf("after the downgrade repair: %s", status.Damage())
	}
}

// TestCurrentVibe_AFlagLineAtAnyValueReadsStale is a defect test (#2171).
// Vibe 3.1.4 warns about the key whatever its value, so a leftover
// `= false` (what an uninstall used to write) is not an intact install
// either. Seen red before the fix.
func TestCurrentVibe_AFlagLineAtAnyValueReadsStale(t *testing.T) {
	isolatedVibeHome(t)
	applyAt(t, "3.1.4")
	_, configPath := vibeFiles(t)
	writeConfig(t, filepath.Dir(configPath), "active_model = \"local\"\n"+flagKey+" = false\n")
	if status := verifyAt(t, "3.1.4"); len(status.Stale) == 0 {
		t.Fatalf("an install with `%s = false` under 3.1.4: %+v, want Stale", flagKey, status)
	}
	applyAt(t, "3.1.4")
	if got := readFileOrEmpty(t, configPath); got != "active_model = \"local\"\n" {
		t.Errorf("config.toml = %q, want the flag line gone and the rest untouched", got)
	}
}

// TestCurrentVibe_AnUnscannableConfigWithoutTheFlagInstallsCleanly is a
// defect test from #2171's review: a config.toml holding a construct hooktoml
// refuses to scan (a triple-quoted string), but no flag, needs no edit at all,
// so the current install must succeed and Verify must read it intact. Seen red
// before the fix: Apply wrote the entry and then reported a refusal for a
// delete it never needed, and Verify errored on every pass.
func TestCurrentVibe_AnUnscannableConfigWithoutTheFlagInstallsCleanly(t *testing.T) {
	seed := "active_model = \"local\"\nnote = \"\"\"\nmulti\n\"\"\"\n"
	setVibeHome(t, seed)
	_, configPath := vibeFiles(t)
	applyAt(t, "3.1.4")
	if status := verifyAt(t, "3.1.4"); !status.Intact() {
		t.Errorf("Verify: %s", status.Damage())
	}
	if got := readFileOrEmpty(t, configPath); got != seed {
		t.Errorf("config.toml changed: %q", got)
	}
}

// TestLegacyVibe_ACurrentArtifactReadsStale covers the reordered legacy
// Verify (#2171 review finding): after a downgrade, the current artifact (a
// post_agent block and no flag) must read Stale under a legacy-selected
// version, as agent.HookVariant.Verify's doc requires of another variant's
// file, not Missing. It covers changed code rather than a defect, so its proof
// is the mutation in tools/lib/vibe-hook-variants-mutations_test.sh that puts
// the old `!present || !flagOn` arm first.
func TestLegacyVibe_ACurrentArtifactReadsStale(t *testing.T) {
	isolatedVibeHome(t)
	applyAt(t, "3.1.4")
	status := verifyAt(t, "2.19.1")
	if len(status.Stale) == 0 || len(status.Missing) != 0 {
		t.Errorf("the current artifact under 2.19.1: %+v, want Stale only", status)
	}
}

// TestUninstall_RemovesTheFlagLineRatherThanWritingFalse is a defect test
// (#2171, a deviation from the triage design recorded in the PR): when no
// hook remains, uninstall deletes the key instead of setting it false.
// Absent and false read the same on 2.19.1 (`Field(default=False)`), and
// Vibe 3.1.4 warns about the key at any value. Seen red before the fix: the
// uninstall wrote `enable_experimental_hooks = false`.
func TestUninstall_RemovesTheFlagLineRatherThanWritingFalse(t *testing.T) {
	isolatedVibeHome(t)
	applyAt(t, "2.19.1")
	if _, err := UninstallHooks(); err != nil {
		t.Fatalf("uninstall: %v", err)
	}
	_, configPath := vibeFiles(t)
	if cfg := readFileOrEmpty(t, configPath); strings.Contains(cfg, flagKey) {
		t.Errorf("config.toml after uninstall = %q, want no %s line", cfg, flagKey)
	}
}

// TestPostAgentHook_DispatchesToHandleStopHook is a defect test (#2171): a
// `post_agent` body in the shape Vibe 2.21.0 sends reaches HandleStopHook
// exactly as post_agent_turn does. The shape is source-read from 2.21.0's
// vibe/core/agent_loop_hooks.py, whose _hook_session_context sets
// transcript_path to the session's messages.jsonl, not live-fired. Seen red
// before the fix: the receiver dropped `post_agent` as an unknown event.
func TestPostAgentHook_DispatchesToHandleStopHook(t *testing.T) {
	root := vibeSessionRoot(t)
	tp := writeSessionTranscript(t, root, "sess-post-agent")
	h, target := newReceiver(t)

	rec := post(t, h, contractPayload(tp, "post_agent"))

	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	stops := target.stops()
	if len(stops) != 1 {
		t.Fatalf("HandleStopHook called %d times for post_agent, want 1", len(stops))
	}
	if got := stops[0]; got.sessionID != "sess-post-agent" || got.transcriptPath != tp ||
		got.lastAssistantText != "" || got.waitingCue {
		t.Errorf("dispatch = %+v, want session sess-post-agent, the confined path, no text and no cue", got)
	}
}

// TestPostAgentHook_A3xSessionDirectoryIsRefusedNotDispatched is a LOCK on
// what the receiver does with the body Vibe 3.1.4 sends, which this ticket
// does NOT make work. It passed before the change too, because the path is
// refused before the event name is read.
//
// Source-read in the 3.1.4 wheel, not live-fired:
// mistralai_vibe_local_harness/vibe/_foreign_hooks.py's _base_payload sets
// transcript_path to the session's storage ROOT, a directory, and
// app_server/_unified_harness_backend_adapter.py's _session_dir names it
// <save_dir>/unified/<session_id>. The payload also carries response_text,
// agent_name and turn_id. 3.1.4 writes no session messages.jsonl: its
// session code only reads one, for legacy import, and the one file of that
// name it writes is the ACP protocol log under ~/.vibe/logs/acp, outside the
// session root. So this adapter's transcript Source does not see a 3.x
// session either. That gap is out of this ticket's scope.
//
// What it pins: 200, no dispatch, and one error log line per POST — so a
// 3.x user's hook costs one log line per turn, not a retry storm.
func TestPostAgentHook_A3xSessionDirectoryIsRefusedNotDispatched(t *testing.T) {
	root := vibeSessionRoot(t)
	sessionDir := filepath.Join(root, "unified", "4f1c2d9e-0000-4000-8000-000000000000")
	if err := os.MkdirAll(sessionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := &mockTarget{}
	log := &contracttesting.RecordingLogger{}
	h := NewHookHandler(target, keyedGate{PermissionKeyHooks: true, PermissionKeyTranscripts: true}, log)

	raw, err := json.Marshal(map[string]any{
		"hook_event_name": "post_agent", "session_id": "4f1c2d9e-0000-4000-8000-000000000000",
		"transcript_path": sessionDir, "cwd": "/tmp", "parent_session_id": nil,
		"agent_name": "default", "turn_id": "turn-1", "response_text": "done",
	})
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	for i := 1; i <= 2; i++ {
		rec := post(t, h, body)
		if rec.Code != http.StatusOK {
			t.Errorf("POST %d: status = %d, want 200", i, rec.Code)
		}
		if n := len(log.Errors()); n != i {
			t.Errorf("after POST %d: %d error log line(s), want %d (one per refused body): %v", i, n, i, log.Errors())
		}
	}
	if n := target.totalCalls(); n != 0 {
		t.Errorf("dispatched %d times for a 3.x session directory, want 0", n)
	}
}

// TestObservedReadsTheUvToolVersion is a defect test (#2171): the daemon
// learns the installed Vibe version from the name of uv's dist-info folder,
// without running anything, because a launchd daemon's PATH does not reach
// ~/.local/bin where `uv tool install` puts `vibe`. Seen red before the fix:
// the gate declared no Observed source at all.
//
// The resolution order is uv's own, measured with uv 0.8.19 on this machine
// (`uv tool dir` under each override): UV_TOOL_DIR wins, then
// $XDG_DATA_HOME/uv/tools, then ~/.local/share/uv/tools; a relative
// XDG_DATA_HOME is ignored by uv, and a relative UV_TOOL_DIR is resolved
// against uv's own working directory, which the daemon cannot know.
func TestObservedReadsTheUvToolVersion(t *testing.T) {
	observed := hooksDeclaration(t).Writes.Version.Observed
	if observed == nil {
		t.Fatal("the hooks permission's version gate declares no Observed source")
	}
	distInfo := func(t *testing.T, toolDir, version string) {
		t.Helper()
		dir := filepath.Join(toolDir, "mistral-vibe", "lib", "python3.12", "site-packages", "mistral_vibe-"+version+".dist-info")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	isolated := func(t *testing.T) string {
		t.Helper()
		home := t.TempDir()
		t.Setenv("HOME", home)
		t.Setenv("UV_TOOL_DIR", "")
		t.Setenv("XDG_DATA_HOME", "")
		return home
	}

	t.Run("default tool dir", func(t *testing.T) {
		home := isolated(t)
		distInfo(t, filepath.Join(home, ".local", "share", "uv", "tools"), "3.1.4")
		if got := observed(); got != "3.1.4" {
			t.Errorf("Observed() = %q, want 3.1.4", got)
		}
	})
	t.Run("nothing installed", func(t *testing.T) {
		isolated(t)
		if got := observed(); got != "" {
			t.Errorf("Observed() = %q with no uv tool tree, want empty so the probe runs", got)
		}
	})
	t.Run("XDG_DATA_HOME", func(t *testing.T) {
		home := isolated(t)
		distInfo(t, filepath.Join(home, ".local", "share", "uv", "tools"), "2.19.1")
		xdg := t.TempDir()
		t.Setenv("XDG_DATA_HOME", xdg)
		distInfo(t, filepath.Join(xdg, "uv", "tools"), "2.21.0")
		if got := observed(); got != "2.21.0" {
			t.Errorf("Observed() = %q, want the XDG_DATA_HOME tree's 2.21.0", got)
		}
	})
	t.Run("relative XDG_DATA_HOME is ignored", func(t *testing.T) {
		home := isolated(t)
		distInfo(t, filepath.Join(home, ".local", "share", "uv", "tools"), "2.19.1")
		t.Setenv("XDG_DATA_HOME", "relative/data")
		if got := observed(); got != "2.19.1" {
			t.Errorf("Observed() = %q, want the default tree's 2.19.1", got)
		}
	})
	t.Run("UV_TOOL_DIR wins", func(t *testing.T) {
		home := isolated(t)
		distInfo(t, filepath.Join(home, ".local", "share", "uv", "tools"), "2.19.1")
		tools := t.TempDir()
		t.Setenv("UV_TOOL_DIR", tools)
		distInfo(t, tools, "3.0.0")
		if got := observed(); got != "3.0.0" {
			t.Errorf("Observed() = %q, want UV_TOOL_DIR's 3.0.0", got)
		}
	})
	t.Run("relative UV_TOOL_DIR reads nothing", func(t *testing.T) {
		home := isolated(t)
		distInfo(t, filepath.Join(home, ".local", "share", "uv", "tools"), "2.19.1")
		t.Setenv("UV_TOOL_DIR", "relative/tools")
		if got := observed(); got != "" {
			t.Errorf("Observed() = %q, want empty: uv resolved that against a directory the daemon cannot know", got)
		}
	})
	t.Run("an unparseable folder name is skipped", func(t *testing.T) {
		home := isolated(t)
		distInfo(t, filepath.Join(home, ".local", "share", "uv", "tools"), "garbage")
		if got := observed(); got != "" {
			t.Errorf("Observed() = %q, want empty", got)
		}
	})
}

// TestCurrentVibe_RoundTrip covers code this ticket added rather than a
// defect, so it has no red-before-the-fix run: the current artifact installs
// idempotently, verifies intact, and the one Uninstall rule removes its
// post_agent block as it removes a legacy one (both carry the sentinel).
func TestCurrentVibe_RoundTrip(t *testing.T) {
	isolatedVibeHome(t)
	hooksPath, configPath := vibeFiles(t)

	if modified, err := EnsureHooksInstalledCurrent(); err != nil || !modified {
		t.Fatalf("first install: modified=%v err=%v, want true/nil", modified, err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Errorf("the current install created config.toml (stat err %v); it only ever deletes a line there", err)
	}
	if status, err := VerifyHooksInstalledCurrent(); err != nil || !status.Intact() {
		t.Fatalf("verify after install: %+v err=%v", status, err)
	}
	if modified, err := EnsureHooksInstalledCurrent(); err != nil || modified {
		t.Errorf("second install: modified=%v err=%v, want false/nil", modified, err)
	}
	if modified, err := UninstallHooks(); err != nil || !modified {
		t.Fatalf("uninstall: modified=%v err=%v, want true/nil", modified, err)
	}
	if hooks := readFileOrEmpty(t, hooksPath); strings.Contains(hooks, "[[hooks]]") {
		t.Errorf("hooks.toml after uninstall = %q, want no block", hooks)
	}
	if status, err := VerifyHooksInstalledCurrent(); err != nil || len(status.Missing) == 0 {
		t.Errorf("verify after uninstall: %+v err=%v, want Missing", status, err)
	}
}
