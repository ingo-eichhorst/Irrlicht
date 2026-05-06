//go:build darwin

package processlifecycle

import (
	"path/filepath"
	"strings"
)

// termProgramByAppName maps the `.app` bundle name from a process's
// executable path to the canonical $TERM_PROGRAM string the Swift
// launcher dispatcher expects. Keys match the directory segment that
// precedes `.app/Contents/MacOS/` on macOS.
//
// The value side is what Swift uses for registry lookup — every entry
// here must have a matching activator in
// `platforms/macos/Irrlicht/Managers/SessionLauncher.swift:activators`.
// Adding a new host means adding one line here (so the hardened-runtime
// ancestry fallback can identify it when env is stripped) AND one line
// on the Swift side.
var termProgramByAppName = map[string]string{
	"iTerm":              "iTerm.app",
	"Terminal":           "Apple_Terminal",
	"Visual Studio Code": "vscode",
	"Cursor":             "cursor",
	"Windsurf":           "windsurf",
	"Ghostty":            "ghostty",
	"WezTerm":            "WezTerm",
	"Hyper":              "Hyper",
	"Warp":               "Warp",
	// JetBrains IDEs — all embed JediTerm; shared Swift activator fans out to
	// whichever IDE bundle is currently running.
	"GoLand":           "jetbrains",
	"IntelliJ IDEA":    "jetbrains",
	"IntelliJ IDEA CE": "jetbrains",
	"PyCharm":          "jetbrains",
	"PyCharm CE":       "jetbrains",
	"WebStorm":         "jetbrains",
	"Rider":            "jetbrains",
	"CLion":            "jetbrains",
	"RustRover":        "jetbrains",
	"Zed":              "zed",
	"kitty":            "kitty",
	"Rio":              "rio",
	"Tabby":            "tabby",
	"Wave":             "waveterm",
	"Alacritty":        "alacritty",
	"Nova":             "nova",
	"cmux":             "cmux",
}

// termProgramForAppPath extracts the host app's canonical TERM_PROGRAM
// value from an executable path of the form
// `/Applications/<App>.app/Contents/MacOS/<binary>`. Returns "" for paths
// without an `.app` segment or when the app isn't one of the hosts we
// know how to activate.
func termProgramForAppPath(cmdPath string) string {
	idx := strings.Index(cmdPath, ".app/")
	if idx < 0 {
		return ""
	}
	head := cmdPath[:idx]
	appName := filepath.Base(head)
	return termProgramByAppName[appName]
}
