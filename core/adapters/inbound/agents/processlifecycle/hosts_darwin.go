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

// topLevelAppPath returns the path of the top-level application bundle whose
// main executable is cmdPath, or "" when cmdPath is not the main executable of
// a real top-level `.app`. It backs the generic host fallback (for embedded
// terminals whose host isn't in termProgramByAppName, e.g. Obsidian).
//
// "Top-level" means the `.app` that directly wraps the executable
// (`<App>.app/Contents/MacOS/<binary>`) is NOT itself nested inside another
// `.app`, a `.framework`, or a `Contents/Frameworks/` directory. That nesting
// test is what rejects the two ancestors a naive walk would wrongly latch onto:
//   - Electron helper processes — `Foo.app/Contents/Frameworks/Foo Helper.app/...`
//   - framework-embedded interpreters — a Python PTY helper at
//     `Xcode.app/.../Python3.framework/.../Python.app/...`
//
// Neither is the GUI app a click should bring forward, so both return "" and
// the ancestry walk continues to the real owner. (termProgramForAppPath keys
// off the *first* `.app/` instead, which is correct for curated hosts because
// the map only matches known top-level apps.)
func topLevelAppPath(cmdPath string) string {
	const marker = ".app/Contents/MacOS/"
	idx := strings.Index(cmdPath, marker)
	if idx < 0 {
		return ""
	}
	prefix := cmdPath[:idx] // everything up to (not including) the wrapping ".app"
	if strings.Contains(prefix, ".app/") ||
		strings.Contains(prefix, ".framework/") ||
		strings.Contains(prefix, "Contents/Frameworks/") {
		return ""
	}
	return cmdPath[:idx+len(".app")]
}
