package vibe

import (
	"os"
	"path/filepath"
	"strings"

	"irrlicht/core/pkg/cliversion"
)

// uvPackageName is the name `uv tool install mistral-vibe` gives the tool's
// directory, and uvDistInfoPrefix the start of its dist-info folder name, as
// laid out on this machine by uv 0.8.19 for the installed 2.19.1:
// ~/.local/share/uv/tools/mistral-vibe/lib/python3.12/site-packages/
// mistral_vibe-2.19.1.dist-info.
const (
	uvPackageName    = "mistral-vibe"
	uvDistInfoPrefix = "mistral_vibe-"
	uvDistInfoSuffix = ".dist-info"
)

// installedVersionFromUvTool is the hooks permission's VersionGate.Observed
// (#2171): the version of a uv-installed Vibe, read from the NAME of its
// dist-info folder. It runs nothing and opens no file's contents: it lists the
// directories its glob passes through (filepath.Glob lists lib/, each
// python*/ and each site-packages/ under the tool's folder), and returns ""
// whenever it cannot answer, so the gate falls through to its probe
// (`vibe --version`).
//
// It exists because the probe often cannot answer for the install method
// Vibe documents. `uv tool install` puts the `vibe` launcher in
// ~/.local/bin, and a daemon started by launchd does not have that directory
// on its PATH: cliprobe looks in root-owned bin directories, then the
// daemon's own PATH (core/pkg/cliprobe/cliprobe.go's Probe doc), and its
// TestProbe_MissingBinaryFailsImmediately names exactly this case. Whether the
// probe failed on the machine #2171 was reported from was not checked. With two hook
// artifacts, an unknown version writes neither (agent.ManagedUserFile
// .SelectVariant), so without this source a uv-installed Vibe under the
// daemon Irrlicht.app starts would never get a hook.
//
// It reads nothing the hooks permission's consent copy does not name (the
// copy names the uv tool directory), so it declares no ObservedRequires. A
// Vibe installed some other way (pipx, pip) has no folder here and gets the
// probe. When several dist-info folders are present, the highest parseable
// version wins.
func installedVersionFromUvTool() string {
	dir := uvToolDir()
	if dir == "" {
		return ""
	}
	matches, err := filepath.Glob(filepath.Join(dir, uvPackageName, "lib", "python*", "site-packages",
		uvDistInfoPrefix+"*"+uvDistInfoSuffix))
	if err != nil {
		return ""
	}
	best := ""
	for _, m := range matches {
		v := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m), uvDistInfoPrefix), uvDistInfoSuffix)
		if _, ok := cliversion.Parse(v); !ok {
			continue
		}
		if best == "" || cliversion.Less(best, v) {
			best = v
		}
	}
	return best
}

// uvToolDir resolves uv's tool directory the way uv does, as measured with
// `uv tool dir` (uv 0.8.19) on the machine this was written on: UV_TOOL_DIR
// first, then $XDG_DATA_HOME/uv/tools, then ~/.local/share/uv/tools. uv
// ignores an empty or relative XDG_DATA_HOME, and so does this. A relative
// UV_TOOL_DIR is resolved by uv against ITS working directory, which the
// daemon cannot know, so that answers "" rather than guessing a different
// directory. Only the daemon's own environment is consulted, so a variable
// the user set only in a shell profile, which a daemon launchd starts does not
// read (not measured here), leaves the default directory to be read.
func uvToolDir() string {
	if v := os.Getenv("UV_TOOL_DIR"); v != "" {
		if !filepath.IsAbs(v) {
			return ""
		}
		return filepath.Clean(v)
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, "uv", "tools")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "share", "uv", "tools")
}
