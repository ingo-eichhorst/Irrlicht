package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"irrlicht/core/adapters/inbound/agents/claudecode"
	"irrlicht/core/adapters/outbound/filesystem"
	"irrlicht/core/domain/permission"
)

// userProseInCLAUDEMd is content the USER wrote in their own ~/.claude/CLAUDE.md,
// seeded around irrlicht's managed blocks.
//
// It is here because this file is the one managed target that is not ours.
// --uninstall-hooks edits agent config files irrlicht co-owns; this command
// edits a document a human writes prose in, so "we removed our blocks" and "we
// removed only our blocks" are two different claims and the second one is the
// expensive one to get wrong. Deliberately shaped like the hazard: a heading
// and a fenced code block, so a remover that keyed on a generic `<!--` scan or
// on markdown structure rather than on the full sentinels would take it.
const userProseInCLAUDEMd = `# My own instructions

Always run the linter before committing.

` + "```" + `
<!-- this looks like a marker but is mine -->
` + "```" + `

Never touch the vendor directory.
`

// TestUninstallTaskEtaAgainstLiveDaemon is #1437's end-to-end regression test,
// the sibling of TestUninstallHooksAgainstLiveDaemon: `irrlichd
// --uninstall-task-eta`, run by hand in a SECOND process, must stick — both
// against the daemon that is running right now and across the next start.
//
// Before the fix it did not. uninstallTaskEtaBlocks called
// claudecode.UninstallInstructionBlocks() and nothing else: the blocks came out
// of ~/.claude/CLAUDE.md while claude-code/instructions stayed "granted" in
// permissions.json, and PermissionService.Start re-runs Apply for every granted
// permission at boot — so the next daemon start wrote them straight back. The
// user ran a command whose entire purpose is "remove these" and they came back.
//
// Two processes and two daemon lifetimes on purpose. A single-process test
// cannot express "the CLI and the daemon disagree", and a single-lifetime test
// cannot reach the re-apply, which for the instructions permission happens at
// START rather than on #1372's re-verification timer.
//
// Hermetic on the same terms as TestDaemonStartupSmoke: HOME and IRRLICHT_HOME
// are fresh temp dirs and every child runs under sanitizedChildEnv, so it never
// reads or writes the real ~/.claude/CLAUDE.md and never reaches a daemon on
// :7837 (#1417).
func TestUninstallTaskEtaAgainstLiveDaemon(t *testing.T) {
	bin := buildIrrlichd(t)

	homeDir := t.TempDir()
	stateDir := shortTempDir(t)
	seedGrantedInstructionsConsent(t, stateDir)
	claudeMd := seedUserAuthoredCLAUDEMd(t, homeDir)

	d := bootSmokeDaemonIn(t, bin, homeDir, stateDir)

	// Precondition, and the vacuity guard: a granted permission means the boot
	// Apply actually wrote the blocks. Without it every assertion below would
	// pass against a daemon that never installed anything.
	if !pollUntil(t, 15*time.Second, 25*time.Millisecond, func() bool { return instructionBlocksPresent(claudeMd) }) {
		t.Fatalf("the daemon never installed the managed instruction blocks into %s at boot", claudeMd)
	}

	// The defect's trigger: the flag, by hand, in a second process, against the
	// live daemon.
	runUninstallTaskEta(t, bin, homeDir, stateDir)

	if instructionBlocksPresent(claudeMd) {
		t.Fatalf("--uninstall-task-eta left managed blocks in %s:\n%s", claudeMd, readFileForMessage(claudeMd))
	}

	// The mechanism against the RUNNING daemon: it must have reloaded consent
	// from the store, exactly as #1429's nudge makes --uninstall-hooks do.
	// Separate from the restart assertion below — if only this fails the nudge
	// did not land; if only the restart one fails the denial was never written.
	if got := livePermissionState(t, d.addr, claudecode.AdapterName, claudecode.PermissionKeyInstructions); got != "denied" {
		t.Errorf("running daemon reports %s/%s = %q after --uninstall-task-eta, want \"denied\" — "+
			"the daemon never reloaded consent from the store",
			claudecode.AdapterName, claudecode.PermissionKeyInstructions, got)
	}

	d.shutdown(t)

	// THIS is the assertion the issue is about: advance past a re-apply. For
	// this permission the re-apply is a daemon start, so start one.
	d2 := bootSmokeDaemonIn(t, bin, homeDir, stateDir)
	defer d2.shutdown(t)

	// Give the boot effects a window to run. The vacuity guard above measured
	// how long a granted boot install takes on this machine, so a fixed wait
	// here is not a guess about an unobservable: the same work either happens
	// or is skipped, and pollUntil returning false IS the pass.
	if pollUntil(t, 5*time.Second, 25*time.Millisecond, func() bool { return instructionBlocksPresent(claudeMd) }) {
		t.Fatalf("the restarted daemon re-installed the managed instruction blocks into %s — "+
			"--uninstall-task-eta never recorded the opt-out (#1437). File now:\n%s",
			claudeMd, readFileForMessage(claudeMd))
	}

	if got := livePermissionState(t, d2.addr, claudecode.AdapterName, claudecode.PermissionKeyInstructions); got != "denied" {
		t.Errorf("restarted daemon reports %s/%s = %q, want \"denied\"",
			claudecode.AdapterName, claudecode.PermissionKeyInstructions, got)
	}
}

// TestUninstallTaskEtaPreservesUserProse is a LOCK, not a defect test: it pins
// behaviour that must NOT change, and passes on main by construction.
//
// ~/.claude/CLAUDE.md is user-authored. The removal is sentinel-delimited and
// preserves surrounding content byte-for-byte, and #1437 must not become the
// change that made a command touching that file take anything else with it.
// Asserted over a full install→uninstall round trip through the real binary
// rather than over removeManagedBlock's unit surface, because what a user loses
// is bytes in their file, not a return value.
func TestUninstallTaskEtaPreservesUserProse(t *testing.T) {
	bin := buildIrrlichd(t)

	homeDir := t.TempDir()
	stateDir := shortTempDir(t)
	seedGrantedInstructionsConsent(t, stateDir)
	claudeMd := seedUserAuthoredCLAUDEMd(t, homeDir)

	d := bootSmokeDaemonIn(t, bin, homeDir, stateDir)
	if !pollUntil(t, 15*time.Second, 25*time.Millisecond, func() bool { return instructionBlocksPresent(claudeMd) }) {
		t.Fatalf("the daemon never installed the managed instruction blocks into %s at boot", claudeMd)
	}
	runUninstallTaskEta(t, bin, homeDir, stateDir)
	d.shutdown(t)

	got, err := os.ReadFile(claudeMd)
	if err != nil {
		t.Fatalf("read %s: %v", claudeMd, err)
	}
	if string(got) != userProseInCLAUDEMd {
		t.Errorf("install→uninstall did not round-trip the user's own CLAUDE.md.\n--- want ---\n%s\n--- got ---\n%s",
			userProseInCLAUDEMd, got)
	}
}

// TestUninstallTaskEtaWithNoDaemonRunning covers the command with nothing to
// notify. Two arms with different standing, stated rather than blurred:
//
//   - The exit status and the "No running daemon to notify" line are a LOCK.
//     --uninstall-task-eta worked with no daemon before this change and has to
//     keep working; a consent-changed signal that cannot be delivered is not an
//     error. (The exit-status half passes on main; the message half cannot,
//     since main sends no signal at all.)
//   - The stored StateDenied is the DEFECT assertion, and is red on main.
func TestUninstallTaskEtaWithNoDaemonRunning(t *testing.T) {
	bin := buildIrrlichd(t)

	homeDir := t.TempDir()
	stateDir := t.TempDir()
	seedGrantedInstructionsConsent(t, stateDir)

	cmd := exec.Command(bin, "--uninstall-task-eta")
	cmd.Env = sanitizedChildEnv(homeDir, stateDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--uninstall-task-eta with no daemon running exited %v, want success\n%s", err, out)
	}
	if !strings.Contains(string(out), "No running daemon to notify") {
		t.Errorf("output did not report the missing daemon:\n%s", out)
	}

	// The store must still record the opt-out (#570): an undeliverable signal
	// changes nothing about what gets persisted.
	if got := storedPermissionState(t, stateDir, claudecode.AdapterName, claudecode.PermissionKeyInstructions); got != permission.StateDenied {
		t.Errorf("permissions.json records %s/%s = %q with no daemon running, want \"denied\"",
			claudecode.AdapterName, claudecode.PermissionKeyInstructions, got)
	}
}

// TestUninstallTaskEtaLeavesHooksConsentAlone is the symmetric guard to
// managedfiles_test.go's "--uninstall-hooks must not revoke instructions": this
// command's name promises the CLAUDE.md blocks, so it must not deny the hooks
// permission on the way past. It is a LOCK on the narrowing, and its value is
// that it fails the moment somebody reaches for ManagedUserFiles here instead
// of the instructions slice.
func TestUninstallTaskEtaLeavesHooksConsentAlone(t *testing.T) {
	bin := buildIrrlichd(t)

	homeDir := t.TempDir()
	stateDir := t.TempDir()

	set := permission.Set{}
	set.Put(claudecode.AdapterName, claudecode.PermissionKeyInstructions, permission.StateGranted)
	set.Put(claudecode.AdapterName, claudecode.PermissionKeyHooks, permission.StateGranted)
	if err := filesystem.NewPermissionStore(stateDir).Save(set); err != nil {
		t.Fatalf("seed permissions store in %s: %v", stateDir, err)
	}

	cmd := exec.Command(bin, "--uninstall-task-eta")
	cmd.Env = sanitizedChildEnv(homeDir, stateDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--uninstall-task-eta exited %v\n%s", err, out)
	}

	// The positive half, and it is not optional. Asserting only that hooks
	// stayed granted passes just as well when the deny disappears ENTIRELY —
	// which is the state of main, where it passes today. "Denied exactly this,
	// and nothing else" is one assertion pair; splitting it leaves a lock that
	// survives the collapse it is supposed to catch.
	if got := storedPermissionState(t, stateDir, claudecode.AdapterName, claudecode.PermissionKeyInstructions); got != permission.StateDenied {
		t.Errorf("--uninstall-task-eta recorded %s/%s = %q, want \"denied\" — the narrowing assertion below "+
			"would otherwise pass vacuously against a command that denies nothing at all",
			claudecode.AdapterName, claudecode.PermissionKeyInstructions, got)
	}
	if got := storedPermissionState(t, stateDir, claudecode.AdapterName, claudecode.PermissionKeyHooks); got != permission.StateGranted {
		t.Errorf("--uninstall-task-eta changed %s/%s to %q — it must touch the instructions permission and nothing else",
			claudecode.AdapterName, claudecode.PermissionKeyHooks, got)
	}
}

// runUninstallTaskEta runs the flag in a child process sharing the live
// daemon's HOME and IRRLICHT_HOME — how a user running it by hand reaches the
// same daemon, and how the CLI resolves that daemon's published address.
func runUninstallTaskEta(t *testing.T, bin, homeDir, stateDir string) {
	t.Helper()
	cmd := exec.Command(bin, "--uninstall-task-eta")
	cmd.Env = sanitizedChildEnv(homeDir, stateDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--uninstall-task-eta exited %v\n%s", err, out)
	}
	t.Logf("--uninstall-task-eta output:\n%s", out)
}

// seedGrantedInstructionsConsent writes a permissions.json granting Claude
// Code's instructions permission, so the daemon under test boots into the only
// state in which this defect can occur: consent granted, blocks installed by
// the boot Apply.
//
// Through the daemon's own store rather than a hand-written JSON literal, for
// the reason seedGrantedHooksConsent gives: the on-disk shape belongs to
// filesystem.PermissionStore and is documented as bumpable.
func seedGrantedInstructionsConsent(t *testing.T, stateDir string) {
	t.Helper()
	set := permission.Set{}
	set.Put(claudecode.AdapterName, claudecode.PermissionKeyInstructions, permission.StateGranted)
	if err := filesystem.NewPermissionStore(stateDir).Save(set); err != nil {
		t.Fatalf("seed permissions store in %s: %v", stateDir, err)
	}
}

// seedUserAuthoredCLAUDEMd writes prose the user owns into the temp HOME's
// ~/.claude/CLAUDE.md and returns its path, so every case here runs against a
// file that was NOT created by irrlicht.
func seedUserAuthoredCLAUDEMd(t *testing.T, homeDir string) string {
	t.Helper()
	dir := filepath.Join(homeDir, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make %s: %v", dir, err)
	}
	path := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(path, []byte(userProseInCLAUDEMd), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// instructionBlocksPresent reports whether a CLAUDE.md currently carries any
// irrlicht-managed block, matched on the installer's own sentinel prefix so the
// two cannot drift — the same discipline hookEntriesPresent uses.
func instructionBlocksPresent(claudeMdPath string) bool {
	b, err := os.ReadFile(claudeMdPath)
	if err != nil {
		return false
	}
	return bytes.Contains(b, []byte(claudecode.ManagedBlockSentinelPrefix))
}
