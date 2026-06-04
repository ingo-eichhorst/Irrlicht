// kitty_permission.go declares the consent surface for irrlicht's kitty
// remote-control config patch (issue #425). Tab-precise click-to-focus needs
// kitty started with allow_remote_control + listen_on; instead of pointing
// the user at docs after the feature silently degrades, the wizard offers a
// managed, reversible patch. Apply/Remove own a sentinel-delimited block in
// kitty.conf so grant and revoke round-trip without touching user-authored
// lines.
package processlifecycle

import (
	"os"
	"path/filepath"
	"strings"

	"irrlicht/core/domain/agent"
	"irrlicht/core/domain/permission"
)

// KittyName identifies the kitty config-patch pseudo-entry in the
// permission store and wizard.
const KittyName = "kitty"

// PermissionKeyKittyConfig gates the kitty.conf remote-control patch.
const PermissionKeyKittyConfig = "remote-control"

// Sentinel markers delimiting the irrlicht-managed block in kitty.conf.
// Used by install (idempotency + stale-content upgrade) and uninstall.
const (
	kittyBlockStart = "# >>> irrlicht remote control >>>"
	kittyBlockEnd   = "# <<< irrlicht remote control <<<"
)

// kittyManagedBlock is the canonical block the patch maintains. listen_on
// makes kitty export KITTY_LISTEN_ON to child processes; the macOS
// activator uses that socket for `kitten @ focus-window`
// (KittyActivator.swift). kitty options are last-one-wins, so appending at
// the end of kitty.conf also overrides an earlier conflicting value — which
// is what an explicit grant asks for.
const kittyManagedBlock = kittyBlockStart + "\n" +
	"allow_remote_control yes\n" +
	"listen_on unix:/tmp/kitty\n" +
	kittyBlockEnd + "\n"

// KittyPermissionDeclaration returns the consent declaration for the kitty
// config patch. Like the launcher entry it isn't a coding-agent adapter (no
// Source/Process axes) — it's a host-integration capability gated through
// the same wizard.
func KittyPermissionDeclaration() agent.Agent {
	return agent.Agent{
		Identity: agent.Identity{Name: KittyName, DisplayName: "kitty"},
		Permissions: []agent.Permission{{
			Key:             PermissionKeyKittyConfig,
			Kind:            permission.KindModify,
			Title:           "Enable kitty remote control",
			FeatureUnlocked: "Tab-precise click-to-focus: clicking a session row raises the exact kitty window and tab that runs it",
			Touches:         "Adds allow_remote_control + listen_on to ~/.config/kitty/kitty.conf",
			Detail: "Appends a block between \"" + kittyBlockStart + "\" and \"" +
				kittyBlockEnd + "\" markers containing `allow_remote_control yes` " +
				"and `listen_on unix:/tmp/kitty` to ~/.config/kitty/kitty.conf " +
				"(XDG_CONFIG_HOME honored; file created if missing). kitty picks " +
				"the change up the next time it starts. Revoking removes exactly " +
				"that block — lines you wrote yourself are never modified. " +
				"Without the grant, clicking a kitty session still raises the " +
				"right kitty instance but stays on its last-focused tab.",
			Apply:  func() error { _, err := EnsureKittyConfigPatched(); return err },
			Remove: func() error { _, err := UninstallKittyConfig(); return err },
		}},
	}
}

// KittyDetected reports whether kitty is in use on this machine — a live
// kitty process or an existing kitty config directory. Drives when the
// wizard shows the kitty entry; the detection poller re-checks while the
// permission is pending, so kitty installed (or first launched) later still
// surfaces the prompt.
func KittyDetected() bool {
	if HasLiveProcess(agent.ExactName{Name: "kitty"}) {
		return true
	}
	dir, err := kittyConfigDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(dir)
	return err == nil
}

// EnsureKittyConfigPatched adds (or refreshes) the irrlicht-managed block in
// kitty.conf. Creates the file if it doesn't exist; a stale block (markers
// present but content drifted) is rewritten to the canonical form. Returns
// true if the file was modified, false if the block was already current.
func EnsureKittyConfigPatched() (bool, error) {
	path, err := kittyConfPath()
	if err != nil {
		return false, err
	}
	content, err := readKittyConf(path)
	if err != nil {
		return false, err
	}
	stripped, _ := removeKittyBlock(content)
	updated := appendKittyBlock(stripped)
	if updated == content {
		return false, nil
	}
	return true, atomicWriteFile(path, []byte(updated))
}

// UninstallKittyConfig removes the irrlicht-managed block from kitty.conf.
// Returns true if the file was modified, false if no block was found.
func UninstallKittyConfig() (bool, error) {
	path, err := kittyConfPath()
	if err != nil {
		return false, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	stripped, found := removeKittyBlock(string(data))
	if !found {
		return false, nil
	}
	return true, atomicWriteFile(path, []byte(stripped))
}

// --- helpers ---

func kittyConfigDir() (string, error) {
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "kitty"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "kitty"), nil
}

func kittyConfPath() (string, error) {
	dir, err := kittyConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "kitty.conf"), nil
}

func readKittyConf(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// appendKittyBlock appends the canonical managed block, separated from any
// existing content by one blank line.
func appendKittyBlock(content string) string {
	base := strings.TrimRight(content, "\n")
	if base == "" {
		return kittyManagedBlock
	}
	return base + "\n\n" + kittyManagedBlock
}

// removeKittyBlock cuts the marker-delimited block (inclusive) plus the
// blank separator line appendKittyBlock added before it. Returns the
// remaining content and whether a block was found.
func removeKittyBlock(content string) (string, bool) {
	startIdx := strings.Index(content, kittyBlockStart)
	if startIdx == -1 {
		return content, false
	}
	endIdx := strings.Index(content[startIdx:], kittyBlockEnd)
	if endIdx == -1 {
		return content, false
	}
	endIdx += startIdx + len(kittyBlockEnd)
	if endIdx < len(content) && content[endIdx] == '\n' {
		endIdx++
	}
	before := content[:startIdx]
	after := content[endIdx:]
	// Drop the blank separator line so apply→remove restores the original.
	if strings.HasSuffix(before, "\n\n") {
		before = before[:len(before)-1]
	}
	merged := before + after
	if strings.TrimSpace(merged) == "" {
		merged = ""
	}
	return merged, true
}

// atomicWriteFile writes data to path via a temp file + rename, creating
// the parent dir. Mirrors the claudecode installer's writer.
func atomicWriteFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
