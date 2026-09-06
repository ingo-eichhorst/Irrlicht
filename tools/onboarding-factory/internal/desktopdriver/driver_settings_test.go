package desktopdriver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A cell's `settings` block reaches the CLI driver as `claude --settings
// <path>`, a LAUNCH FLAG. Claude Desktop launches its own session and takes no
// such flag, so the equivalent is a settings file inside the run's own
// throwaway workspace.
//
// Measured live on 2026-09-06 before this existed, driving the real app with a
// hand-placed workspace file:
//
//	{"hooks":{"PreToolUse":[{"matcher":"Bash",...}]}}   → the hook FIRED
//	{"permissions":{"defaultMode":"plan"}}              → the turn refused to
//	                                                      write, 7x ExitPlanMode
//
// So Desktop honours workspace-scoped settings. What it did NOT have was any
// way for the harness to put them there: driver-desktop.sh has accepted a
// <settings-path> argument since it was written and never used it, which is
// why desktop_profile_validate_cell had to refuse the whole class — without
// the refusal a cell would have run WITHOUT its settings and reported a pass.

// settingsProbe records whether the workspace settings file existed at the
// moment Desktop was pointed at the workspace. Writing it after the deep link
// would be indistinguishable from not writing it at all, because Claude Code
// reads project settings when the session starts.
type settingsProbe struct {
	fakeRuntime
	settingsPath   string
	existedAtOpen  bool
	checkedAtOpen  bool
	contentsAtOpen string
}

func (runtime *settingsProbe) OpenComposer(ctx context.Context, workspace string) error {
	runtime.checkedAtOpen = true
	if data, err := os.ReadFile(runtime.settingsPath); err == nil {
		runtime.existedAtOpen = true
		runtime.contentsAtOpen = string(data)
	}
	return runtime.fakeRuntime.OpenComposer(ctx, workspace)
}

func settingsRequest(t *testing.T, settings string) (RunRequest, string) {
	t.Helper()
	workspace := t.TempDir()
	request := validRunRequest()
	request.Workspace = workspace
	if settings != "" {
		request.WorkspaceSettings = []byte(settings)
	}
	return request, filepath.Join(workspace, ".claude", "settings.json")
}

// RED-FIRST: before RunRequest carried WorkspaceSettings this failed with the
// file absent at open, because nothing wrote it.
func TestRunWritesWorkspaceSettingsBeforeOpeningDesktop(t *testing.T) {
	const blob = `{"permissions":{"defaultMode":"plan"}}`
	request, settingsPath := settingsRequest(t, blob)
	runtime := &settingsProbe{settingsPath: settingsPath}

	if _, err := Run(context.Background(), runtime, request); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !runtime.checkedAtOpen {
		t.Fatal("the probe never ran — Desktop was never pointed at the workspace, so this test proved nothing")
	}
	if !runtime.existedAtOpen {
		t.Fatal("workspace settings did not exist when Desktop opened the workspace; Claude Code reads project settings at session start, so a later write is the same as none")
	}
	if runtime.contentsAtOpen != blob {
		t.Errorf("workspace settings = %q, want the cell's blob verbatim %q", runtime.contentsAtOpen, blob)
	}
}

// LOCK (passes by construction): a cell with no settings block must leave the
// workspace exactly as it found it. run-cell.sh writes `null` into
// settings.json when a cell declares none, so "absent" has to mean absent.
func TestRunWithoutWorkspaceSettingsLeavesTheWorkspaceClean(t *testing.T) {
	request, settingsPath := settingsRequest(t, "")
	runtime := &settingsProbe{settingsPath: settingsPath}

	if _, err := Run(context.Background(), runtime, request); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := os.Stat(filepath.Dir(settingsPath)); !os.IsNotExist(err) {
		t.Fatalf("a .claude directory appeared in a workspace whose cell declared no settings (stat err = %v)", err)
	}
}

// `jq '.settings'` emits the four-byte literal `null` for a cell with no
// settings block, and `{}` for an empty one. Neither is a settings file, and
// writing either would hand Claude Code a project config the cell never asked
// for.
func TestRunTreatsEmptySettingsBlobsAsAbsent(t *testing.T) {
	for _, blob := range []string{"null", "{}", "  null\n", "", "   "} {
		request, settingsPath := settingsRequest(t, blob)
		runtime := &settingsProbe{settingsPath: settingsPath}
		if _, err := Run(context.Background(), runtime, request); err != nil {
			t.Fatalf("Run(%q) error = %v", blob, err)
		}
		if _, err := os.Stat(settingsPath); !os.IsNotExist(err) {
			t.Errorf("blob %q produced a settings file; it carries no settings", blob)
		}
	}
}

// A blob that is not a JSON object is an authoring error. Handing it to Claude
// Code unread would surface as an unexplained session failure rather than as
// the malformed cell it is.
func TestRunRejectsSettingsThatAreNotAJSONObject(t *testing.T) {
	for _, blob := range []string{`[1,2]`, `"text"`, `{`, `3`} {
		request, _ := settingsRequest(t, blob)
		runtime := &settingsProbe{}
		if _, err := Run(context.Background(), runtime, request); err == nil {
			t.Errorf("blob %q was accepted; it is not a settings object", blob)
		}
	}
}
