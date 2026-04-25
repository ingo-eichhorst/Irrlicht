//go:build darwin

package processlifecycle

import (
	"os"
	"testing"
)

func TestTermProgramForAppPath(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"/Applications/Visual Studio Code.app/Contents/MacOS/Code", "vscode"},
		{"/Applications/Visual Studio Code.app/Contents/Frameworks/Code Helper.app/Contents/MacOS/Code Helper", "vscode"},
		{"/Applications/iTerm.app/Contents/MacOS/iTerm2", "iTerm.app"},
		{"/System/Applications/Utilities/Terminal.app/Contents/MacOS/Terminal", "Apple_Terminal"},
		{"/Applications/Cursor.app/Contents/MacOS/Cursor", "cursor"},
		{"/Applications/Ghostty.app/Contents/MacOS/ghostty", "ghostty"},
		{"/Applications/Warp.app/Contents/MacOS/stable", "Warp"},
		{"/Applications/WezTerm.app/Contents/MacOS/wezterm-gui", "WezTerm"},
		{"/Applications/Hyper.app/Contents/MacOS/Hyper", "Hyper"},
		{"/Applications/Windsurf.app/Contents/MacOS/Windsurf", "windsurf"},
		// JetBrains IDEs
		{"/Users/ingo/Applications/GoLand.app/Contents/MacOS/goland", "jetbrains"},
		{"/Applications/IntelliJ IDEA.app/Contents/MacOS/idea", "jetbrains"},
		{"/Applications/IntelliJ IDEA CE.app/Contents/MacOS/idea", "jetbrains"},
		{"/Applications/PyCharm.app/Contents/MacOS/pycharm", "jetbrains"},
		{"/Applications/PyCharm CE.app/Contents/MacOS/pycharm", "jetbrains"},
		{"/Applications/WebStorm.app/Contents/MacOS/webstorm", "jetbrains"},
		{"/Applications/Rider.app/Contents/MacOS/rider", "jetbrains"},
		{"/Applications/CLion.app/Contents/MacOS/clion", "jetbrains"},
		{"/Applications/RustRover.app/Contents/MacOS/rustrover", "jetbrains"},
		// Additional hosts
		{"/Applications/Zed.app/Contents/MacOS/zed", "zed"},
		{"/Applications/kitty.app/Contents/MacOS/kitty", "kitty"},
		{"/Applications/Rio.app/Contents/MacOS/rio", "rio"},
		{"/Applications/Tabby.app/Contents/MacOS/tabby", "tabby"},
		{"/Applications/Wave.app/Contents/MacOS/wave", "waveterm"},
		{"/Applications/Alacritty.app/Contents/MacOS/alacritty", "alacritty"},
		{"/Applications/Nova.app/Contents/MacOS/nova", "nova"},
		{"/Applications/cmux.app/Contents/MacOS/cmux", "cmux"},
		// No .app segment: not a host we know.
		{"/bin/zsh", ""},
		{"/Users/ingo/.local/share/claude/versions/2.1.114", ""},
		{"/usr/bin/tmux", ""},
		// .app appears in a path fragment but not as a bundle boundary.
		{"/tmp/not.appended/bin/thing", ""},
		{"", ""},
	}
	for _, tc := range tests {
		if got := termProgramForAppPath(tc.in); got != tc.want {
			t.Errorf("termProgramForAppPath(%q): got %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestResolveTermProgramFromAncestry_Self walks the ancestry of the running
// test binary. We don't know what terminal launched the developer's `go test`
// invocation, so we only assert that the helper either finds a supported host
// (non-empty) or returns "" cleanly — never errors or panics.
func TestResolveTermProgramFromAncestry_Self(t *testing.T) {
	got := resolveTermProgramFromAncestry(os.Getpid())
	if got != "" {
		if _, known := termProgramByAppName[reverseLookup(got)]; !known {
			t.Errorf("resolveTermProgramFromAncestry returned unknown TermProgram %q", got)
		}
	}
}

func reverseLookup(termProgram string) string {
	for k, v := range termProgramByAppName {
		if v == termProgram {
			return k
		}
	}
	return ""
}
